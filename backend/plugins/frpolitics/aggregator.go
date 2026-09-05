package frpolitics

import (
	"digital-mirror/backend/core"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// derivePartyMajorityFacts builds party-level facts based on majority positions
func derivePartyMajorityFacts(law Law, groupStats LawGroupStats, threshold float64, organeCache map[string]OrganeInfo, summary LawSummary, objectPhrase string) []FactRecord {
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

		factText := buildCollectiveVoteStatement(pos, objectPhrase)
		// Use just the law topic for embedding - action verb is in FactText for LLM comparison
		embeddingText := objectPhrase
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

func buildCollectiveVoteStatement(pos VotePosition, lawPhrase string) string {
	switch pos {
	case VoteFor:
		return fmt.Sprintf("a collectivement voté POUR %s", lawPhrase)
	case VoteAgainst:
		return fmt.Sprintf("a collectivement voté CONTRE %s", lawPhrase)
	case VoteAbstention:
		return fmt.Sprintf("s'est collectivement abstenu lors du vote sur %s", lawPhrase)
	default:
		return fmt.Sprintf("n'a pas pris part au vote collectif sur %s", lawPhrase)
	}
}

func buildVoteStatement(pos VotePosition, lawPhrase string) string {
	switch pos {
	case VoteFor:
		return fmt.Sprintf("a voté POUR %s", lawPhrase)
	case VoteAgainst:
		return fmt.Sprintf("a voté CONTRE %s", lawPhrase)
	case VoteAbstention:
		return fmt.Sprintf("s'est abstenu lors du vote sur %s", lawPhrase)
	default:
		return fmt.Sprintf("n'a pas pris part au vote sur %s", lawPhrase)
	}
}

func prepareLawPhrase(lawSimple string) string {
	// The summarizer's simple_statement is a full French sentence ("Crée un
	// cadre stable..."), not a noun phrase; wrapping it in guillemets after
	// "le texte de loi :" reads naturally for any statement shape. The old
	// English article logic produced "voted FOR a Creates a stable..." while
	// the fact was labelled lang=fr.
	guard := strings.TrimSpace(lawSimple)
	if guard == "" {
		return "le texte de loi"
	}
	return fmt.Sprintf("le texte de loi : « %s »", guard)
}
