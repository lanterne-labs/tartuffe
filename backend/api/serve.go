package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"digital-mirror/backend/accounts/providers"
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
	reg, err := providers.RegistryFromEnv()
	if err != nil {
		return err
	}
	names := []string{}
	for _, p := range reg.Infos() {
		names = append(names, p.Name)
	}
	log.Info().Strs("auth_providers", names).Msg("Sign-in providers enabled")
	acc, m, p := Wire(storer, reg)
	server := NewServer(storer, acc, m, p)
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

	// The collection tasks and the maintenance pass run in the worker service
	// (`dm worker run`), not in-process. See CLAUDE.md → Deployment.

	// Background archiver: politely submits fact source URLs to the Wayback Machine
	// so snapshots survive paywalls/deletions. Decoupled from ingestion.
	go archiver.New(storer).Run(ctx, 1*time.Hour)

	addr := ":" + cfg.Port
	log.Info().Str("port", cfg.Port).Msg("Starting Digital Mirror web server")

	return server.Start(addr)
}
