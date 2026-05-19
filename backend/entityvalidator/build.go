package entityvalidator

import (
	"github.com/rs/zerolog/log"
)

// BuildFromEnv constructs the default Validator chain used by both the daily
// pipeline (pre-insert guard) and the `dm entities validate` CLI:
//
//	WikiFilter → GoogleFilter → LLMFilter
//
// Both evidence-gathering filters (Wiki, Google) ALWAYS Defer with whatever
// they collected; LLMFilter aggregates and makes the final accept/reject call.
// Optional dependencies degrade gracefully:
//
//   - SERPER_API_KEY unset    → GoogleFilter dropped, wiki + LLM only
//   - OPEN_AI_API unset       → LLMFilter dropped; the validator's
//                               DefaultDecision (Approve by default, safe for
//                               extractor) decides anything wiki/google can't
//
// Returned Validator has DefaultDecision=Approve — CLI cleanup callers can
// override to Reject for a more aggressive sweep.
func BuildFromEnv() *Validator {
	var filters []Filter
	filters = append(filters, NewWikiFilter())

	if g, err := NewGoogleFilter(); err == nil {
		filters = append(filters, g)
		log.Info().Msg("Entity validator: GoogleFilter active (Serper)")
	} else {
		log.Warn().Err(err).Msg("Entity validator: GoogleFilter unavailable (no SERPER_API_KEY)")
	}

	if l, err := NewLLMFilter(); err == nil {
		filters = append(filters, l)
		log.Info().Msg("Entity validator: LLMFilter active")
	} else {
		log.Warn().Err(err).Msg("Entity validator: LLMFilter unavailable; relying on DefaultDecision")
	}

	return New(filters...)
}
