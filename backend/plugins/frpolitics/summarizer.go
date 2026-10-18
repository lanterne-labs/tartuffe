package frpolitics

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"digital-mirror/backend/llm"

	"github.com/rs/zerolog/log"
)

// summarizeBatchSize is how many laws one summarization call covers.
const summarizeBatchSize = 10

// SummarizeLaws produces the LLM summary of every law, in batches. A failed
// batch fails the call: its confidence would otherwise read as 0 and the AI
// filter would record the laws as irrelevant for good. The scrutins that
// summarize one title (the articles of one bill) get one summary: one call,
// and one wording in the facts of the bill.
func SummarizeLaws(ctx context.Context, laws []Law) (map[string]LawSummary, error) {
	result := make(map[string]LawSummary, len(laws))
	// One representative per title; the others take its summary.
	sameTitle := make(map[string][]string, len(laws)) // representative key → the keys of the others
	seen := make(map[string]string, len(laws))        // title → representative key
	var distinct []Law
	for _, law := range laws {
		title := summaryTitle(law)
		if rep, ok := seen[title]; ok {
			sameTitle[rep] = append(sameTitle[rep], law.Key)
			continue
		}
		seen[title] = law.Key
		distinct = append(distinct, law)
	}
	laws = distinct

	batchSize := summarizeBatchSize
	totalBatches := (len(laws) + batchSize - 1) / batchSize
	for i := 0; i < len(laws); i += batchSize {
		end := i + batchSize
		if end > len(laws) {
			end = len(laws)
		}
		batch := laws[i:end]
		currentBatch := (i / batchSize) + 1

		// Log progress every 10% of batches or every batch if less than 10 batches
		if totalBatches <= 10 || currentBatch%(totalBatches/10) == 0 || currentBatch == 1 || currentBatch == totalBatches {
			// Show sample keys from this batch
			sampleKeys := make([]string, 0, 3)
			for j := 0; j < len(batch) && j < 3; j++ {
				sampleKeys = append(sampleKeys, batch[j].Key)
			}
			log.Info().
				Int("batch", currentBatch).
				Int("total_batches", totalBatches).
				Int("percent", currentBatch*100/totalBatches).
				Int("laws_from", i+1).
				Int("laws_to", end).
				Strs("sample_keys", sampleKeys).
				Msg("frpolitics: law summarization progress")
		}

		summaries, err := callBatchedSummaryJSON(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("summarizing laws %d-%d: %w", i+1, end, err)
		}

		for key, summary := range summaries {
			result[key] = summary
			for _, other := range sameTitle[key] {
				result[other] = summary
			}
		}

		// Add small delay between batches to avoid rate limiting (skip on last batch)
		if currentBatch < totalBatches {
			time.Sleep(200 * time.Millisecond)
		}
	}

	return result, nil
}

func buildBatchedSummaryPrompt(laws []Law) string {
	b := &strings.Builder{}
	b.WriteString("You summarize French laws into a concise human-readable short title and a brief neutral statement with EXPLICIT directional keywords.\n")
	b.WriteString("Short titles should be recognizable (e.g. 'Réforme des retraites' instead of 'projet de loi de financement rectificative de la sécurité sociale pour 2023').\n")
	b.WriteString("Return JSON ONLY as an object where keys are the law keys provided and values are summary objects:\n")
	b.WriteString("{\n  \"LAW_KEY\": {\"short_title\": string, \"simple_statement\": string, \"confidence\": number}\n}\n\n")
	b.WriteString("You summarize French laws into:\n")
	b.WriteString("- short_title and simple_statement MUST be written in FRENCH — they are shown verbatim to French users and embedded into French fact texts.\n")
	b.WriteString("- short_title: concise human name\n")
	b.WriteString("- simple_statement: MUST include EXPLICIT DIRECTION. Use action verbs like:\n")
	b.WriteString("  * 'increases/augmente', 'decreases/diminue'\n")
	b.WriteString("  * 'strengthens/renforce', 'weakens/affaiblit'\n")
	b.WriteString("  * 'expands/étend', 'restricts/restreint'\n")
	b.WriteString("  * 'creates/crée', 'abolishes/supprime'\n")
	b.WriteString("\n")
	b.WriteString("  GOOD examples:\n")
	b.WriteString("  * 'Renforce les pouvoirs de la police contre le trafic de stupéfiants'\n")
	b.WriteString("  * 'Réduit les peines maximales pour les délits non violents'\n")
	b.WriteString("  * 'Étend la couverture sociale aux indépendants'\n")
	b.WriteString("\n")
	b.WriteString("  BAD examples (too generic - set confidence < 0.5):\n")
	b.WriteString("  * 'Ajuste le financement de la sécurité sociale' (no direction)\n")
	b.WriteString("  * 'Modifie l'exécution des peines' (no direction)\n")
	b.WriteString("  * 'Change la répartition budgétaire' (no specifics)\n")
	b.WriteString("\n")
	b.WriteString("- confidence: 0.0-1.0 where:\n")
	b.WriteString("  * 0.0-0.4: Cannot determine policy direction - SKIP\n")
	b.WriteString("  * 0.5-0.7: Direction is inferred but uncertain\n")
	b.WriteString("  * 0.8-1.0: Clear, explicit policy direction\n")
	b.WriteString("\n")
	b.WriteString("CRITICAL: If you cannot determine the DIRECTION of the policy change from the title,\n")
	b.WriteString("you MUST set confidence < 0.5. Never silently infer ambiguous content.\n")
	b.WriteString("simple_statement is embedded verbatim in the sentence 'X a voté POUR le texte de loi : « ... »'.\n")
	b.WriteString("It states only what the law does. It NEVER carries a remark about the title, the article, your\n")
	b.WriteString("certainty or what cannot be determined ('mais le contenu précis de l'article 7 n'est pas\n")
	b.WriteString("déterminable à partir du titre'): such a doubt goes in confidence, never in the text.\n\n")
	b.WriteString("Laws to summarize:\n")
	for _, law := range laws {
		b.WriteString(fmt.Sprintf("- Key: %s\n  Title: %s\n  Legislature: %d\n", law.Key, summaryTitle(law), law.Legislature))
	}
	return b.String()
}

func callBatchedSummaryJSON(ctx context.Context, laws []Law) (map[string]LawSummary, error) {
	prompt := buildBatchedSummaryPrompt(laws)

	client, err := llm.NewClientFromEnv("frpolitics-summarizer")
	if err != nil {
		return nil, err
	}
	content, _, err := client.Chat(ctx, llm.ChatRequest{
		Messages:    []llm.ChatMessage{{Role: "system", Content: prompt}},
		Temperature: 0.1,
		// Reply budget for a batch of 10 law summaries (~150 tokens each).
		// max_tokens is charged against the context window up front, and the
		// old 16384 exceeded the local model's whole 12k window, so every
		// summarization call was rejected with 400 before generation.
		MaxTokens:      4096,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskLawSummary,
	})
	if err != nil {
		return nil, err
	}

	var rawResult map[string]struct {
		ShortTitle      string  `json:"short_title"`
		SimpleStatement string  `json:"simple_statement"`
		Confidence      float64 `json:"confidence"`
	}

	if err := json.Unmarshal([]byte(content), &rawResult); err != nil {
		return nil, fmt.Errorf("parse summary json: %w", err)
	}

	summaries := make(map[string]LawSummary)
	for key, out := range rawResult {
		confidence := out.Confidence
		if metaStatement(out.SimpleStatement) {
			// The statement is embedded in every ballot of the law: a remark
			// about what the model could not determine must not be. The law
			// is skipped as a low-confidence one.
			log.Warn().Str("law_key", key).Str("statement", out.SimpleStatement).
				Msg("frpolitics: the law statement carries a remark on the title; skipping the law")
			confidence = 0
		}
		summaries[key] = LawSummary{
			ShortTitle:      out.ShortTitle,
			SimpleStatement: out.SimpleStatement,
			Confidence:      confidence,
		}
	}

	return summaries, nil
}

// metaStatementRe matches a statement that talks about the summarization
// (what the title does or does not say) instead of the law.
var metaStatementRe = regexp.MustCompile(`(?i)d[ée]terminable|à partir du titre|(im)?possible de d[ée]terminer|le titre ne (permet|précise|dit)|contenu (précis|exact) (de l'article|du texte|de l'amendement) n'est pas`)

// metaStatement reports whether statement talks about the title or the
// summarization instead of the law.
func metaStatement(statement string) bool {
	return metaStatementRe.MatchString(statement)
}
