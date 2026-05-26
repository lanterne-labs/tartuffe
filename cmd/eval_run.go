package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/eval"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	evalRunCasesPath string
	evalRunKeepData  bool
	evalRunCheckAll  bool
)

var evalRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Ingest a synthetic corpus, run detection, print a precision/recall report.",
	Long: `Steps:
  1. Load the YAML corpus and validate it.
  2. For each person in the corpus: delete prior facts + contradictions (unless --keep-data).
  3. Insert every fact through FactInserter (the same path used by the daily pipeline).
     Each insert embeds the fact and routes it through the stance manager.
  4. Run contradiction detection per test person (--check-all by default,
     so we exercise the LLM prompt on EVERY pair regardless of KNN distance —
     the eval is about prompt quality, not KNN recall).
  5. Pull contradictions from the DB and compare to corpus.expected_pairs.

The corpus is fictional. Run against a local Supabase, never production.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runEvalRun,
}

func init() {
	evalCmd.AddCommand(evalRunCmd)
	evalRunCmd.Flags().StringVar(&evalRunCasesPath, "cases", "eval/cases.yaml", "Path to the eval YAML file")
	evalRunCmd.Flags().BoolVar(&evalRunKeepData, "keep-data", false, "Skip the per-person delete that happens at the start of every run")
	evalRunCmd.Flags().BoolVar(&evalRunCheckAll, "check-all", true, "Force the detector to check every fact pair (bypasses the KNN similarity filter)")
}

func runEvalRun(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env not found; relying on environment")
	}

	corpus, err := eval.LoadCorpus(evalRunCasesPath)
	if err != nil {
		return err
	}
	log.Info().
		Int("persons", len(corpus.Persons)).
		Int("facts", len(corpus.AllFacts())).
		Int("expected_pairs", len(corpus.ExpectedPairs)).
		Str("cases_file", evalRunCasesPath).
		Msg("Eval corpus loaded")

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer storer.Close()

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("init embedder: %w", err)
	}

	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return fmt.Errorf("init stance manager: %w", err)
	}
	inserter := core.NewFactInserter(storer, stanceMgr)

	if !evalRunKeepData {
		if err := resetEvalPersons(ctx, storer, corpus); err != nil {
			return fmt.Errorf("reset persons: %w", err)
		}
	}

	idMap, err := ingestCorpus(ctx, embedder, inserter, corpus)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	log.Info().Int("inserted", len(idMap)).Msg("Corpus ingest complete")

	for _, p := range corpus.Persons {
		opts := ContradictionDetectOpts{
			Person:   p.Name,
			CheckAll: evalRunCheckAll,
		}
		if err := executeContradictionDetection(ctx, storer, opts); err != nil {
			log.Warn().Err(err).Str("person", p.Name).Msg("Detection step failed; continuing")
		}
	}

	detected, err := collectContradictions(ctx, storer, corpus)
	if err != nil {
		return fmt.Errorf("collect contradictions: %w", err)
	}
	log.Info().Int("contradictions", len(detected)).Msg("Detection pull complete")

	report := eval.Evaluate(corpus, idMap, detected)
	fmt.Println()
	fmt.Println(eval.FormatReport(report))
	return nil
}

// resetEvalPersons deletes prior facts + contradictions for every corpus
// person that exists in the DB. Missing persons are ignored — they'll be
// created on first insert.
func resetEvalPersons(ctx context.Context, storer *storage.PostgresStorer, corpus *eval.Corpus) error {
	for _, p := range corpus.Persons {
		personID, err := storer.GetPersonIDByName(ctx, util.NormalizeString(p.Name))
		if err != nil {
			log.Debug().Err(err).Str("person", p.Name).Msg("Reset: person not found, skipping (will be created on insert)")
			continue
		}
		// Order matters: contradictions reference facts, so delete contradictions
		// first, then facts. Both helpers are idempotent.
		if _, err := storer.DeleteContradictionsForPerson(ctx, personID); err != nil {
			log.Warn().Err(err).Str("person", p.Name).Msg("Reset: delete contradictions failed")
		}
		deleted, err := storer.DeleteFactsForPerson(ctx, personID)
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Msg("Reset: delete facts failed")
			continue
		}
		log.Info().Str("person", p.Name).Int64("facts_deleted", deleted).Msg("Reset: pre-existing data cleared")
	}
	return nil
}

// ingestCorpus inserts every fact one-by-one (so we can map each corpus ID
// to its assigned DB fact ID). Returns the corpus_id → db_fact_id map.
// Facts that get deduped during insertion produce a warning and are absent
// from the map; downstream Evaluate handles missing mappings explicitly.
func ingestCorpus(ctx context.Context, embedder embedding.EmbeddingGenerator, inserter *core.FactInserter, corpus *eval.Corpus) (map[string]int64, error) {
	idMap := make(map[string]int64)

	for _, person := range corpus.Persons {
		for _, stance := range person.Stances {
			for _, fact := range stance.Facts {
				embs, err := embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{fact.EmbeddingText})
				if err != nil {
					return idMap, fmt.Errorf("embed %s: %w", fact.ID, err)
				}
				if len(embs) == 0 {
					return idMap, fmt.Errorf("embed %s returned empty embedding slice", fact.ID)
				}
				emb := embs[0]
				in := core.StoreFactInput{
					SubjectEntity:        person.Name,
					SubjectEntityType:    person.EntityType,
					NormalizedFactText:   fact.Text,
					NormalizedFactTextEn: fact.TextEn,
					EmbeddingText:        fact.EmbeddingText,
					Embedding:            emb,
					OriginalQuote:        fact.Quote,
					CanonicalVerb:        fact.Verb,
					SourceURL:            fact.SourceURL,
					PublishedAt:          fact.ParsedDate,
					SourceType:           "eval",
					Lang:                 fact.Lang,
					Metadata: map[string]any{
						"eval_corpus_id": fact.ID,
						"eval_axis":      stance.Axis,
					},
				}
				result, err := inserter.InsertFacts(ctx, []core.StoreFactInput{in})
				if err != nil {
					return idMap, fmt.Errorf("insert %s: %w", fact.ID, err)
				}
				if len(result.InsertedMeta) > 0 {
					idMap[fact.ID] = result.InsertedMeta[0].FactID
				} else {
					log.Warn().Str("corpus_id", fact.ID).Msg("Fact was deduped during insertion; will be absent from id map")
				}
			}
		}
	}
	return idMap, nil
}

// collectContradictions pulls every contradiction recorded for the corpus's
// persons. Deduplicates by (fact_a_id, fact_b_id) so the same pair recorded
// under multiple persons is reported once.
func collectContradictions(ctx context.Context, storer *storage.PostgresStorer, corpus *eval.Corpus) ([]eval.DetectedContradiction, error) {
	seen := make(map[eval.FactIDPair]string)
	for _, p := range corpus.Persons {
		personID, err := storer.GetPersonIDByName(ctx, util.NormalizeString(p.Name))
		if err != nil {
			log.Debug().Err(err).Str("person", p.Name).Msg("Collect: person not found, skipping")
			continue
		}
		page, err := storer.GetPersonContradictions(ctx, personID, 1000, 0)
		if err != nil {
			return nil, fmt.Errorf("read contradictions for %s: %w", p.Name, err)
		}
		for _, c := range page.Items {
			key := eval.PairKey(c.FactAID, c.FactBID)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = c.Reason
		}
	}
	out := make([]eval.DetectedContradiction, 0, len(seen))
	for key, reason := range seen {
		out = append(out, eval.DetectedContradiction{FactAID: key.A, FactBID: key.B, Reason: reason})
	}
	return out, nil
}
