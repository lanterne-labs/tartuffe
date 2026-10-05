package core

import (
	"context"
	"digital-mirror/backend/llm"
	"fmt"

	"github.com/rs/zerolog/log"
)

// FactDedupeAdjudicator asks an LLM whether two same-subject statements express
// the SAME claim (one a redundant restatement of the other) or are genuinely
// distinct. It is consulted only for the "gray zone" of embedding similarity
// (below the auto-dedup threshold but above the clearly-distinct floor), where
// cosine similarity alone cannot decide and the verb heuristic is too coarse —
// e.g. "il ne renoncera pas à sa candidature et ira jusqu'au bout" (PROMISED)
// vs "sa candidature irait jusqu'au bout" (CLAIMED): the same claim in different
// words, similarity 0.78, which slips just under the 0.80 auto-dedup line.
type FactDedupeAdjudicator struct {
	client *llm.Client
}

// NewFactDedupeAdjudicatorFromEnv builds an adjudicator from OPEN_AI_ env vars.
func NewFactDedupeAdjudicatorFromEnv() (*FactDedupeAdjudicator, error) {
	client, err := llm.NewClientFromEnv("fact-dedupe")
	if err != nil {
		return nil, err
	}
	return &FactDedupeAdjudicator{client: client}, nil
}

// IsSameClaim returns true when factB is a redundant restatement of factA by the
// same person — i.e. they should be deduplicated. Distinct claims, opposing
// stances, and different specifics return false. The subject name is passed for
// context (both facts are about the same person).
func (a *FactDedupeAdjudicator) IsSameClaim(ctx context.Context, subject, factA, factB string) (bool, error) {
	prompt := a.buildPrompt(subject, factA, factB)
	return a.askSameGate(ctx, prompt, 0.1)
}

// IsSamePosition is the meta-FOLDING gate: unlike IsSameClaim (which guards a
// destructive drop and therefore treats any differing specific as distinct),
// folding keeps every raw fact, so this gate asks the broader question — do the
// two statements assert the SAME POSITION on the SAME specific matter, differing
// only in wording, emphasis, or facet? "Affirms his innocence", "claims good
// faith", and "contests the qualification of embezzlement" about the same affair
// all fold; a commitment vs a contrary action, opposing positions, or different
// matters never do (those pairs are contradiction material and must stay apart).
//
// "Same" must hold in both orders: the gate reads the second statement
// against the first, so a pair where one says more than the other (a bundle
// and one of its claims, a broad claim and a narrower one in the same area)
// often reads as a restatement one way only. A restatement reads the same
// both ways; the second call is made only when the first says same.
func (a *FactDedupeAdjudicator) IsSamePosition(ctx context.Context, subject, factA, factB string) (bool, error) {
	same, err := a.askSameGate(ctx, a.buildSamePositionPrompt(subject, factA, factB), 0.0)
	if err != nil || !same {
		return same, err
	}
	return a.askSameGate(ctx, a.buildSamePositionPrompt(subject, factB, factA), 0.0)
}

// Both gate prompts are laid out rules first, per-call data (person, the two
// statements) next, response format last: the rule block is byte-identical
// across calls, so vLLM prefix caching prefills it once per process instead
// of once per candidate pair (see CHANGES.md, prompt layout).
func (a *FactDedupeAdjudicator) buildSamePositionPrompt(subject, factA, factB string) string {
	return fmt.Sprintf(`Two statements are recorded about the SAME person. Decide whether they assert the SAME POSITION on the SAME specific matter — so that they can be AGGREGATED under one summary statement. The originals are kept either way.

The MATTER is the specific question a statement answers — NOT its subject area. Two statements about the same area (the same election, the same war, the same adversary, the same budget) that answer DIFFERENT questions are different matters. Test: could a reader who knows A still learn a distinct demand, prediction, assessment, or figure from B? If yes, answer false.

SAME position (answer true):
- The same stance on the same question, in different words or through different facets: "affirms his innocence in the affair", "claims his good faith", "contests the qualification of embezzlement" — all one position ("denies guilt in that affair").
- The same announcement/position reported with different incidental detail (outlet, phrasing, minor date differences, added color).
- The same demand/support/opposition restated over time with the same direction and target: "demande la baisse des taxes sur les carburants" / "la France peut baisser la fiscalité sur l'essence dès maintenant".
- Several reports of ONE event or outcome (the same ruling, vote, result, or announcement, on the same occasion), even when they give different details or count its numbers differently: "condamnée en appel à un an sous bracelet électronique et 15 mois d'inéligibilité" / "condamnée par la cour d'appel à trois ans dont un an sous bracelet, et à 45 mois d'inéligibilité dont 30 avec sursis" are the same appeal ruling — but that appeal ruling and the first-instance ruling before it are two events.

DIFFERENT (answer false — never aggregate):
- OPPOSING or diverging positions, or a position vs a later change: these are contradiction material and must stay separate.
- A commitment/promise/value vs an ACTION or outcome (even one that fulfils it): a statement and a deed are different kinds of facts.
- Different questions within the same subject area:
  "annonce sa candidature à la primaire" vs "affirme qu'il gagnera la présidentielle" — a candidacy vs a prediction.
  "demande la démission du ministre" vs "juge la politique du ministre catastrophique" — a demand vs an assessment, same adversary.
  "veut sortir du nucléaire d'ici 2040" vs "demande un moratoire sur les nouveaux réacteurs" — two measures in one policy field.
  "la guerre doit cesser" vs "l'Europe est incapable de peser sur la guerre" — a wish vs a judgment about actors.
- Different matters, targets, or questions (two different affairs, laws, elections, policies) — even if thematically close.
- Different concrete events (two separate votes, two separate trials/instances, two separate polls). A vote is ONE ballot on ONE text: votes on different amendments or sub-amendments, or on an amendment vs the whole bill, are different events even on the same bill and the same day.
- A statement that bundles several claims vs one of those claims alone ("X said A and criticised B" vs "X criticised B"): the bundle asserts something the other does not — answer false.
- Materially different numbers as the substance of a statement or proposal: "une aide de 300 euros par foyer" vs "une aide de 1 000 euros par foyer" are two proposals, not one. (An event reported twice is not two events because its numbers are counted differently.)
- Different PROCEDURAL STAGES of the same affair: a prosecutor's request (réquisition) vs the court's decision, an indictment vs a verdict, a first-instance ruling vs the appeal — each is its own fact.

Person: %s
STATEMENT A: "%s"
STATEMENT B: "%s"

Return ONLY strict JSON, nothing else:
{"reasoning": "<one short sentence>", "same": true}
or
{"reasoning": "<one short sentence>", "same": false}`, subject, factA, factB)
}

// askSameGate runs a same/distinct JSON gate prompt at the given temperature
// and parses the verdict (the PairVerdict same/reasoning shape defined in
// entity_dedup.go).
func (a *FactDedupeAdjudicator) askSameGate(ctx context.Context, prompt string, temperature float64) (bool, error) {
	content, _, err := a.client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature:    temperature,
		MaxTokens:      256,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		return false, fmt.Errorf("same-gate LLM call failed: %w", err)
	}
	verdict, err := llm.ParseJSONResponse[PairVerdict](content)
	if err != nil {
		log.Error().Err(err).Str("llm_content", content).Msg("Failed to parse fact-dedupe response")
		return false, fmt.Errorf("failed to parse LLM response: %w", err)
	}
	return verdict.Same, nil
}

func (a *FactDedupeAdjudicator) buildPrompt(subject, factA, factB string) string {
	return fmt.Sprintf(`Two statements are recorded about the SAME person. Decide whether they are the SAME claim — one a redundant restatement of the other carrying NO new information — so that keeping both would be pure duplication.

This is a deduplication gate, so be CONSERVATIVE: when in doubt, answer false. Wrongly merging two statements deletes real information; keeping a redundant one is harmless. Only answer true when B is essentially A reworded.

SAME claim (answer true): the same assertion in different words — different reporting verb ("promised"/"claimed"/"declared"/"stated"/"affirmed" are interchangeable) or extra incidental wording, but the SAME subject, SAME direction, and SAME substance. e.g. "X will not give up his candidacy and will see it through" vs "X's candidacy would go all the way".

DISTINCT claims (answer false — keep both):
- The two could be presented as a CONTRADICTION or a change of position: a promise/commitment/stated value vs a later or opposing ACTION (e.g. "promised to keep the retirement age at 62" vs "abandoned the repeal of the move to 64"); a before vs after; "supports X" vs "opposes X". These are NEVER the same — merging them would erase a real contradiction.
- Different specifics: different policies, dates, people, places, numbers, or scope.
- A general position vs a specific concrete act that advances it.
- Any case where B states something A does not, or vice versa.

Person: %s
STATEMENT A: "%s"
STATEMENT B: "%s"

Return ONLY strict JSON, nothing else:
{"reasoning": "<one short sentence>", "same": true}
or
{"reasoning": "<one short sentence>", "same": false}`, subject, factA, factB)
}
