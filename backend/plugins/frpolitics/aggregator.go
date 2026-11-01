package frpolitics

import (
	"digital-mirror/backend/core"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// derivePartyMajorityFacts builds party-level facts based on majority positions
// The participation floor of a group's collective vote: at least
// groupVoteMinVoters members voting, and at least groupVoteMinShare of the
// group's members (nombreMembresGroupe; nonVotants are the members present
// who did not vote, usually 0, so they cannot stand for the absent: the
// review of 2026-11-01 found 7 of 69 PS deputies making a "collective" vote).
const (
	groupVoteMinVoters = 3
	groupVoteMinShare  = 0.2
)

func derivePartyMajorityFacts(law Law, groupStats LawGroupStats, threshold float64, organeCache map[string]OrganeInfo, summary LawSummary, objectPhrase string) []FactRecord {
	facts := make([]FactRecord, 0, len(groupStats))
	for orgRef, stats := range groupStats {
		total := stats.For + stats.Against + stats.Abst
		// A collective position needs the group to have voted: no voters,
		// or fewer than groupVoteMinVoters and groupVoteMinShare of the
		// members (2 of 23 LIOT deputies made a "collective" vote,
		// 2026-11-01), gives no fact.
		members := stats.Members
		if members < total+stats.NonVot {
			members = total + stats.NonVot
		}
		if total == 0 || total < groupVoteMinVoters || float64(total) < groupVoteMinShare*float64(members) {
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
		// The non-attached deputies are not a group: their votes are each
		// their own, never a collective position.
		if isNonInscrits(name) {
			continue
		}

		factText := name + " " + buildCollectiveVoteStatement(pos, objectPhrase)
		// Use just the law topic for embedding - action verb is in FactText for LLM comparison
		embeddingText := objectPhrase
		log.Debug().Str("party", name).Float64("frac", majorityFrac).Str("text", factText).Msg("frpolitics: derived collective party fact")
		facts = append(facts, FactRecord{
			PersonName:    name,
			EntityType:    "party",
			FactText:      factText,
			EmbeddingText: embeddingText, // Just the law topic, no action verb
			CanonicalVerb: deriveCollectiveVerb(pos),
			SourceURL:     law.URL,
			LawKey:        law.Key,
			SeanceRef:     law.SeanceRef,
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
		return fmt.Sprintf("a collectivement choisi l'abstention lors du vote sur %s", lawPhrase)
	default:
		return fmt.Sprintf("n'a pas pris part au vote collectif sur %s", lawPhrase)
	}
}

// buildVoteStatement is the vote of one deputy. The abstention is worded
// without a past participle: the AN records give no civility, and "s'est
// abstenu" was wrong for every woman deputy (audit of 2026-10-31).
func buildVoteStatement(pos VotePosition, lawPhrase string) string {
	switch pos {
	case VoteFor:
		return fmt.Sprintf("a voté POUR %s", lawPhrase)
	case VoteAgainst:
		return fmt.Sprintf("a voté CONTRE %s", lawPhrase)
	case VoteAbstention:
		return fmt.Sprintf("a choisi l'abstention lors du vote sur %s", lawPhrase)
	default:
		return fmt.Sprintf("n'a pas pris part au vote sur %s", lawPhrase)
	}
}

// textNoun names the kind of text a scrutin title votes on: a proposition
// de résolution or a résolution européenne is not a law, and calling it "le
// texte de loi" misstates the vote (2 of 100 audited vote facts, 2026-10-31).
func textNoun(title string) string {
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "proposition de résolution"), strings.Contains(t, "proposition de resolution"):
		return "la proposition de résolution"
	case strings.Contains(t, "projet de loi organique"), strings.Contains(t, "proposition de loi organique"):
		return "le texte de loi organique"
	case strings.Contains(t, "déclaration du gouvernement"), strings.Contains(t, "declaration du gouvernement"):
		// A vote on a declaration of the Government (article 50-1 of the
		// Constitution): not a law.
		return "la déclaration du Gouvernement"
	default:
		return "le texte de loi"
	}
}

// lawPhraseFor is prepareLawPhrase with the noun of the scrutin's text.
func lawPhraseFor(title, lawSimple string) string {
	phrase := prepareLawPhrase(lawSimple)
	noun := textNoun(title)
	if noun == "la déclaration du Gouvernement" {
		// The summary of a declaration repeats the noun ("Approuve la
		// déclaration du Gouvernement visant à …"): the phrase is the
		// declaration itself, not a quote of it.
		if rest, ok := cutPrefixFold(strings.TrimSpace(lawSimple), "approuve la déclaration du gouvernement "); ok {
			return noun + " " + rest
		}
	}
	if noun != "le texte de loi" {
		phrase = noun + strings.TrimPrefix(phrase, "le texte de loi")
	}
	return phrase
}

// cutPrefixFold is strings.CutPrefix without regard to case.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
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

// isNonInscrits reports whether an organe name is the non-attached deputies
// ("Non inscrit", "Non inscrits", "NI").
func isNonInscrits(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "ni" || strings.HasPrefix(n, "non inscrit") || strings.HasPrefix(n, "non-inscrit")
}
