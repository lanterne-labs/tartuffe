package frpolitics

import "github.com/rs/zerolog/log"

// deriveDeputyFacts builds deputy-level vote facts for each VoteEvent
func deriveDeputyFacts(law Law, events []VoteEvent, summary LawSummary) []FactRecord {
	if len(events) == 0 {
		return nil
	}
	out := make([]FactRecord, 0, len(events))
	// Use just the law topic for embedding - this ensures:
	// 1. All votes on the SAME law cluster together (FOR, AGAINST, ABSTAIN are neighbors)
	// 2. Votes on DIFFERENT laws don't cluster together
	// The action verb (voted FOR/AGAINST) is preserved in FactText for display + LLM comparison
	embeddingText := prepareLawPhrase(summary.SimpleStatement)
	for _, ev := range events {
		text := buildVoteStatement(ev.Position, summary.SimpleStatement)
		if len(out) < 3 {
			log.Debug().Str("deputy", ev.DisplayName).Str("text", text).Msg("frpolitics: derived deputy fact")
		}
		out = append(out, FactRecord{
			PersonName:    ev.DisplayName,
			FactText:      text,
			EmbeddingText: embeddingText, // Just the law topic, no action verb
			Topics:        summary.Topics,
			SourceURL:     law.URL,
			LawKey:        law.Key,
			PublishedAt:   law.VoteDate,
		})
	}
	return out
}

// derivePartyFactsFromEvents constructs party facts by counting nominative events
// If a super-majority of a party's deputies voted the same way, we assert a party fact.
func derivePartyFactsFromEvents(law Law, events []VoteEvent, threshold float64, organeCache map[string]OrganeInfo, summary LawSummary) []FactRecord {
	if len(events) == 0 {
		return nil
	}
	// Count per organeRef
	type counts struct{ forN, againstN, abstN, nonN int }
	byOrg := map[string]*counts{}
	for _, ev := range events {
		c := byOrg[ev.OrganeRef]
		if c == nil {
			c = &counts{}
			byOrg[ev.OrganeRef] = c
		}
		switch ev.Position {
		case VoteFor:
			c.forN++
		case VoteAgainst:
			c.againstN++
		case VoteAbstention:
			c.abstN++
		default:
			c.nonN++
		}
	}
	var facts []FactRecord
	for orgRef, c := range byOrg {
		total := c.forN + c.againstN + c.abstN
		if total == 0 {
			continue
		}
		// Determine majority and fraction
		maj := VoteFor
		majCount := c.forN
		if c.againstN > majCount {
			maj = VoteAgainst
			majCount = c.againstN
		}
		if c.abstN > majCount {
			maj = VoteAbstention
			majCount = c.abstN
		}
		frac := float64(majCount) / float64(total)
		if frac < threshold {
			continue
		}
		name := orgRef
		if info, ok := organeCache[orgRef]; ok {
			if info.LongName != "" {
				name = info.LongName
			} else if info.ShortName != "" {
				name = info.ShortName
			}
		}
		factText := buildVoteStatement(maj, summary.SimpleStatement)
		// Use just the law topic for embedding - action verb is in FactText for LLM comparison
		embeddingText := prepareLawPhrase(summary.SimpleStatement)
		facts = append(facts, FactRecord{
			PersonName:    name,
			FactText:      factText,
			EmbeddingText: embeddingText, // Just the law topic, no action verb
			Topics:        summary.Topics,
			SourceURL:     law.URL,
			LawKey:        law.Key,
			PublishedAt:   law.VoteDate,
		})
	}
	return facts
}
