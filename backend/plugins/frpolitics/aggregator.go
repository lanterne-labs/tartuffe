package frpolitics

import (
	"digital-mirror/backend/core"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// derivePartyMajorityFacts builds party-level facts based on majority positions
func derivePartyMajorityFacts(law Law, groupStats LawGroupStats, threshold float64, organeCache map[string]OrganeInfo, summary LawSummary) []FactRecord {
	facts := make([]FactRecord, 0, len(groupStats))
	for orgRef, stats := range groupStats {
		total := stats.For + stats.Against + stats.Abst
		// Avoid division by zero; skip if no present voters
		if total == 0 {
			continue
		}
		var majorityFrac float64
		var pos VotePosition
		switch stats.Position {
		case VoteFor:
			majorityFrac = float64(stats.For) / float64(total)
			pos = VoteFor
		case VoteAgainst:
			majorityFrac = float64(stats.Against) / float64(total)
			pos = VoteAgainst
		case VoteAbstention:
			majorityFrac = float64(stats.Abst) / float64(total)
			pos = VoteAbstention
		default:
			// If non votants majority or unknown, skip
			continue
		}
		if majorityFrac < threshold {
			continue
		}

		// Resolve party name strictly; drop if no human-readable name
		name := ""
		if info, ok := organeCache[orgRef]; ok {
			if strings.TrimSpace(info.LongName) != "" {
				name = info.LongName
			} else if strings.TrimSpace(info.ShortName) != "" {
				name = info.ShortName
			}
		}
		if strings.TrimSpace(name) == "" {
			// Strict mode: skip when we cannot resolve a proper name
			continue
		}

		factText := buildCollectiveVoteStatement(pos, summary.SimpleStatement)
		// Use just the law topic for embedding - action verb is in FactText for LLM comparison
		embeddingText := prepareLawPhrase(summary.SimpleStatement)
		log.Debug().Str("party", name).Float64("frac", majorityFrac).Str("text", factText).Msg("frpolitics: derived collective party fact")
		facts = append(facts, FactRecord{
			PersonName:    name,
			FactText:      factText,
			EmbeddingText: embeddingText, // Just the law topic, no action verb
			CanonicalVerb: deriveCollectiveVerb(pos),
			SourceURL:     law.URL,
			LawKey:        law.Key,
			PublishedAt:   core.PickPublishedAt(law.VoteDate),
		})
	}
	return facts
}

// deriveCollectiveVerb converts a VotePosition to a canonical verb for collective votes.
func deriveCollectiveVerb(pos VotePosition) string {
	switch pos {
	case VoteFor:
		return "VOTED_FOR"
	case VoteAgainst:
		return "VOTED_AGAINST"
	default:
		return ""
	}
}

func buildCollectiveVoteStatement(pos VotePosition, lawSimple string) string {
	lawPhrase := prepareLawPhrase(lawSimple)
	switch pos {
	case VoteFor:
		return fmt.Sprintf("collectively voted FOR %s", lawPhrase)
	case VoteAgainst:
		return fmt.Sprintf("collectively voted AGAINST %s", lawPhrase)
	case VoteAbstention:
		return fmt.Sprintf("collectively ABSTAINED on %s", lawPhrase)
	default:
		return fmt.Sprintf("did not collectively vote on %s", lawPhrase)
	}
}

func buildVoteStatement(pos VotePosition, lawSimple string) string {
	lawPhrase := prepareLawPhrase(lawSimple)
	switch pos {
	case VoteFor:
		return fmt.Sprintf("voted FOR %s", lawPhrase)
	case VoteAgainst:
		return fmt.Sprintf("voted AGAINST %s", lawPhrase)
	case VoteAbstention:
		return fmt.Sprintf("ABSTAINED on %s", lawPhrase)
	default:
		return fmt.Sprintf("did not vote on %s", lawPhrase)
	}
}

func prepareLawPhrase(lawSimple string) string {
	guard := strings.TrimSpace(lawSimple)
	if guard == "" {
		guard = "the law"
	}
	// ensure article "a" before noun if not already starts with article
	lower := strings.ToLower(guard)
	if !(strings.HasPrefix(lower, "a ") || strings.HasPrefix(lower, "an ") || strings.HasPrefix(lower, "the ")) {
		return fmt.Sprintf("a %s", guard)
	}
	return guard
}
