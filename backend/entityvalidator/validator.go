package entityvalidator

import (
	"context"
	"strings"

	"github.com/rs/zerolog/log"
)

// Validator runs a candidate (name + entity type) through an ordered chain of
// Filters and returns a Verdict. Filters are tried in order; the first one to
// return Approve or Reject wins. If every filter Defers (or errors out), the
// Validator falls back to DefaultDecision.
//
// Each filter sees the accumulated Previous results via its Candidate input,
// so a downstream filter (LLMFilter) can read what an upstream filter (WikiFilter)
// found — even when that upstream filter chose to Defer.
//
// The Validator is intentionally a thin coordinator — all classification logic
// lives in the Filters so we can swap in new heuristics, new lookup sources,
// or model variants by appending to the chain.
type Validator struct {
	filters []Filter
	// DefaultDecision is what we return when every filter Defers. Use Approve
	// when the cost of dropping a real entity is high (e.g. during extraction).
	// Use Reject when running a destructive cleanup and you'd rather lose
	// ambiguous things than keep junk.
	DefaultDecision Decision
}

// New returns a Validator with the supplied filters, in order. The default
// decision is Approve — safer for the extractor path. CLI cleanup callers can
// override by setting DefaultDecision after construction.
func New(filters ...Filter) *Validator {
	return &Validator{
		filters:         filters,
		DefaultDecision: Approve,
	}
}

// Validate runs `name` and `entityType` through every filter in order. Filter
// errors are logged but do NOT abort the pipeline — they're recorded in the
// FilterRun history (so downstream filters can adapt) and treated as Defer so
// a transient external-API failure can't reject otherwise-real entities. The
// same robustness extends to the call site: callers iterating many candidates
// (a fact batch, the whole persons table) should keep going on Validate-level
// failures by catching the typed error returned from the wiring layer.
func (v *Validator) Validate(ctx context.Context, name, entityType string) Verdict {
	cand := Candidate{
		Name:       strings.TrimSpace(name),
		EntityType: strings.ToLower(strings.TrimSpace(entityType)),
		Previous:   make([]FilterRun, 0, len(v.filters)),
	}

	for _, f := range v.filters {
		res, err := f.Check(ctx, cand)
		cand.Previous = append(cand.Previous, FilterRun{
			Filter: f.Name(),
			Result: res,
			Err:    err,
		})
		if err != nil {
			log.Warn().
				Err(err).
				Str("filter", f.Name()).
				Str("name", cand.Name).
				Str("entity_type", cand.EntityType).
				Msg("Entity validator filter errored; treating as Defer")
			continue
		}
		if res.Decision == Defer {
			continue
		}
		return Verdict{
			Real:    res.Decision == Approve,
			Filter:  f.Name(),
			Reason:  res.Reason,
			History: cand.Previous,
		}
	}

	return Verdict{
		Real:    v.DefaultDecision == Approve,
		Filter:  "default",
		Reason:  "no filter produced a non-Defer decision",
		History: cand.Previous,
	}
}
