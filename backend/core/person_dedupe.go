package core

import (
	"context"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/rs/zerolog/log"
)

// PersonDedupMember is a person row with the signals needed to pick the
// canonical survivor of a same-entity cluster.
type PersonDedupMember struct {
	ID         int64
	Name       string
	EntityType string
	FactCount  int
	// Curated is true for a curated French Assemblée entity; such rows are
	// preferred survivors and are never merged into one another.
	Curated bool
}

// PersonDuplicateCandidate is a trigram-similar pair of persons.
type PersonDuplicateCandidate struct {
	IDA         int64
	IDB         int64
	NameA       string
	NameB       string
	EntityTypeA string
	EntityTypeB string
	FactCountA  int
	FactCountB  int
	Similarity  float64
}

// PersonDedupStorer is the storage view of the person dedupe passes.
// storage.PostgresStorer implements it; the survivor rule (ChooseCanonical)
// and the repointing (MergePersons) stay in storage.
type PersonDedupStorer interface {
	FindDuplicateCandidates(ctx context.Context, entityType string, threshold float64) ([]PersonDuplicateCandidate, error)
	GetPersonsForDedup(ctx context.Context, ids []int64) ([]PersonDedupMember, error)
	GetAllDedupMembers(ctx context.Context) ([]PersonDedupMember, error)
	// ChooseCanonical picks the survivor of a confirmed cluster; ok is false
	// when the cluster holds two or more curated members (never merged).
	ChooseCanonical(members []PersonDedupMember) (PersonDedupMember, bool)
	MergePersons(ctx context.Context, fromID, intoID int64) error
	RenamePerson(ctx context.Context, id int64, name string) error
}

// EntityPairAdjudicator decides whether two similarly-named persons are the
// same real-world entity. EntityDedupAdjudicator implements it.
type EntityPairAdjudicator interface {
	AdjudicatePair(ctx context.Context, a, b EntityRef) (*PairVerdict, error)
}

// maxConfirmedClusterSize is a defense-in-depth cap: with pairwise adjudication
// a confirmed cluster should stay tiny, so anything larger is suspicious and we
// refuse to bulk-merge it silently.
const maxConfirmedClusterSize = 12

// PersonDedupeOptions tunes DedupePersons.
type PersonDedupeOptions struct {
	EntityType string  // human|party|organization; empty = all
	Threshold  float64 // minimum trigram similarity for candidate pairs
	Apply      bool    // merge; otherwise only report what would merge
}

// DefaultPersonDedupeOptions are the thresholds `dm persons dedupe` uses.
var DefaultPersonDedupeOptions = PersonDedupeOptions{Threshold: 0.45}

// Reasons a confirmed cluster is skipped.
const (
	PersonClusterSkipCap     = "cap"     // more members than maxConfirmedClusterSize
	PersonClusterSkipCurated = "curated" // two or more curated entities
)

// PersonMergeOutcome is one non-canonical member of a cluster: merged, would
// merge (dry run), or failed.
type PersonMergeOutcome struct {
	From    PersonDedupMember
	Into    PersonDedupMember
	Applied bool
	Err     error
}

// PersonDedupeCluster is one confirmed same-entity cluster and what happened
// to it.
type PersonDedupeCluster struct {
	IDs        []int64
	SkipReason string // empty when processed; PersonClusterSkip* otherwise
	Members    []PersonDedupMember
	Canonical  PersonDedupMember // zero when skipped
	Merges     []PersonMergeOutcome
}

// PersonDedupeResult is the outcome of DedupePersons.
type PersonDedupeResult struct {
	Candidates      []PersonDuplicateCandidate
	PairsConfirmed  int
	PairsRejected   int
	PairErrors      int
	ClustersSkipped int
	ClusterErrors   int
	WouldMerge      int
	Merged          int
	MergeFailed     int
	Clusters        []PersonDedupeCluster
	// MaxClusterSize is the safety cap a cluster must stay under.
	MaxClusterSize int
}

// DedupePersons finds trigram-similar persons, asks the adjudicator pairwise
// whether each candidate pair is the same entity, clusters the CONFIRMED pairs
// (union-find, so chaining through unconfirmed links is impossible), and — with
// opts.Apply — merges the non-canonical members of each cluster into the
// rule-chosen survivor. Per-pair and per-cluster failures are logged, counted
// and skipped.
func DedupePersons(ctx context.Context, storer PersonDedupStorer, adjudicator EntityPairAdjudicator, opts PersonDedupeOptions) (PersonDedupeResult, error) {
	res := PersonDedupeResult{MaxClusterSize: maxConfirmedClusterSize}

	candidates, err := storer.FindDuplicateCandidates(ctx, opts.EntityType, opts.Threshold)
	if err != nil {
		return res, fmt.Errorf("failed to find duplicate candidates: %w", err)
	}
	res.Candidates = candidates
	if len(candidates) == 0 {
		return res, nil
	}

	// 1. Adjudicate each candidate pair; keep only confirmed same-entity pairs.
	var confirmedPairs [][2]int64
	for _, c := range candidates {
		a := EntityRef{ID: c.IDA, Name: c.NameA, EntityType: c.EntityTypeA, FactCount: c.FactCountA}
		b := EntityRef{ID: c.IDB, Name: c.NameB, EntityType: c.EntityTypeB, FactCount: c.FactCountB}
		verdict, err := adjudicator.AdjudicatePair(ctx, a, b)
		if err != nil {
			res.PairErrors++
			log.Error().Err(err).
				Int64("id_a", c.IDA).Str("name_a", c.NameA).
				Int64("id_b", c.IDB).Str("name_b", c.NameB).
				Msg("Pair adjudication failed; skipping pair")
			continue
		}
		if !verdict.Same {
			res.PairsRejected++
			log.Debug().
				Int64("id_a", c.IDA).Str("name_a", c.NameA).
				Int64("id_b", c.IDB).Str("name_b", c.NameB).
				Str("reasoning", verdict.Reasoning).
				Msg("LLM judged pair as DIFFERENT entities; not merging")
			continue
		}
		res.PairsConfirmed++
		confirmedPairs = append(confirmedPairs, [2]int64{c.IDA, c.IDB})
	}

	// 2. Cluster only the confirmed pairs — chaining through unconfirmed links is
	//    structurally impossible.
	for _, ids := range ClusterPairs(confirmedPairs) {
		cluster := PersonDedupeCluster{IDs: ids}

		// Defense-in-depth: never bulk-merge a mega-cluster silently.
		if len(ids) > maxConfirmedClusterSize {
			res.ClustersSkipped++
			log.Warn().
				Ints64("ids", ids).
				Int("size", len(ids)).
				Int("cap", maxConfirmedClusterSize).
				Msg("Confirmed cluster exceeds safety cap; skipping (will not bulk-merge)")
			cluster.SkipReason = PersonClusterSkipCap
			res.Clusters = append(res.Clusters, cluster)
			continue
		}

		members, err := storer.GetPersonsForDedup(ctx, ids)
		if err != nil {
			res.ClusterErrors++
			log.Error().Err(err).Interface("ids", ids).Msg("Failed to load cluster members; skipping")
			continue
		}
		if len(members) < 2 {
			continue
		}
		cluster.Members = members

		canonical, ok := storer.ChooseCanonical(members)
		if !ok {
			// Two or more curated members — ChooseCanonical already logged it.
			res.ClustersSkipped++
			cluster.SkipReason = PersonClusterSkipCurated
			res.Clusters = append(res.Clusters, cluster)
			continue
		}
		cluster.Canonical = canonical

		for _, m := range members {
			if m.ID == canonical.ID {
				continue
			}
			res.WouldMerge++
			out := PersonMergeOutcome{From: m, Into: canonical}
			if opts.Apply {
				if err := storer.MergePersons(ctx, m.ID, canonical.ID); err != nil {
					res.MergeFailed++
					log.Error().Err(err).Int64("from", m.ID).Int64("into", canonical.ID).Msg("Merge failed")
					out.Err = err
				} else {
					res.Merged++
					out.Applied = true
				}
			}
			cluster.Merges = append(cluster.Merges, out)
		}
		res.Clusters = append(res.Clusters, cluster)
	}
	return res, nil
}

// PersonUnaccentGroup is one set of person rows colliding on the canonical
// name form.
type PersonUnaccentGroup struct {
	Canonical string
	Members   []PersonDedupMember
	Survivor  PersonDedupMember // zero when Skipped
	FromIDs   []int64           // members merged (or to merge) into the survivor
	Skipped   bool              // two or more curated members
}

// PersonUnaccentResult is the outcome of UnaccentPersons.
type PersonUnaccentResult struct {
	Groups     []PersonUnaccentGroup
	DupGroups  int
	MergedAway int
	Skipped    int
	Renamed    int
}

// UnaccentPersons merges person rows that collide on the canonical person-name
// form (util.NormalizePersonName) and, with apply, rewrites every name to that
// form, so accent, curly-apostrophe, fancy-dash and whitespace variants can
// never split a person. Without apply it only reports the groups.
func UnaccentPersons(ctx context.Context, storer PersonDedupStorer, apply bool) (PersonUnaccentResult, error) {
	var res PersonUnaccentResult

	members, err := storer.GetAllDedupMembers(ctx)
	if err != nil {
		return res, fmt.Errorf("failed to load persons: %w", err)
	}

	// Group by the canonical form — IDENTICAL to what names are stored under.
	byCanonical := map[string][]PersonDedupMember{}
	for _, m := range members {
		k := util.NormalizePersonName(m.Name)
		byCanonical[k] = append(byCanonical[k], m)
	}

	for canonical, g := range byCanonical {
		if len(g) < 2 {
			continue
		}
		res.DupGroups++
		group := PersonUnaccentGroup{Canonical: canonical, Members: g}
		survivor, ok := storer.ChooseCanonical(g)
		if !ok {
			res.Skipped++
			group.Skipped = true
			res.Groups = append(res.Groups, group)
			log.Warn().Str("canonical", canonical).Int("members", len(g)).
				Msg("Skipping group (2+ curated members — won't merge real entities)")
			continue
		}
		group.Survivor = survivor
		for _, m := range g {
			if m.ID != survivor.ID {
				group.FromIDs = append(group.FromIDs, m.ID)
			}
		}
		res.Groups = append(res.Groups, group)
		if apply {
			for _, from := range group.FromIDs {
				if err := storer.MergePersons(ctx, from, survivor.ID); err != nil {
					return res, fmt.Errorf("merge %d into %d (%q): %w", from, survivor.ID, canonical, err)
				}
				res.MergedAway++
			}
		}
	}

	if apply {
		// Rewrite every remaining name to its canonical form (collision-free now
		// that all canonical-colliding rows were merged above).
		members, err = storer.GetAllDedupMembers(ctx)
		if err != nil {
			return res, fmt.Errorf("failed to reload persons: %w", err)
		}
		for _, m := range members {
			if c := util.NormalizePersonName(m.Name); c != m.Name {
				if err := storer.RenamePerson(ctx, m.ID, c); err != nil {
					return res, fmt.Errorf("canonicalize %d: %w", m.ID, err)
				}
				res.Renamed++
			}
		}
		log.Info().Int("dup_groups", res.DupGroups).Int("rows_merged_away", res.MergedAway).
			Int("groups_skipped", res.Skipped).Int("names_canonicalized", res.Renamed).Msg("Unaccent complete")
	}
	return res, nil
}
