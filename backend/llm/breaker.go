package llm

import (
	"bytes"
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

// ErrUnavailable marks a call that failed because no endpoint could serve it:
// the primary was unreachable (or skipped by the open breaker) and the
// fallback, when configured, failed too. The request itself was not at fault,
// so callers that count per-item failures must not charge it to the item.
var ErrUnavailable = errors.New("LLM unavailable")

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

// isEndpointOutage reports whether err means the ENDPOINT is unusable —
// transport failure, timeout, 5xx, sustained rate limiting — as opposed to this
// one request being unserviceable. Only the former may arm the breaker: a 4xx
// is per-request, and arming on it takes every OTHER call to the fallback for
// probeInterval even though the endpoint is perfectly healthy.
//
// A 4xx still fails over for the call that hit it (the fallback may accept a
// prompt the primary rejected), it just no longer speaks for the endpoint.
func isEndpointOutage(err error) bool {
	if err == nil {
		return false
	}
	var clientErr *ClientError
	return !errors.As(err, &clientErr)
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
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		MarkPrimaryDown("start-of-run probe: HTTP " + resp.Status)
		return false
	}

	// /models answers without loading a model, so a reachable llama-swap can
	// still hide a COLD model behind it: the first completion of a run then
	// pays the ~105s load, Cloudflare cuts the request at its 100s proxy
	// ceiling with a 524, the per-call retries burn out, and the breaker sends
	// the whole run to the fallback (2026-09-03 cron lost its news phase this
	// way). Warm the chat model here, tolerating 5xx until the load finishes.
	if !warmPrimaryChat(ctx, base) {
		MarkPrimaryDown("start-of-run warm-up: chat model did not come up within " + warmupBudget.String())
		return false
	}
	MarkPrimaryUp()
	return true
}

// warmupBudget bounds how long ProbePrimary waits for a cold chat model. A
// llama-swap load of the 26B NVFP4 takes ~105s; the budget covers two loads
// (e.g. a swap displacing another model first). Variable so tests can shrink it.
var warmupBudget = 4 * time.Minute

// warmupRetryDelay is the pause between warm-up attempts. Variable for tests.
var warmupRetryDelay = 5 * time.Second

// warmPrimaryChat POSTs 1-token completions at the primary until one answers
// or the budget runs out. Any HTTP status below 500 counts as warm — the model
// answered, even if it disliked this particular request. 5xx (Cloudflare 524
// while loading, transient 502) and transport errors are retried.
func warmPrimaryChat(ctx context.Context, base string) bool {
	model := ConfigFromEnv().ChatModel
	payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"ok"}],"max_tokens":1}`)
	url := ChatCompletionsURL(base)

	deadline := time.Now().Add(warmupBudget)
	for attempt := 1; time.Now().Before(deadline) && ctx.Err() == nil; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, 115*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return true // malformed URL is a config problem, not an outage
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+os.Getenv("OPEN_AI_KEY"))
		if id := os.Getenv("CF_ACCESS_CLIENT_ID"); id != "" {
			req.Header.Set("CF-Access-Client-Id", id)
			req.Header.Set("CF-Access-Client-Secret", os.Getenv("CF_ACCESS_CLIENT_SECRET"))
		}
		resp, err := (&http.Client{Timeout: 115 * time.Second}).Do(req)
		cancel()
		if err == nil {
			status := resp.StatusCode
			resp.Body.Close()
			if status < 500 {
				if attempt > 1 {
					log.Info().Int("attempts", attempt).Msg("Primary chat model warmed up")
				}
				return true
			}
			log.Warn().Int("status", status).Int("attempt", attempt).Msg("Warm-up completion got 5xx (model likely still loading); retrying")
		} else {
			log.Warn().Err(err).Int("attempt", attempt).Msg("Warm-up completion failed; retrying")
		}
		select {
		case <-time.After(warmupRetryDelay):
		case <-ctx.Done():
			return false
		}
	}
	return false
}
