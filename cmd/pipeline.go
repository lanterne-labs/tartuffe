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
	"digital-mirror/backend/plugins/frpolitics"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// startPipelineScheduler launches background goroutines for periodic pipeline runs.
// Two independent schedules:
//   - Daily pipeline (PIPELINE_INTERVAL_HOURS): queue process (news) + contradiction detection
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

// runDailyPipeline executes the daily pipeline: queue process (news) then contradiction detection.
func runDailyPipeline(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier) {
	start := time.Now()
	log.Info().Msg("Daily pipeline started")

	// Step 1: Queue process — news search + fact extraction
	queueOpts := QueueProcessOpts{
		Plugins:       "news",
		NewsMaxParse:  5,
		NewsLocation:  "France",
		NewsExtractor: "webcrawler",
	}
	queueErr := executeQueueProcess(ctx, storer, queueOpts)
	if queueErr != nil {
		log.Error().Err(queueErr).Msg("Daily pipeline: queue process failed")
	}

	// Step 2: Contradiction detection (catch-up pass)
	detectOpts := ContradictionDetectOpts{}
	detectErr := executeContradictionDetection(ctx, storer, detectOpts)
	if detectErr != nil {
		log.Error().Err(detectErr).Msg("Daily pipeline: contradiction detection failed")
	}

	elapsed := time.Since(start)

	// Notify via Telegram
	if notifier != nil {
		var msg string
		if queueErr != nil || detectErr != nil {
			var parts []string
			if queueErr != nil {
				parts = append(parts, "queue: "+queueErr.Error())
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

// runFrpoliticsPipeline executes the frpolitics pipeline: votes + news for deputies.
func runFrpoliticsPipeline(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier) {
	start := time.Now()
	log.Info().Msg("Frpolitics pipeline started")

	opts := frpolitics.Options{
		Mode:             "both",
		MaxLaws:          10,
		PartyThreshold:   0.8,
		Legislatures:     []int{16},
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
