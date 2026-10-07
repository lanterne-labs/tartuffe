package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
)

// ClaimGroup is a group parent (an active fact) and the person it belongs to.
type ClaimGroup struct {
	ParentID   int64
	PersonID   int64
	PersonName string
	Text       string
}

// GroupReviewStorer is the storage view the claim-group review needs.
// *storage.PostgresStorer implements it.
type GroupReviewStorer interface {
	// ListClaimGroups returns the groups that gained a member at or after
	// since, oldest parent first; onlyIDs, when non-empty, restricts the
	// listing to those parents.
	ListClaimGroups(ctx context.Context, since time.Time, onlyIDs []int64) ([]ClaimGroup, error)
	// GetGroupMembers returns the parent's members (id and text).
	GetGroupMembers(ctx context.Context, parentID int64) ([]FactForFold, error)
	ClaimEvidenceStorer
	// UngroupFact makes a member active again, in the parent's stance.
	UngroupFact(ctx context.Context, parentID, factID int64) error
	RefreshPersonScore(ctx context.Context, personID int64) error
}

// GroupReviewOptions tunes ReviewClaimGroups.
type GroupReviewOptions struct {
	Since time.Time
	// OnlyIDs restricts the review to these group parents.
	OnlyIDs []int64
	// Apply un-groups the members that no longer belong. Without it the
	// review only reports.
	Apply bool
	// StartAfter skips the groups whose parent id is at most this value, to
	// resume a cut run (groups are reviewed in parent id order).
	StartAfter int64
}

// GroupReviewStats are the counters of one review.
type GroupReviewStats struct {
	Groups       int64
	Intact       int64 // every member still belongs
	Trimmed      int64 // some members un-grouped, the group keeps at least one
	Dissolved    int64 // no member belongs: every member un-grouped
	Ungrouped    int64 // members made active again (Apply)
	NoEvidence   int64 // members out because they meet the evidence rule with no other fact of the group
	GateFailures int64 // groups skipped because a gate call failed
}

// ReviewClaimGroups re-judges groups with the current grouping rule (see
// claim_evidence.go). The parent — an actual statement — anchors the group.
// A member stays only when it meets the evidence rule with another fact of
// the group; a member without that evidence (a vote, a restatement weeks
// later) leaves without a gate call. The other members are taken in id order
// and each stays when the gate confirms it against at least as many kept
// facts as it rejects. A majority, not a clique: an over-grouped member (a
// different claim) fails against every kept fact, while a member the gate is
// borderline on against ONE sibling still agrees with the rest. Members that
// fall out are un-grouped; nothing is deleted. A gate error skips the group:
// it is never trimmed on partial information.
func ReviewClaimGroups(ctx context.Context, storer GroupReviewStorer, judge SameOccurrenceJudge, opts GroupReviewOptions) (GroupReviewStats, error) {
	var st GroupReviewStats
	rule, err := groupEvidenceRule()
	if err != nil {
		return st, err
	}
	groups, err := storer.ListClaimGroups(ctx, opts.Since, opts.OnlyIDs)
	if err != nil {
		return st, fmt.Errorf("listing claim groups: %w", err)
	}
	if opts.StartAfter > 0 {
		var rest []ClaimGroup
		for _, g := range groups {
			if g.ParentID > opts.StartAfter {
				rest = append(rest, g)
			}
		}
		groups = rest
	}
	log.Info().Int("groups", len(groups)).Time("since", opts.Since).Bool("apply", opts.Apply).Msg("Claim group review started")

	for _, g := range groups {
		members, err := storer.GetGroupMembers(ctx, g.ParentID)
		if err != nil {
			return st, fmt.Errorf("loading members of group %d: %w", g.ParentID, err)
		}
		st.Groups++
		sort.Slice(members, func(a, b int) bool { return members[a].ID < members[b].ID })

		// A member joined on evidence with the parent of that time, and a
		// later, more concrete fact can have become the parent since then. So
		// the evidence rule is checked against every other fact of the group.
		groupIDs := []int64{g.ParentID}
		for _, f := range members {
			groupIDs = append(groupIDs, f.ID)
		}
		var pairs [][2]int64
		for i := range groupIDs {
			for j := i + 1; j < len(groupIDs); j++ {
				pairs = append(pairs, [2]int64{groupIDs[i], groupIDs[j]})
			}
		}
		overlapRows, err := storer.ListGroupEvidencePairs(ctx, pairs, rule)
		if err != nil {
			return st, fmt.Errorf("group evidence of group %d: %w", g.ParentID, err)
		}
		sources, err := storer.LoadClaimSources(ctx, groupIDs, rule.SourceTypes)
		if err != nil {
			return st, fmt.Errorf("claim sources of group %d: %w", g.ParentID, err)
		}
		evidenceOf := func(f FactForFold) ClaimEvidence {
			return ClaimEvidence{Text: f.Text, Sources: sources[f.ID]}
		}
		quoted := map[int64]bool{}
		for _, pr := range overlapRows {
			quoted[pr[0]] = true
			quoted[pr[1]] = true
		}

		kept := []FactForFold{{ID: g.ParentID, Text: g.Text}}
		var out []FactForFold
		noEvidence := int64(0)
		failed := false
	members:
		for _, f := range members {
			if !quoted[f.ID] {
				noEvidence++
				out = append(out, f)
				continue
			}
			// The member stays when agree >= disagree over the kept facts:
			// stop as soon as the remaining calls cannot change that.
			agree, disagree := 0, 0
			for _, k := range kept {
				if 2*agree >= len(kept) || 2*disagree > len(kept) {
					break
				}
				verdict, err := judge.IsSameOccurrence(ctx, evidenceOf(k), evidenceOf(f))
				same := verdict.Groupable()
				if err != nil {
					log.Warn().Err(err).Int64("parent_fact_id", g.ParentID).Msg("Claim group review: gate failed; group skipped")
					failed = true
					break members
				}
				if same {
					agree++
				} else {
					disagree++
				}
			}
			if disagree > agree {
				out = append(out, f)
				continue
			}
			kept = append(kept, f)
		}
		if failed {
			st.GateFailures++
			continue
		}
		st.NoEvidence += noEvidence
		switch {
		case len(out) == 0:
			st.Intact++
			continue
		case len(kept) == 1:
			st.Dissolved++
		default:
			st.Trimmed++
		}
		for _, f := range out {
			log.Info().Int64("parent_fact_id", g.ParentID).Int64("fact_id", f.ID).Str("person", g.PersonName).
				Str("fact_text", f.Text).Str("parent_text", g.Text).Bool("apply", opts.Apply).
				Msg("Claim group review: member no longer belongs")
		}
		if !opts.Apply {
			continue
		}
		for _, f := range out {
			if err := storer.UngroupFact(ctx, g.ParentID, f.ID); err != nil {
				return st, fmt.Errorf("un-grouping fact %d from %d: %w", f.ID, g.ParentID, err)
			}
			st.Ungrouped++
		}
		if err := storer.RefreshPersonScore(ctx, g.PersonID); err != nil {
			log.Warn().Err(err).Int64("person_id", g.PersonID).Msg("Failed to refresh fact count")
		}
	}

	log.Info().Int64("groups", st.Groups).Int64("intact", st.Intact).Int64("trimmed", st.Trimmed).
		Int64("dissolved", st.Dissolved).Int64("ungrouped", st.Ungrouped).Int64("no_evidence", st.NoEvidence).Int64("gate_failures", st.GateFailures).
		Msg("Claim group review finished")
	return st, nil
}
