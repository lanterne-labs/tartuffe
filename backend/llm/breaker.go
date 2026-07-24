package llm

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// primaryBreaker is a process-wide circuit breaker for the PRIMARY LLM endpoint
// (the home GPU box). When the box is unreachable, every call otherwise burns
// the full per-endpoint retry backoff (~2 min) before failing over — a pipeline
// run makes hundreds of calls, so an outage turned 3-hour runs into 10-hour
// ones (mirror-s8t). With the breaker armed, calls skip the primary entirely
// and go straight to the fallback; the primary is re-tried at most once per
// probeInterval (half-open) so recovery is automatic.
//
// One shared breaker (not per-client) is deliberate: chat and embeddings hit
// the same physical box, so one endpoint down means the other is too.
var primaryBreaker struct {
	mu        sync.Mutex
	downUntil time.Time
}

// probeInterval is how long the breaker stays armed before letting one call
// through to test recovery (half-open). A failed test re-arms it.
const probeInterval = 10 * time.Minute

// errPrimaryBreakerOpen is the sentinel error carried when the primary attempt
// was skipped because the breaker is open (fallback handles the call).
var errPrimaryBreakerOpen = errors.New("primary LLM endpoint skipped: circuit breaker open")

// PrimaryDown reports whether the primary endpoint is currently considered
// unreachable. When it returns true, callers with a configured fallback should
// skip the primary attempt entirely.
func PrimaryDown() bool {
	primaryBreaker.mu.Lock()
	defer primaryBreaker.mu.Unlock()
	return time.Now().Before(primaryBreaker.downUntil)
}

// MarkPrimaryDown arms the breaker for probeInterval. Called when the primary
// fails hard (all retries exhausted) or when a start-of-run probe fails.
func MarkPrimaryDown(reason string) {
	primaryBreaker.mu.Lock()
	defer primaryBreaker.mu.Unlock()
	wasDown := time.Now().Before(primaryBreaker.downUntil)
	primaryBreaker.downUntil = time.Now().Add(probeInterval)
	if !wasDown {
		log.Warn().Str("reason", reason).Dur("retry_in", probeInterval).
			Msg("Primary LLM endpoint marked DOWN — routing calls straight to fallback")
	}
}

// MarkPrimaryUp clears the breaker. Called whenever a primary call succeeds.
func MarkPrimaryUp() {
	primaryBreaker.mu.Lock()
	defer primaryBreaker.mu.Unlock()
	if time.Now().Before(primaryBreaker.downUntil) {
		log.Info().Msg("Primary LLM endpoint recovered — resuming primary-first routing")
	}
	primaryBreaker.downUntil = time.Time{}
}

// ProbePrimary performs a cheap reachability check of the primary endpoint
// (GET {OPEN_AI_API}/models with the Cloudflare-Access headers, short timeout)
// and arms the breaker on failure. Called at the start of a pipeline run so an
// unreachable home box flips the entire run to the fallback immediately instead
// of per-call. Returns true when the primary answered.
//
// No fallback configured ⇒ always returns true without probing (there is
// nothing to flip to, so the breaker would only add noise).
func ProbePrimary(ctx context.Context) bool {
	if strings.TrimSpace(os.Getenv("FALLBACK_URL")) == "" {
		return true
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("OPEN_AI_API")), "/")
	if base == "" {
		return true
	}
	url := base + "/models"

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return true // malformed base URL is a config problem, not an outage
	}
	if id := os.Getenv("CF_ACCESS_CLIENT_ID"); id != "" {
		req.Header.Set("CF-Access-Client-Id", id)
		req.Header.Set("CF-Access-Client-Secret", os.Getenv("CF_ACCESS_CLIENT_SECRET"))
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPEN_AI_KEY"))

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		MarkPrimaryDown("start-of-run probe: " + err.Error())
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		MarkPrimaryDown("start-of-run probe: HTTP " + resp.Status)
		return false
	}
	MarkPrimaryUp()
	return true
}
