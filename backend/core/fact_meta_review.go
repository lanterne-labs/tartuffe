package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
)

// MetaFactRef identifies a meta-fact and the person it belongs to.
type MetaFactRef struct {
	ID         int64
	PersonID   int64
	PersonName string
	Text       string
}

// MetaReviewStorer is the storage view the meta-fact review needs.
// *storage.PostgresStorer implements it.
type MetaReviewStorer interface {
	// ListMetaFacts returns the meta-facts created at or after since, oldest first.
	ListMetaFacts(ctx context.Context, since time.Time) ([]MetaFactRef, error)
	// GetMetaMembers returns the meta's member facts (id and text), oldest first.
	GetMetaMembers(ctx context.Context, metaID int64) ([]FactForFold, error)
	// UnfoldFactFromMeta removes the member's 'duplicate' link to the meta, so
	// the fact is active again.
	UnfoldFactFromMeta(ctx context.Context, metaID, factID int64) error
	UpdateMetaFactText(ctx context.Context, metaID int64, text string) error
	GetFactEmbeddingMeta(ctx context.Context, factID int64) (embeddingText, lang, verb string, err error)
	RefreshPersonScore(ctx context.Context, personID int64) error
}

// MetaReviewOptions tunes ReviewMetaFacts.
type MetaReviewOptions struct {
	Since time.Time
	// Apply unfolds the members the gate no longer confirms and rewrites the
	// meta text from the members that stay. Without it the review only reports.
	Apply bool
}

// MetaReviewStats are the counters of one review.
type MetaReviewStats struct {
	Metas        int64
	Intact       int64 // every member still confirmed against every other
	Trimmed      int64 // some members unfolded, the meta keeps >= 2
	Dissolved    int64 // fewer than 2 members survive: the meta is a phantom (see DissolvedIDs)
	Unfolded     int64 // members made active again (Apply)
	GateFailures int64 // metas skipped because a gate call failed
	// DissolvedIDs are the metas whose members no longer hold together at all.
	// The review never deletes them: the caller decides (dm facts review-metas
	// --delete-dissolved).
	DissolvedIDs []int64
}

// ReviewMetaFacts re-judges every meta-fact created since opts.Since with the
// current same-position gate: the members are checked pairwise and kept as a
// clique (each confirmed against every kept member, in id order), as the fold
// pass builds them. Members outside the clique are unfolded (they become
// active facts again; they have no stance link, so the nightly orphan routing
// places them) and the meta text is re-synthesized from the clique. A meta
// whose clique has fewer than two members is reported as dissolved and left
// untouched. A gate error skips the meta: it is never trimmed on partial
// information.
func ReviewMetaFacts(ctx context.Context, storer MetaReviewStorer, judge SamePositionJudge, synth MetaSynthesizer, opts MetaReviewOptions) (MetaReviewStats, error) {
	var st MetaReviewStats
	metas, err := storer.ListMetaFacts(ctx, opts.Since)
	if err != nil {
		return st, fmt.Errorf("listing meta-facts: %w", err)
	}
	log.Info().Int("metas", len(metas)).Time("since", opts.Since).Bool("apply", opts.Apply).Msg("Meta-fact review started")

	for _, m := range metas {
		members, err := storer.GetMetaMembers(ctx, m.ID)
		if err != nil {
			return st, fmt.Errorf("loading members of meta %d: %w", m.ID, err)
		}
		st.Metas++
		sort.Slice(members, func(a, b int) bool { return members[a].ID < members[b].ID })

		var clique, out []FactForFold
		failed := false
	members:
		for _, f := range members {
			for _, k := range clique {
				same, err := judge.IsSamePosition(ctx, m.PersonName, k.Text, f.Text)
				if err != nil {
					log.Warn().Err(err).Int64("meta_id", m.ID).Msg("Meta-fact review: gate failed; meta skipped")
					failed = true
					break members
				}
				if !same {
					out = append(out, f)
					continue members
				}
			}
			clique = append(clique, f)
		}
		if failed {
			st.GateFailures++
			continue
		}
		switch {
		case len(out) == 0:
			st.Intact++
			continue
		case len(clique) < 2:
			st.Dissolved++
			st.DissolvedIDs = append(st.DissolvedIDs, m.ID)
			log.Info().Int64("meta_id", m.ID).Str("person", m.PersonName).Int("members", len(members)).
				Str("meta_text", m.Text).Msg("Meta-fact review: DISSOLVED — its members no longer hold together")
			continue
		}
		st.Trimmed++
		for _, f := range out {
			log.Info().Int64("meta_id", m.ID).Int64("fact_id", f.ID).Str("person", m.PersonName).
				Str("fact_text", f.Text).Str("meta_text", m.Text).Bool("apply", opts.Apply).
				Msg("Meta-fact review: member no longer belongs")
		}
		if !opts.Apply {
			continue
		}
		for _, f := range out {
			if err := storer.UnfoldFactFromMeta(ctx, m.ID, f.ID); err != nil {
				return st, fmt.Errorf("unfolding fact %d from meta %d: %w", f.ID, m.ID, err)
			}
			st.Unfolded++
		}
		_, lang, _, err := storer.GetFactEmbeddingMeta(ctx, m.ID)
		if err != nil {
			return st, fmt.Errorf("loading meta %d lang: %w", m.ID, err)
		}
		texts := make([]string, len(clique))
		for i, f := range clique {
			texts[i] = f.Text
		}
		text, err := synth.SynthesizeMetaFact(ctx, m.PersonName, texts, lang)
		if err != nil {
			return st, fmt.Errorf("re-synthesizing meta %d: %w", m.ID, err)
		}
		if err := storer.UpdateMetaFactText(ctx, m.ID, text); err != nil {
			return st, fmt.Errorf("updating meta %d text: %w", m.ID, err)
		}
		if err := storer.RefreshPersonScore(ctx, m.PersonID); err != nil {
			log.Warn().Err(err).Int64("person_id", m.PersonID).Msg("Failed to refresh fact count")
		}
	}

	log.Info().Int64("metas", st.Metas).Int64("intact", st.Intact).Int64("trimmed", st.Trimmed).
		Int64("dissolved", st.Dissolved).Int64("unfolded", st.Unfolded).Int64("gate_failures", st.GateFailures).
		Msg("Meta-fact review finished")
	return st, nil
}
