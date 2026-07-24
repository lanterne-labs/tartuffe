package alerting

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// PipelineHealth mirrors storage.PipelineHealth without importing storage
// (alerting stays dependency-light; cmd/web.go adapts between the two).
type PipelineHealth struct {
	LastOKStart  *time.Time
	StuckRunning int
	RecentFailed int
}

const (
	// pipelineWatchdogInterval is how often the watchdog re-checks health.
	pipelineWatchdogInterval = 1 * time.Hour
	// pipelineMaxSilence is the alert threshold for "no successful run": the
	// cron fires daily, so >30h without a success/warning run means at least
	// one full cycle was missed.
	pipelineMaxSilence = 30 * time.Hour
	// pipelineAlertCooldown suppresses repeats of the SAME alert condition so a
	// standing outage pages once a day, not once an hour.
	pipelineAlertCooldown = 24 * time.Hour
)

// evaluatePipelineHealth turns a health snapshot into alert messages, honoring
// the per-condition cooldown recorded in lastSent (mutated on emit). Factored
// out of the poller loop so it is unit-testable without tickers or Telegram.
func evaluatePipelineHealth(h PipelineHealth, now time.Time, lastSent map[string]time.Time) []string {
	var msgs []string
	emit := func(key, msg string) {
		if now.Sub(lastSent[key]) < pipelineAlertCooldown {
			return
		}
		lastSent[key] = now
		msgs = append(msgs, msg)
	}

	switch {
	case h.LastOKStart == nil:
		emit("silence", "🚨 pipeline: no successful run recorded AT ALL — is the cron service configured?")
	case now.Sub(*h.LastOKStart) > pipelineMaxSilence:
		emit("silence", fmt.Sprintf("🚨 pipeline: no successful run in %.0fh (last OK started %s)",
			now.Sub(*h.LastOKStart).Hours(), h.LastOKStart.Format("2006-01-02 15:04 MST")))
	}
	if h.StuckRunning > 0 {
		emit("stuck", fmt.Sprintf("⚠️ pipeline: %d run(s) stuck in 'running' for >6h (killed or hung — check the daily pipeline service)", h.StuckRunning))
	}
	if h.RecentFailed > 0 {
		emit("failed", fmt.Sprintf("⚠️ pipeline: %d run(s) errored in the last 24h", h.RecentFailed))
	}
	return msgs
}

// StartPipelineWatchdog polls pipeline health hourly and sends Telegram alerts
// when the daily pipeline has gone silent (>30h without a successful run), a
// run is stuck in 'running', or runs errored. Runs until ctx is cancelled.
// fetch is an adapter around storage.GetPipelineHealth so this package does not
// depend on storage.
func StartPipelineWatchdog(ctx context.Context, notifier Notifier, fetch func(context.Context) (PipelineHealth, error)) {
	if notifier == nil || fetch == nil {
		log.Info().Msg("Pipeline watchdog disabled (no notifier)")
		return
	}
	lastSent := make(map[string]time.Time)
	ticker := time.NewTicker(pipelineWatchdogInterval)
	defer ticker.Stop()
	log.Info().Dur("interval", pipelineWatchdogInterval).Msg("Pipeline watchdog started")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h, err := fetch(ctx)
			if err != nil {
				log.Warn().Err(err).Msg("Pipeline watchdog: health query failed")
				continue
			}
			for _, msg := range evaluatePipelineHealth(h, time.Now(), lastSent) {
				if err := notifier.Send(ctx, msg); err != nil {
					log.Warn().Err(err).Msg("Pipeline watchdog: Telegram send failed")
				}
			}
		}
	}
}
