package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// UnavailableError is the error of a Claude endpoint that cannot serve calls
// before RetryAt: every Team seat at its usage limit, or a failed batch API
// call. A zero RetryAt means that the reset time is not known (a reset text
// that does not parse). Err wraps ErrUnavailable. WaitForEndpoint reads it to wait
// for the endpoint that failed. A local outage has no UnavailableError: the
// wait probes the local endpoint.
type UnavailableError struct {
	Endpoint EndpointName
	RetryAt  time.Time
	Err      error
}

func (e *UnavailableError) Error() string { return e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// claudeRetryDelay is the wait before the next call to a Claude endpoint
// that failed as unavailable and gave no reset time.
const claudeRetryDelay = 5 * time.Minute

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

// IsEndpointOutage reports whether err means the ENDPOINT is unusable —
// transport failure, timeout, 5xx, sustained rate limiting — as opposed to this
// one request being unserviceable. Only the former may arm the breaker: a 4xx
// is per-request, and arming on it takes every OTHER call to the fallback for
// probeInterval even though the endpoint is perfectly healthy.
//
// A 4xx still fails over for the call that hit it (the fallback may accept a
// prompt the primary rejected), it just no longer speaks for the endpoint.
func IsEndpointOutage(err error) bool {
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
// nothing to flip to, so the breaker would only add noise). A routing that
// sends no task to the local endpoint (the claude-team profile) ⇒ the same:
// on 2026-11-09 the probe of the idle GPU box logged "routing calls straight
// to fallback" on a worker whose every call went to a Claude seat, and the
// line was read as the pass running on OpenRouter.
func ProbePrimary(ctx context.Context) bool {
	if strings.TrimSpace(os.Getenv("FALLBACK_URL")) == "" || !ActiveRoutingUsesLocal() {
		return true
	}
	base := primaryBaseURL()
	if base == "" {
		return true
	}
	// A malformed base URL is a config problem, not an outage.
	if err := probeModels(ctx, base); err != nil && !errors.Is(err, errBadBaseURL) {
		MarkPrimaryDown("start-of-run probe: " + err.Error())
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

// primaryBaseURL is the primary endpoint's base URL (OPEN_AI_API) without a
// trailing slash; "" when unset.
func primaryBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("OPEN_AI_API")), "/")
}

// errBadBaseURL marks a probe that could not be built from OPEN_AI_API.
var errBadBaseURL = errors.New("malformed OPEN_AI_API")

// primaryRequest builds a request to the primary with the auth and
// Cloudflare-Access headers; an error wraps errBadBaseURL.
func primaryRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadBaseURL, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPEN_AI_KEY"))
	if id := os.Getenv("CF_ACCESS_CLIENT_ID"); id != "" {
		req.Header.Set("CF-Access-Client-Id", id)
		req.Header.Set("CF-Access-Client-Secret", os.Getenv("CF_ACCESS_CLIENT_SECRET"))
	}
	return req, nil
}

// probeModels GETs {base}/models with a short timeout. nil means the endpoint
// answered below 500; a malformed base URL returns an error wrapping
// errBadBaseURL.
func probeModels(ctx context.Context, base string) error {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := primaryRequest(reqCtx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

// warmChatOnce POSTs one 1-token completion at the primary's chat model. Any
// HTTP status below 500 counts as warm (nil) — the model answered, even if it
// disliked this particular request; a 5xx (Cloudflare 524 while loading,
// transient 502) or a transport error is returned, and a malformed base URL
// returns an error wrapping errBadBaseURL.
func warmChatOnce(ctx context.Context, base string) error {
	model := ConfigFromEnv().ChatModel
	payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"ok"}],"max_tokens":1}`)
	reqCtx, cancel := context.WithTimeout(ctx, 115*time.Second)
	defer cancel()
	req, err := primaryRequest(reqCtx, http.MethodPost, ChatCompletionsURL(base), payload)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 115 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("warm-up completion: HTTP %s", resp.Status)
	}
	return nil
}

// WaitForEndpoint waits for the endpoint of cause, an error that wraps
// ErrUnavailable. The wait ends at until at the latest. It reports whether
// the endpoint can serve again and after how many probes:
//   - An UnavailableError (a Claude endpoint): the wait sleeps until its
//     RetryAt and reports back after 1 probe. With polls=1, the gate of the
//     daily run counts the retry as a quick refail, so a reset time that is
//     wrong cannot make a phase retry without end. The wait does not probe
//     the local endpoint, which can be up while the Claude seat is at its
//     limit.
//   - A RetryAt after until, or a zero RetryAt (reset time not known), gives
//     false at once: the run stops at its phase and the next run continues
//     it. With the wait budget of the daily run (30 min), a seat that resets
//     3 h later thus stops the run.
//   - Any other error: waitForPrimary.
func WaitForEndpoint(ctx context.Context, cause error, until time.Time) (back bool, polls int) {
	var ue *UnavailableError
	if !errors.As(cause, &ue) {
		return waitForPrimary(ctx, until)
	}
	if ue.RetryAt.IsZero() || ue.RetryAt.After(until) {
		log.Error().Str("endpoint", string(ue.Endpoint)).Time("retry_at", ue.RetryAt).Time("until", until).
			Msg("LLM wait: the reset time of the endpoint is not known or is after the end of the wait")
		return false, 0
	}
	log.Warn().Str("endpoint", string(ue.Endpoint)).Time("retry_at", ue.RetryAt).
		Msg("LLM unavailable: waiting for the reset of the endpoint")
	select {
	case <-time.After(time.Until(ue.RetryAt)):
		return true, 1
	case <-ctx.Done():
		log.Error().Err(ctx.Err()).Str("endpoint", string(ue.Endpoint)).
			Msg("LLM wait: cancelled before the reset of the endpoint")
		return false, 0
	}
}

// waitPollInterval is the pause before each probe of waitForPrimary.
// Variable so tests can shrink it.
var waitPollInterval = 2 * time.Minute

// waitForPrimary blocks until the primary endpoint answers its /models probe
// and its chat model answers one warm-up completion (a reachable llama-swap
// can hide a cold or failing model; the warm-up makes it load the model),
// probing every waitPollInterval — the first probe too comes after one
// interval, so a request that keeps failing on a healthy endpoint is not
// retried in a tight loop. It reports whether the endpoint came back before
// until (false as well when ctx ends or OPEN_AI_API is unset or malformed) and
// how many probes it made. Unlike ProbePrimary it probes even without a
// fallback: the caller waits for the endpoint itself. On success the breaker
// is cleared, so the next call goes to the primary at once instead of waiting
// out probeInterval.
func waitForPrimary(ctx context.Context, until time.Time) (back bool, polls int) {
	base := primaryBaseURL()
	if base == "" {
		log.Error().Msg("LLM wait: OPEN_AI_API is not set; nothing to wait for")
		return false, 0
	}
	start := time.Now()
	log.Warn().Time("until", until).Dur("poll_every", waitPollInterval).
		Msg("LLM unavailable: waiting for the primary endpoint")
	for {
		if time.Now().Add(waitPollInterval).After(until) {
			log.Error().Dur("waited", time.Since(start).Round(time.Second)).Time("until", until).Int("polls", polls).
				Msg("LLM wait: deadline reached before the primary endpoint came back")
			return false, polls
		}
		select {
		case <-time.After(waitPollInterval):
		case <-ctx.Done():
			log.Error().Err(ctx.Err()).Dur("waited", time.Since(start).Round(time.Second)).
				Msg("LLM wait: cancelled before the primary endpoint came back")
			return false, polls
		}
		polls++
		err := probeModels(ctx, base)
		if err == nil {
			err = warmChatOnce(ctx, base)
		}
		if errors.Is(err, errBadBaseURL) {
			log.Error().Err(err).Msg("LLM wait: the primary endpoint URL is malformed; it cannot come back")
			return false, polls
		}
		if err == nil {
			MarkPrimaryUp()
			log.Info().Dur("outage", time.Since(start).Round(time.Second)).Int("polls", polls).
				Msg("LLM wait: primary endpoint is back")
			return true, polls
		}
		if polls%5 == 0 {
			log.Warn().Err(err).Dur("waiting", time.Since(start).Round(time.Second)).Int("polls", polls).
				Msg("LLM wait: primary endpoint still down")
		}
	}
}

// warmupBudget bounds how long ProbePrimary waits for a cold chat model. A
// llama-swap load of the 26B NVFP4 takes ~105s; the budget covers two loads
// (e.g. a swap displacing another model first). Variable so tests can shrink it.
var warmupBudget = 4 * time.Minute

// warmupRetryDelay is the pause between warm-up attempts. Variable for tests.
var warmupRetryDelay = 5 * time.Second

// warmPrimaryChat retries warmChatOnce until the chat model answers or the
// budget runs out. A malformed base URL is a config problem, not an outage,
// and counts as warm.
func warmPrimaryChat(ctx context.Context, base string) bool {
	deadline := time.Now().Add(warmupBudget)
	for attempt := 1; time.Now().Before(deadline) && ctx.Err() == nil; attempt++ {
		err := warmChatOnce(ctx, base)
		if err == nil || errors.Is(err, errBadBaseURL) {
			if attempt > 1 {
				log.Info().Int("attempts", attempt).Msg("Primary chat model warmed up")
			}
			return true
		}
		log.Warn().Err(err).Int("attempt", attempt).Msg("Warm-up completion failed (model likely still loading); retrying")
		select {
		case <-time.After(warmupRetryDelay):
		case <-ctx.Done():
			return false
		}
	}
	return false
}
