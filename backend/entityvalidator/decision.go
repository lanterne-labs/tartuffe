package entityvalidator

import (
	"context"
	"encoding/json"
)

// Decision is what a single Filter returns for one candidate.
//
//   - Approve: definitely a real entity, stop the pipeline and keep it
//   - Reject:  definitely junk (meta-group, composite, fictional, …), stop and drop it
//   - Defer:   "I can't tell", pass to the next filter
type Decision int

const (
	Defer Decision = iota
	Approve
	Reject
)

func (d Decision) String() string {
	switch d {
	case Approve:
		return "approve"
	case Reject:
		return "reject"
	default:
		return "defer"
	}
}

// FilterResult is what a Filter emits for one candidate.
//
// Reason is a short human-readable explanation surfaced in CLI reports and logs.
//
// Extra (optional) is a structured payload a Filter wants to hand forward —
// e.g. WikiFilter stashes the matched page titles + intro extracts so LLMFilter
// can read them via Candidate.PrevByName("wiki").Result.Extra without re-doing
// the lookup. We use json.RawMessage so each Filter defines its own typed
// schema without the package needing to know about it.
type FilterResult struct {
	Decision Decision
	Reason   string
	Extra    json.RawMessage
}

// FilterRun records a single filter's output for one candidate. The Validator
// builds these up as it iterates the chain and exposes them via Candidate.Previous
// so that later filters can read what earlier ones said — even when those earlier
// filters Deferred (e.g. "WikiFilter found 2 pages whose extracts are attached",
// so the LLMFilter can read them and judge).
type FilterRun struct {
	Filter string
	Result FilterResult
	Err    error
}

// Candidate is the input to a Filter — the entity under examination plus the
// history of what previous filters said about it.
type Candidate struct {
	Name       string
	EntityType string      // human / party / organization
	Previous   []FilterRun // results from previously-run filters in order
}

// PrevByName returns the most recent run for the named filter, or (zero, false)
// if that filter hasn't run yet. Used by downstream filters that want to consult
// a specific predecessor's verdict (e.g. LLMFilter looking up WikiFilter's snippets).
func (c Candidate) PrevByName(name string) (FilterRun, bool) {
	for i := len(c.Previous) - 1; i >= 0; i-- {
		if c.Previous[i].Filter == name {
			return c.Previous[i], true
		}
	}
	return FilterRun{}, false
}

// Filter is one step in the validation pipeline. Filters are expected to be
// cheap-first / expensive-last so we can short-circuit on obvious cases before
// hitting an external API or the LLM.
//
// A Filter MUST NOT panic on bad input — return Defer with a Reason instead, so
// later filters get a chance. Returning an error is reserved for hard failures
// (e.g. LLM completely unreachable) where the caller may want to treat the
// candidate as Defer-by-default.
type Filter interface {
	Name() string
	Check(ctx context.Context, c Candidate) (FilterResult, error)
}

// Verdict is the final pipeline output for one candidate.
type Verdict struct {
	Real    bool        // false ⇒ the entity should be dropped / not inserted
	Filter  string      // which filter produced the decision ("default" if all deferred)
	Reason  string      // human-readable detail
	History []FilterRun // full chain of filter runs (useful for logging / debugging)
}
