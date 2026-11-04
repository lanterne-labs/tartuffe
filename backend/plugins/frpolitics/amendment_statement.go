package frpolitics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"digital-mirror/backend/llm"

	"github.com/rs/zerolog/log"
)

// An amendment ballot must say what the amendment does. The exposé sommaire
// often opens with context, and its first 200 characters were the object:
// « Le versement des allocations familiales a pour objectif de consolider la
// communauté nationale… » for the RN amendment that reserves them to French
// families, so a vote CONTRE read as a vote against family allowances (audit
// of 2026-11-04: 10 % of the sampled ballots, 9 of 10 amendments). The model
// states the amendment from its dispositif and exposé, as for an article.

// amendmentStatementMinConfidence is the confidence under which the ballot
// keeps the quoted exposé (purposePhrase) instead of the statement.
const amendmentStatementMinConfidence = 0.5

// amendmentTextMaxChars bounds each of the dispositif and the exposé the
// model reads.
const amendmentTextMaxChars = 4000

// amendmentStatements gives an amendment's purpose phrase from its statement,
// cached in vote_amendments. A nil *amendmentStatements gives the quoted
// exposé (purposePhrase), with no LLM call.
type amendmentStatements struct {
	store     *Store
	statement func(ctx context.Context, title string, info *AmendementInfo) (LawSummary, error)
}

func newAmendmentStatements(store *Store) *amendmentStatements {
	return &amendmentStatements{store: store, statement: amendmentStatement}
}

// purpose returns the phrase that follows "l'amendement n° N (groupe)" in a
// ballot: (« statement ») when the model states the amendment with
// confidence, else the quoted exposé. stated tells which.
func (a *amendmentStatements) purpose(ctx context.Context, law Law, info *AmendementInfo) (phrase string, stated bool, err error) {
	if a == nil || info.TexteRef == "" {
		return purposePhrase(info), false, nil
	}
	summary, found, err := a.store.StoredAmendmentStatement(ctx, law.Legislature, info.TexteRef, info.Numero)
	if err != nil {
		return "", false, err
	}
	if !found {
		if strings.TrimSpace(info.Dispositif) == "" && strings.TrimSpace(info.Expose) == "" {
			return "", false, nil
		}
		summary, err = a.statement(ctx, law.Title, info)
		if err != nil {
			return "", false, fmt.Errorf("statement of amendment %d of %s: %w", info.Numero, info.TexteRef, err)
		}
		if err := a.store.StoreAmendmentStatement(ctx, law.Legislature, info.TexteRef, info.Numero, summary); err != nil {
			return "", false, err
		}
	}
	if summary.Confidence < amendmentStatementMinConfidence {
		log.Warn().Str("law_key", law.Key).Int("numero", info.Numero).Float64("confidence", summary.Confidence).
			Msg("frpolitics: no confident statement of the amendment; the ballot quotes its exposé")
		return purposePhrase(info), false, nil
	}
	return fmt.Sprintf("(« %s »)", strings.TrimSuffix(summary.SimpleStatement, ".")), true, nil
}

// amendmentStatement asks the model what an amendment does: one French
// sentence with the direction of the change, and a confidence. The reasoning
// comes first and the decision last.
func amendmentStatement(ctx context.Context, title string, info *AmendementInfo) (LawSummary, error) {
	prompt := fmt.Sprintf(`You summarize ONE AMENDMENT to a French bill into a brief neutral statement, in FRENCH, with an EXPLICIT direction (crée, supprime, étend, restreint, réserve, renforce, allège, autorise, interdit, fixe, rétablit...). The statement is embedded verbatim in the sentence "X a voté CONTRE l'amendement n° N (« ... »), déposé sur le texte de loi : ...".

Rules:
- Say what THIS AMENDMENT DOES to the text, read from the dispositif (the change itself); the exposé sommaire tells why and helps read a dispositif that only renumbers or substitutes words. Never the author's justification or context ("Le versement des allocations familiales a pour objectif de...") in place of the change ("Réserve les allocations familiales aux familles de nationalité française").
- One sentence, 25 words at most, no subject. Name what an article or a reference it cuts or restores provides when the texts say it ("Rétablit l'article 3, qui ...", not "Rétablit l'article 3").
- The statement NEVER carries a remark about the text or your certainty. A doubt goes in the confidence.
- confidence: 0.8-1.0 when the change is explicit; 0.5-0.7 when inferred from the exposé; below 0.5 when neither text tells what the amendment changes.

Return ONLY strict JSON, with the keys in this order:
{"reasoning": "<one sentence: what the amendment changes and where>", "statement": "<the French statement>", "confidence": <number>}

Scrutin: %s
Dispositif:
%s
Exposé sommaire:
%s`, title, capRunes(info.Dispositif, amendmentTextMaxChars), capRunes(info.Expose, amendmentTextMaxChars))

	client, err := llm.NewClientFromEnv("frpolitics-amendment")
	if err != nil {
		return LawSummary{}, err
	}
	content, _, err := client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.1,
		MaxTokens:      400,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskLawSummary,
	})
	if err != nil {
		return LawSummary{}, err
	}
	return parseAmendmentStatement(content)
}

func parseAmendmentStatement(content string) (LawSummary, error) {
	var reply struct {
		Reasoning  string  `json:"reasoning"`
		Statement  string  `json:"statement"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(content), &reply); err != nil {
		return LawSummary{}, fmt.Errorf("parse amendment statement: %w", err)
	}
	if strings.TrimSpace(reply.Statement) == "" {
		return LawSummary{}, errors.New("amendment statement: empty statement")
	}
	summary := LawSummary{SimpleStatement: strings.TrimSpace(reply.Statement), Confidence: reply.Confidence}
	if metaStatement(summary.SimpleStatement) {
		summary.Confidence = 0
	}
	return summary, nil
}
