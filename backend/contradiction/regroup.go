package contradiction

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"
)

// GroupSummarizer adapts the detector's merged-summary synthesis to the
// callback RegroupPersonContradictions expects.
func GroupSummarizer(detector *OpenAIContradictionDetector) storage.GroupSummarizer {
	return func(ctx context.Context, baseFact string, spokes []storage.GroupSpokeInput) (string, string, error) {
		ls := make([]llm.GroupSpoke, len(spokes))
		for i, sp := range spokes {
			ls[i] = llm.GroupSpoke{FactText: sp.FactText, Reason: sp.Reason}
		}
		return detector.SynthesizeGroupSummary(ctx, baseFact, ls)
	}
}

// RegroupOptions configures RunRegroup.
type RegroupOptions struct {
	// PersonIDs restricts the pass; empty = every person with contradictions.
	PersonIDs []int64
	// Summarize generates each multi-fact group's merged summary; nil sets
	// group bases only (no LLM).
	Summarize storage.GroupSummarizer
	// MaxPersons bounds the pass to the first N persons of the set above, in
	// its order (0 = all).
	MaxPersons int
}

// RegroupResult is the aggregate of a regroup pass.
type RegroupResult struct {
	// Persons is how many persons the pass covered.
	Persons int
	storage.RegroupResult
}

// RunRegroup runs RegroupPersonContradictions over each person in scope and
// returns the aggregate result. A per-person error is logged and skipped so
// one bad person never aborts a full sweep. Once the summarizer reports the
// LLM unavailable it is not called again (the remaining groups keep their
// summaries), the pass stops after that person, and the error (wrapping
// llm.ErrUnavailable) is returned. A cancelled ctx stops the pass before the
// next person, or at the person it cut, with an error wrapping ctx.Err().
func RunRegroup(ctx context.Context, storer *storage.PostgresStorer, opts RegroupOptions) (RegroupResult, error) {
	personIDs := opts.PersonIDs
	if len(personIDs) == 0 {
		var err error
		personIDs, err = storer.PersonIDsWithContradictions(ctx)
		if err != nil {
			return RegroupResult{}, fmt.Errorf("failed to list persons with contradictions: %w", err)
		}
	}
	personIDs = firstN(personIDs, opts.MaxPersons)

	summarize := opts.Summarize
	var unavailable error
	if summarize != nil {
		summarize = func(ctx context.Context, baseFact string, spokes []storage.GroupSpokeInput) (string, string, error) {
			if unavailable != nil {
				return "", "", unavailable
			}
			summary, summaryEn, err := opts.Summarize(ctx, baseFact, spokes)
			if errors.Is(err, llm.ErrUnavailable) {
				unavailable = err
			}
			return summary, summaryEn, err
		}
	}

	agg := RegroupResult{Persons: len(personIDs)}
	for _, pid := range personIDs {
		if err := ctx.Err(); err != nil {
			return agg, fmt.Errorf("regroup stopped: %w", err)
		}
		res, err := storer.RegroupPersonContradictions(ctx, pid, summarize)
		if err != nil && ctx.Err() != nil {
			return agg, fmt.Errorf("regroup stopped at person %d: %w", pid, ctx.Err())
		}
		if err != nil {
			log.Error().Err(err).Int64("person_id", pid).Msg("regroup: person failed, skipping")
			continue
		}
		agg.Rebased += res.Rebased
		agg.Groups += res.Groups
		agg.SummariesWrote += res.SummariesWrote
		agg.SummariesFailed += res.SummariesFailed
		if res.Rebased > 0 || res.Groups > 0 {
			log.Info().Int64("person_id", pid).Int("rebased", res.Rebased).
				Int("groups", res.Groups).Int("summaries", res.SummariesWrote).
				Int("summary_failures", res.SummariesFailed).Msg("regrouped contradictions")
		}
		if unavailable != nil {
			return agg, fmt.Errorf("regroup stopped at person %d: %w", pid, unavailable)
		}
	}
	return agg, nil
}

// RunRegroupStep is the daily-pipeline entry point: regroup every person with
// contradictions (the first maxPersons of them when > 0), generating merged
// summaries via the LLM. Best-effort — a nil detector (no LLM env) downgrades
// to base-only rather than failing the pipeline.
func RunRegroupStep(ctx context.Context, storer *storage.PostgresStorer, maxPersons int) error {
	var summarize storage.GroupSummarizer
	if detector, dErr := NewOpenAIContradictionDetectorFromEnv(); dErr != nil {
		log.Warn().Err(dErr).Msg("regroup: no LLM detector; setting group bases without summaries")
	} else {
		summarize = GroupSummarizer(detector)
	}

	agg, err := RunRegroup(ctx, storer, RegroupOptions{Summarize: summarize, MaxPersons: maxPersons})
	if err != nil {
		return err
	}
	if agg.Persons == 0 {
		return nil
	}
	log.Info().Int("persons", agg.Persons).Int("rebased", agg.Rebased).
		Int("groups", agg.Groups).Int("summaries", agg.SummariesWrote).
		Int("summary_failures", agg.SummariesFailed).Msg("Daily pipeline: contradiction regroup done")
	return nil
}
