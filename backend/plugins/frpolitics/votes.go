package frpolitics

import (
	"digital-mirror/backend/core"

	"github.com/rs/zerolog/log"
)

// deriveDeputyFacts builds deputy-level vote facts for each VoteEvent
func deriveDeputyFacts(law Law, events []VoteEvent, summary LawSummary, objectPhrase string) []FactRecord {
	if len(events) == 0 {
		return nil
	}
	out := make([]FactRecord, 0, len(events))
	// Use just the law topic for embedding - this ensures:
	// 1. All votes on the SAME law cluster together (FOR, AGAINST, ABSTAIN are neighbors)
	// 2. Votes on DIFFERENT laws don't cluster together
	// The action verb (voted FOR/AGAINST) is preserved in FactText for display + LLM comparison
	embeddingText := objectPhrase
	for _, ev := range events {
		text := buildVoteStatement(ev.Position, objectPhrase)
		if len(out) < 3 {
			log.Debug().Str("deputy", ev.DisplayName).Str("text", text).Msg("frpolitics: derived deputy fact")
		}
		out = append(out, FactRecord{
			PersonName:    ev.DisplayName,
			FactText:      text,
			EmbeddingText: embeddingText, // Just the law topic, no action verb
			CanonicalVerb: deriveVerbFromPosition(ev.Position),
			SourceURL:     law.URL,
			LawKey:        law.Key,
			PublishedAt:   core.PickPublishedAt(law.VoteDate),
		})
	}
	return out
}

// deriveVerbFromPosition converts a VotePosition to a canonical verb.
func deriveVerbFromPosition(pos VotePosition) string {
	switch pos {
	case VoteFor:
		return "VOTED_FOR"
	case VoteAgainst:
		return "VOTED_AGAINST"
	default:
		return ""
	}
}
