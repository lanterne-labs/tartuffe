package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/plugins/frpolitics"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// startPipelineScheduler launches background goroutines for periodic pipeline runs.
// Two independent schedules:
//   - Daily pipeline (PIPELINE_INTERVAL_HOURS): news content collection + batch LLM submission + contradiction detection
//   - Frpolitics pipeline (FRPOLITICS_INTERVAL_DAYS): French politics votes + news
func startPipelineScheduler(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier) {
	dailyHours := envInt("PIPELINE_INTERVAL_HOURS", 0)
	frpoliticsDays := envInt("FRPOLITICS_INTERVAL_DAYS", 0)

	if dailyHours <= 0 && frpoliticsDays <= 0 {
		log.Info().Msg("Pipeline scheduler disabled (both intervals are 0)")
		return
	}

	var dailyRunning atomic.Bool
	var frpoliticsRunning atomic.Bool

	if dailyHours > 0 {
		interval := time.Duration(dailyHours) * time.Hour
		go func() {
			log.Info().Int("hours", dailyHours).Msg("Daily pipeline scheduler started")

			// Run immediately on startup, then repeat on interval
			runDailyPipeline(ctx, storer, notifier)

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					log.Info().Msg("Daily pipeline scheduler stopped")
					return
				case <-ticker.C:
					if !dailyRunning.CompareAndSwap(false, true) {
						log.Warn().Msg("Daily pipeline still running, skipping tick")
						continue
					}
					runDailyPipeline(ctx, storer, notifier)
					dailyRunning.Store(false)
				}
			}
		}()
	}

	if frpoliticsDays > 0 {
		interval := time.Duration(frpoliticsDays) * 24 * time.Hour
		go func() {
			log.Info().Int("days", frpoliticsDays).Msg("Frpolitics pipeline scheduler started")

			// Run immediately on startup, then repeat on interval
			runFrpoliticsPipeline(ctx, storer, notifier)

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					log.Info().Msg("Frpolitics pipeline scheduler stopped")
					return
				case <-ticker.C:
					if !frpoliticsRunning.CompareAndSwap(false, true) {
						log.Warn().Msg("Frpolitics pipeline still running, skipping tick")
						continue
					}
					runFrpoliticsPipeline(ctx, storer, notifier)
					frpoliticsRunning.Store(false)
				}
			}
		}()
	}
}

// runDailyPipeline collects news content for all queued persons, submits to batch LLM,
// then runs contradiction detection. The batch poller (already running) handles
// retrieving results and inserting facts asynchronously.
func runDailyPipeline(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier) {
	start := time.Now()
	log.Info().Msg("Daily pipeline started")

	// Step 1: Collect news content and submit to batch LLM
	batchErr := collectAndSubmitBatch(ctx, storer)
	if batchErr != nil {
		log.Error().Err(batchErr).Msg("Daily pipeline: batch submission failed")
	}

	// Step 2: Contradiction detection (catch-up pass for existing facts)
	detectOpts := ContradictionDetectOpts{}
	detectErr := executeContradictionDetection(ctx, storer, detectOpts)
	if detectErr != nil {
		log.Error().Err(detectErr).Msg("Daily pipeline: contradiction detection failed")
	}

	elapsed := time.Since(start)

	// Notify via Telegram
	if notifier != nil {
		var msg string
		if batchErr != nil || detectErr != nil {
			var parts []string
			if batchErr != nil {
				parts = append(parts, "batch: "+batchErr.Error())
			}
			if detectErr != nil {
				parts = append(parts, "detect: "+detectErr.Error())
			}
			msg = fmt.Sprintf("⚠️ <b>Daily pipeline errors</b> (%s)\n%s",
				elapsed.Round(time.Second), strings.Join(parts, "\n"))
		} else {
			msg = fmt.Sprintf("✅ <b>Daily pipeline completed</b> (%s)",
				elapsed.Round(time.Second))
		}
		if err := notifier.Send(ctx, msg); err != nil {
			log.Error().Err(err).Msg("Failed to send pipeline Telegram notification")
		}
	}

	log.Info().Dur("elapsed", elapsed).Msg("Daily pipeline finished")
}

// collectAndSubmitBatch searches news for all queued persons, extracts article content,
// and submits everything as a single Gemini batch job. Returns immediately —
// the batch poller handles retrieval and fact insertion asynchronously.
func collectAndSubmitBatch(ctx context.Context, storer *storage.PostgresStorer) error {
	// Get queue
	queue, err := storer.GetSearchQueue(ctx)
	if err != nil {
		return fmt.Errorf("failed to get search queue: %w", err)
	}
	if len(queue) == 0 {
		log.Info().Msg("Search queue is empty, skipping batch submission")
		return nil
	}

	// Initialize components for content extraction (no LLM needed here)
	serpAPI, err := search.NewSerpAPINewsProviderFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize SerpAPI: %w", err)
	}

	rawExtractor, err := extractor.NewExtractorFromType("webcrawler")
	if err != nil {
		return fmt.Errorf("failed to initialize webcrawler: %w", err)
	}
	batchExtractor := core.NewBatchExtractorFromEnv(rawExtractor)

	// News provider — only needs search + content extraction (no FactExtractor/Embedder)
	newsProvider := &news.Provider{
		NewsSearch:       serpAPI,
		ContentExtractor: batchExtractor,
		Storer:           storer,
	}

	newsOpts := news.Options{
		MaxParse:     5,
		Location:     "France",
		RevisitHours: 24 * 365 * 10,
	}

	// Collect content from all queued persons
	var allItems []core.BatchContentItem
	for i, entry := range queue {
		log.Info().Int("progress", i+1).Int("total", len(queue)).Str("person", entry.Name).Msg("Collecting news content")

		targetType := news.TargetPerson
		if entry.EntityType == "party" {
			targetType = news.TargetParty
		}
		target := news.SearchTarget{
			Name:     entry.Name,
			Type:     targetType,
			PersonID: &entry.PersonID,
		}

		items, result, err := newsProvider.CollectContent(ctx, target, newsOpts)
		if err != nil {
			log.Error().Err(err).Str("person", entry.Name).Msg("Failed to collect content")
			continue
		}

		if result != nil {
			log.Info().
				Str("person", entry.Name).
				Int("articles_found", result.ArticlesFound).
				Int("articles_parsed", result.ArticlesParsed).
				Int("content_items", len(items)).
				Msg("Content collection completed")
		}

		// Convert news ContentItems to batch BatchContentItems
		for _, item := range items {
			allItems = append(allItems, core.BatchContentItem{
				SourceURL:   item.SourceURL,
				Content:     item.Content,
				TargetName:  item.TargetName,
				TargetType:  item.TargetType,
				PublishedAt: item.PublishedAt,
			})
		}
	}

	if len(allItems) == 0 {
		log.Info().Msg("No content collected, skipping batch submission")
		return nil
	}

	// Initialize batch provider and submit
	provider, err := newBatchProvider()
	if err != nil {
		return fmt.Errorf("failed to create batch provider: %w", err)
	}

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to create embedder: %w", err)
	}

	inserter := core.NewFactInserter(storer)
	adapter := newBatchStorerAdapter(storer)
	orchestrator := core.NewBatchJobOrchestrator(provider, adapter, embedder, inserter, categorizer.ParseFactJSON)

	batchID, err := orchestrator.PrepareAndSubmit(ctx, allItems, map[string]any{
		"source":    "daily-pipeline",
		"submitted": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("batch submission failed: %w", err)
	}

	log.Info().
		Int64("batch_id", batchID).
		Int("content_items", len(allItems)).
		Msg("Batch submitted — poller will process results")

	return nil
}

// runFrpoliticsPipeline executes the frpolitics pipeline: votes + news for deputies.
func runFrpoliticsPipeline(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier) {
	start := time.Now()
	log.Info().Msg("Frpolitics pipeline started")

	opts := frpolitics.Options{
		Mode:             "both",
		MaxLaws:          10,
		PartyThreshold:   0.8,
		NewsLimit:        5,
		NewsLocation:     "France",
		ContentExtractor: "webcrawler",
	}

	runErr := frpolitics.Run(ctx, storer, opts)
	if runErr != nil {
		log.Error().Err(runErr).Msg("Frpolitics pipeline failed")
	}

	elapsed := time.Since(start)

	// Notify via Telegram
	if notifier != nil {
		var msg string
		if runErr != nil {
			msg = fmt.Sprintf("⚠️ <b>Frpolitics pipeline failed</b> (%s)\n%s",
				elapsed.Round(time.Second), runErr.Error())
		} else {
			msg = fmt.Sprintf("✅ <b>Frpolitics pipeline completed</b> (%s)",
				elapsed.Round(time.Second))
		}
		if err := notifier.Send(ctx, msg); err != nil {
			log.Error().Err(err).Msg("Failed to send frpolitics Telegram notification")
		}
	}

	log.Info().Dur("elapsed", elapsed).Msg("Frpolitics pipeline finished")
}

// envInt reads an integer from an environment variable, returning defaultVal on missing/invalid.
func envInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}
