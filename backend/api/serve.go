package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/archiver"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// Config is the listener configuration of the web service.
type Config struct {
	// Port the public API listens on.
	Port string
	// MetricsPort the internal Prometheus /metrics and /health endpoints listen on.
	MetricsPort string
}

// Serve runs the web service: the public API, the internal metrics server and
// the background workers only the long-lived web process hosts (balance
// poller, pipeline watchdog, source archiver). Blocks until the API listener
// fails.
func Serve(ctx context.Context, storer *storage.PostgresStorer, cfg Config) error {
	server := NewServer(storer)
	storer.StartMetricsCollectors(ctx)

	// Start internal metrics server on a separate port
	metricsMux := metrics.NewServeMux()
	metricsMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := storer.Ping(r.Context()); err != nil {
			log.Error().Err(err).Msg("metrics health check: db ping failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
	})

	go func() {
		log.Info().Str("port", cfg.MetricsPort).Msg("Starting internal metrics server")
		if err := metrics.ListenAndServe(cfg.MetricsPort, metricsMux); err != nil {
			log.Error().Err(err).Msg("Metrics server failed")
		}
	}()

	// Start balance alert poller if Telegram is configured
	if alerterInstance := alerting.NewBalanceAlerterFromEnv(); alerterInstance != nil {
		interval := alerting.BalanceConfigFromEnv().CheckInterval
		go alerting.StartBalancePoller(ctx, alerterInstance, interval)
		llm.OnCreditError = func(provider, errMsg string) {
			alerterInstance.AlertCreditError(context.Background(), provider, errMsg)
		}
		log.Info().Msg("Balance alerting enabled (Telegram + poller)")
	}

	// Pipeline watchdog: pages via Telegram when the daily pipeline goes silent
	// (>30h without a successful run), a run is stuck in 'running', or runs
	// errored — the Jul 22-23 outage produced none of these signals anywhere
	// (mirror-1ir). Lives here because the web service is the only long-lived
	// process; the cron container is ephemeral.
	if tn := alerting.NewTelegramNotifierFromEnv(); tn != nil {
		go alerting.StartPipelineWatchdog(ctx, tn, func(c context.Context) (alerting.PipelineHealth, error) {
			h, err := storer.GetPipelineHealth(c)
			return alerting.PipelineHealth{LastOKStart: h.LastOKStart, StuckRunning: h.StuckRunning, RecentFailed: h.RecentFailed}, err
		})
	} else {
		log.Info().Msg("Pipeline watchdog disabled (Telegram not configured)")
	}

	// Daily pipeline runs as a separate the host cron service (`dm pipeline daily`),
	// not in-process. See CLAUDE.md → Deployment.

	// Background archiver: politely submits fact source URLs to the Wayback Machine
	// so snapshots survive paywalls/deletions. Decoupled from ingestion.
	go archiver.New(storer).Run(ctx, 1*time.Hour)

	addr := ":" + cfg.Port
	log.Info().Str("port", cfg.Port).Msg("Starting Digital Mirror web server")

	return server.Start(addr)
}
