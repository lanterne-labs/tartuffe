// Throwaway script: validate that the LLM extraction produces the new
// bilingual fields (lang, normalized_fact_en) on a French sample.
// Run: OPEN_AI_API='http://127.0.0.1:8080/v1' OPEN_AI_CHAT_MODEL='google/gemma-4-26b-a4b-it' OPEN_AI_KEY=any go run scripts/test_multilingual_extraction.go
// Delete after one-time use.

//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load(".env")
	if os.Getenv("OPEN_AI_API") == "" || os.Getenv("OPEN_AI_KEY") == "" {
		fmt.Fprintln(os.Stderr, "OPEN_AI_API + OPEN_AI_KEY required")
		os.Exit(2)
	}

	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(2)
	}

	target := os.Getenv("TEST_TARGET")
	if target == "" {
		target = "Marine Le Pen"
	}
	var content extractor.ExtractedContent
	if os.Getenv("TEST_LANG") == "en" {
		// English test snippet (Trump). Expect lang="en" and normalized_fact_en="" (omitted).
		content = extractor.ExtractedContent{
			URL:   "https://example.com/test/article-trump",
			Title: "Trump on NATO and the Iran war",
			Content: `Speaking at a rally in Pennsylvania on May 14, 2026, Donald Trump made several pointed declarations.

"If Iran does not reopen the Strait of Hormuz by 8 p.m. tomorrow, we will strike their power plants. I will not hesitate," Trump told the crowd of 12,000.

On NATO, Trump repeated his position: "I have always said NATO must pay their fair share. I will demand 5% of GDP from every member, or the United States walks away."

Trump also threatened to reduce U.S. forces in Italy and Spain if those countries did not increase support: "These countries owe us. They will pay, or we leave."

Asked about a possible third term in office, Trump dismissed the idea: "I am focused on my current term. The Constitution is the Constitution."`,
		}
	} else {
		// French test snippet (Le Pen). Expect lang="fr" and a real normalized_fact_en.
		content = extractor.ExtractedContent{
			URL:   "https://example.fr/test/article-multilingual",
			Title: "Marine Le Pen sur la dissuasion nucléaire et l'OTAN",
			Content: `La présidente du Rassemblement national, Marine Le Pen, a déclaré ce lundi 12 mai 2026 lors d'un meeting à Paris :
"Nous devons sortir du commandement intégré de l'OTAN. Cette intégration nous prive de notre souveraineté militaire. Si je suis élue présidente en 2027, je le ferai dès la première semaine."

Sur la dissuasion nucléaire, elle a ajouté : "La France doit conserver sa dissuasion nucléaire de manière strictement nationale. Aucun partage avec d'autres puissances européennes, sous quelque forme que ce soit."

Marine Le Pen a également promis d'augmenter le budget de la défense à 3% du PIB d'ici 2030. "Nos forces armées sont en sous-financement chronique depuis trop longtemps", a-t-elle insisté.

Concernant la guerre en Ukraine, la présidente du RN a déclaré qu'elle ne soutiendrait plus l'envoi d'armes à Kiev : "Nous avons assez donné. Il est temps de privilégier la diplomatie." Elle a précisé qu'un éventuel accord de paix devrait inclure une "neutralité de l'Ukraine vis-à-vis de l'OTAN".`,
		}
	}

	ctx := context.Background()
	facts, err := ext.ExtractFactsWithType(ctx, target, "person", content)
	if err != nil {
		fmt.Fprintln(os.Stderr, "extraction:", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== Extracted %d fact(s) ===\n\n", len(facts))
	allGood := true
	for i, f := range facts {
		out := map[string]any{
			"#":                   i + 1,
			"subject_entity":      f.SubjectEntity,
			"lang":                f.Lang,
			"normalized_fact":     f.NormalizedFactText,
			"normalized_fact_en":  f.NormalizedFactTextEn,
			"canonical_verb":      f.CanonicalVerb,
			"embedding_text":      f.EmbeddingText,
			"original_quote":      f.OriginalQuote,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		fmt.Println()

		// Validate
		if f.Lang == "" {
			fmt.Printf("  ❌ FAIL: lang is empty\n")
			allGood = false
		}
		if f.Lang == "fr" && f.NormalizedFactTextEn == "" {
			fmt.Printf("  ⚠️  WARN: lang=fr but no English translation\n")
		}
		if f.Lang == "en" && f.NormalizedFactTextEn != "" {
			fmt.Printf("  ❌ FAIL: lang=en but English translation provided (should be omitted)\n")
			allGood = false
		}
	}

	if !allGood {
		fmt.Println("\n❌ Some validations failed")
		os.Exit(1)
	}
	if len(facts) == 0 {
		fmt.Println("\n❌ Zero facts extracted")
		os.Exit(1)
	}
	fmt.Printf("\n✅ Extraction OK — %d facts have lang set and bilingual rules respected\n", len(facts))

	// Now go all the way: embed, insert, read back from DB.
	fmt.Println("\n=== DB round-trip ===")
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "storage init:", err)
		os.Exit(1)
	}
	defer storer.Close()
	emb, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedder init:", err)
		os.Exit(1)
	}
	stancer := storage.NewStanceStorerAdapter(storer)
	sm, err := core.NewStanceManagerFromEnv(stancer)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stance init:", err)
		os.Exit(1)
	}
	inserter := core.NewFactInserter(storer, sm)

	for _, f := range facts {
		vecs, err := emb.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{f.EmbeddingText})
		if err != nil {
			fmt.Fprintln(os.Stderr, "embed:", err)
			continue
		}
		input := core.StoreFactInput{
			SubjectEntity:        f.SubjectEntity,
			SubjectEntityType:    f.SubjectEntityType,
			NormalizedFactText:   f.NormalizedFactText,
			NormalizedFactTextEn: f.NormalizedFactTextEn,
			Lang:                 f.Lang,
			EmbeddingText:        f.EmbeddingText,
			Embedding:            vecs[0],
			OriginalQuote:        f.OriginalQuote,
			CanonicalVerb:        f.CanonicalVerb,
			InvolvedPeople:       f.InvolvedPeople,
			SourceURL:            content.URL,
			SourceType:           "test-multilingual",
		}
		res, err := inserter.InsertFacts(ctx, []core.StoreFactInput{input})
		if err != nil {
			fmt.Fprintln(os.Stderr, "insert:", err)
			continue
		}
		fmt.Printf("  inserted=%d skipped=%d\n", res.Inserted, res.Skipped)
	}

	// Read back what's in the facts table for this source URL.
	dburl := os.Getenv("DATABASE_URL")
	cfg, _ := pgxpool.ParseConfig(dburl)
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)
		os.Exit(1)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		SELECT f.id, f.lang, f.fact_text, COALESCE(f.fact_text_en, '')
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		WHERE fs.source_url = $1
		ORDER BY f.id DESC LIMIT 10`, content.URL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "select:", err)
		os.Exit(1)
	}
	defer rows.Close()
	fmt.Println("\nRows in DB:")
	count := 0
	for rows.Next() {
		var id int64
		var lang, ft, fte string
		if err := rows.Scan(&id, &lang, &ft, &fte); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			continue
		}
		count++
		fmt.Printf("  id=%d lang=%q fact_text=%q fact_text_en=%q\n", id, lang, truncate(ft, 80), truncate(fte, 80))
	}
	if count == 0 {
		fmt.Println("  (no rows — insertion may have failed or deduped)")
		os.Exit(1)
	}
	fmt.Println("\n✅ DB round-trip OK")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
