package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// ClaudeAPIEndpoint answers each call with one synchronous POST /v1/messages
// on the Claude API, with the key of ANTHROPIC_API_KEY. The claude-team
// profile uses it as the fallback of the seat: the calls go to the API when
// the seat reaches its usage limit. The API bills each call at the list
// price, so the endpoint stops when the spend of the day (UTC) reaches its
// cap, and serves again the next day.
type ClaudeAPIEndpoint struct {
	api    claudeHTTP
	maxUSD float64
	now    func() time.Time // nil: time.Now

	mu       sync.Mutex
	usage    map[usageKey]*TaskUsage
	prefixes prefixCache
	// day is the UTC day spentUSD and stop belong to; a new day resets them.
	day time.Time
	// spentUSD is the cost of the replies plus the reserved worst case of the
	// calls in flight and of the calls that the API can have billed without
	// a reply (a timeout, a transport error).
	spentUSD float64
	stop     *UnavailableError // set when the endpoint stops for the rest of the day
}

// claudeAPIRunCapUSD is the spend cap of the API endpoint per UTC day when
// CLAUDE_API_DAY_CAP_USD is not set (claudeAPIDayCapFromEnv). A sync
// call costs about 0.01 USD on Sonnet 5.5 and 0.02 USD on Opus 5.5 (the
// batch pilot of 2026-10-10: 0.01 USD per occurrence pair on Opus at half
// price). A day of the worker makes about 2,000 calls, about 30 USD on the
// API. The seats serve first, so 25 USD lets the overflow finish most days. A
// bad day (a loop, a prompt that grows) costs 25 USD at most, and the next
// day resumes the work.
const claudeAPIRunCapUSD = 25.0

// claudeAPITimeout is the limit of one HTTP request, the default of the
// Anthropic SDKs. A reply with a long thinking can take minutes.
const claudeAPITimeout = 10 * time.Minute

// claudeAPICacheTTL is the TTL of the cache breakpoints: "" is 5 min. The
// prices are factors of the input price for the cached prefix:
//   - No gap of more than 5 min between 2 calls of a family: 1 write, 1.25
//     for 5 min against 2 for 1 h.
//   - 1 such gap: 2 writes, 2.5 for 5 min against 2 + 0.1 (1 read) = 2.1 for
//     1 h.
//
// The calls of a family come seconds apart in one phase, and each cache read
// starts the 5 min again at no cost. So the first case is the usual one.
const claudeAPICacheTTL = ""

// NewClaudeAPIEndpointFromEnv reads ANTHROPIC_API_KEY and the spend cap of
// the day, CLAUDE_API_DAY_CAP_USD (claudeAPIRunCapUSD when not set).
func NewClaudeAPIEndpointFromEnv() (*ClaudeAPIEndpoint, error) {
	h, err := newClaudeHTTPFromEnv(claudeAPITimeout)
	if err != nil {
		return nil, fmt.Errorf("claude api endpoint: %w", err)
	}
	maxUSD, err := claudeAPIDayCapFromEnv()
	if err != nil {
		return nil, fmt.Errorf("claude api endpoint: %w", err)
	}
	return &ClaudeAPIEndpoint{api: h, maxUSD: maxUSD}, nil
}

// claudeAPIDayCapFromEnv reads CLAUDE_API_DAY_CAP_USD, the spend cap of the
// endpoint per UTC day: claudeAPIRunCapUSD when not set; 0 stops the endpoint
// before its first call.
func claudeAPIDayCapFromEnv() (float64, error) {
	s := strings.TrimSpace(os.Getenv("CLAUDE_API_DAY_CAP_USD"))
	if s == "" {
		return claudeAPIRunCapUSD, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("CLAUDE_API_DAY_CAP_USD %q: want a number of USD, 0 or more", s)
	}
	return v, nil
}

// Call sends one Messages request and waits for its reply. Only an endpoint
// from NewClaudeAPIEndpointFromEnv has a key: a struct literal fails here.
//
// Before the request, Call reserves the worst-case cost of the call
// (worstCaseCostUSD). A reply replaces the reservation with the real cost. A
// request that the API rejects (a 4xx) costs nothing. Any other failure keeps
// the reservation: the API can have billed the call. So the cap holds for
// concurrent calls too.
//
// The errors:
//   - The reserved spend passes the cap, the account has no credit (a 402, or
//     a 429 for the spend limit), or the API refuses the key or the URL (a
//     401, 403 or 404): an UnavailableError with a zero RetryAt. The gate of
//     the daily run then stops the run at its phase. Every later call of the
//     process gets the same error and sends nothing.
//   - The API stays down after the retries (transport error, 5xx, 429): an
//     UnavailableError with a RetryAt claudeRetryDelay later.
//   - Another 4xx, a cancelled call or a bad reply: a plain error, as for
//     the other endpoints.
func (t *ClaudeAPIEndpoint) Call(ctx context.Context, req ChatRequest, opts ClaudeOptions) (string, *ChatResponse, error) {
	if t.api.key == "" {
		return "", nil, errors.New("claude api endpoint: no key; use NewClaudeAPIEndpointFromEnv")
	}
	creq, err := NewClaudeRequest(req, opts)
	if err != nil {
		return "", nil, err
	}
	if creq, err = t.withPrefix(req.Task, creq); err != nil {
		return "", nil, err
	}
	body, err := json.Marshal(creq)
	if err != nil {
		return "", nil, fmt.Errorf("marshal claude request: %w", err)
	}
	reserve, err := worstCaseCostUSD(creq, false)
	if err != nil {
		return "", nil, err
	}
	day, err := t.admit(ctx, reserve)
	if err != nil {
		return "", nil, err
	}
	t.mu.Lock()
	u := countTaskCall(&t.usage, req.Task, creq.Model)
	if !creq.hasCacheBreakpoint() {
		u.Uncached++
	}
	t.mu.Unlock()
	fail := func(err error) (string, *ChatResponse, error) {
		t.mu.Lock()
		u.Failed++
		t.mu.Unlock()
		return "", nil, err
	}
	data, err := t.api.do(ctx, http.MethodPost, t.api.baseURL+"/messages", body)
	if err != nil {
		var ce *ClientError
		if errors.As(err, &ce) {
			t.settle(day, reserve, 0) // the API rejected the request: no bill
		}
		return fail(t.callError(ctx, err))
	}
	reply, err := decodeClaudeReply(data)
	if err != nil {
		return fail(err)
	}
	cost, err := ClaudeCostUSD(creq.Model, reply.Usage, false)
	if err != nil {
		return fail(err)
	}
	t.settle(day, reserve, cost)
	t.mu.Lock()
	u.Usage = u.Usage.Add(reply.Usage)
	u.CostUSD += cost
	t.mu.Unlock()
	// A reply that stopped at max_tokens or refused is billed too: the cost
	// above counts it before Text fails.
	text, err := reply.Text(req.wantsJSON())
	if err != nil {
		return fail(err)
	}
	return text, claudeChatResponse(text, reply.Usage), nil
}

// SpendLedger holds the spend of the Claude API per UTC day for every
// process: the worker and each dm run against the same database share one
// cap. Reserve adds cost to the spend of day when the total stays within
// limit, and returns the total and whether it was added; Settle adds delta
// (the real cost minus the reservation) to the spend of day.
type SpendLedger interface {
	Reserve(ctx context.Context, day time.Time, cost, limit float64) (float64, bool, error)
	Settle(ctx context.Context, day time.Time, delta float64) error
}

var (
	ledgerMu sync.RWMutex
	ledger   SpendLedger
)

// SetSpendLedger makes every ClaudeAPIEndpoint of the process count its
// spend in l (nil: in the process only, as the tests do).
func SetSpendLedger(l SpendLedger) {
	ledgerMu.Lock()
	ledger = l
	ledgerMu.Unlock()
}

func spendLedger() SpendLedger {
	ledgerMu.RLock()
	defer ledgerMu.RUnlock()
	return ledger
}

// PhaseLedger is a SpendLedger that also keeps the real cost per phase.
type PhaseLedger interface {
	AddPhase(ctx context.Context, day time.Time, phase string, cost float64) error
}

// The phases of the spend. The worker's maintenance pass runs alone, so one
// value for the process says what a call pays for.
const (
	PhaseExtraction  = "extraction"
	PhaseMaintenance = "maintenance"
)

var spendPhase atomic.Value // string

// SetSpendPhase names what the calls of the process pay for from now on, and
// returns the function that restores the phase before.
func SetSpendPhase(phase string) (restore func()) {
	before := currentSpendPhase()
	spendPhase.Store(phase)
	return func() { spendPhase.Store(before) }
}

func currentSpendPhase() string {
	if p, ok := spendPhase.Load().(string); ok {
		return p
	}
	return PhaseExtraction
}

// APIDayCapUSD is the spend cap of the API key per UTC day (CLAUDE_API_DAY_CAP_USD).
func APIDayCapUSD() (float64, error) { return claudeAPIDayCapFromEnv() }

// claudeLedgerTimeout bounds one write to the spend ledger.
const claudeLedgerTimeout = 10 * time.Second

// admit reserves cost for a call and returns the day it counts in, or
// returns the stop error of the endpoint.
// It stops the endpoint when the reservation would pass the cap of the day,
// until the next day: the spend of the process, and with a SpendLedger the
// spend of every process. The first stop by the cap calls OnCreditError, as
// an account without credit does. A ledger that cannot be written refuses
// the call for claudeRetryDelay: a spend nobody counts has no cap.
func (t *ClaudeAPIEndpoint) admit(ctx context.Context, cost float64) (time.Time, error) {
	t.mu.Lock()
	t.rollDay()
	day, stop := t.day, t.stop
	t.mu.Unlock()
	if stop != nil {
		return day, stop
	}
	spent, ok := 0.0, true
	if l := spendLedger(); l != nil {
		lctx, cancel := context.WithTimeout(ctx, claudeLedgerTimeout)
		var err error
		spent, ok, err = l.Reserve(lctx, day, cost, t.maxUSD)
		cancel()
		if err != nil {
			log.Error().Err(err).Msg("claude api: the spend ledger cannot be written; the call is refused")
			return day, &UnavailableError{
				Endpoint: EndpointClaudeAPI,
				RetryAt:  time.Now().Add(claudeRetryDelay),
				Err:      fmt.Errorf("%w: claude api: spend ledger: %w", ErrUnavailable, err),
			}
		}
	}
	t.mu.Lock()
	first := false
	switch {
	case t.stop != nil:
	case !ok:
		first = t.halt(fmt.Errorf("%w: claude api: the spend of the day of every process (%.2f USD) plus the worst case of the next call (%.4f USD) passes the cap of %.2f USD",
			ErrUnavailable, spent, cost, t.maxUSD), day.AddDate(0, 0, 1))
	case t.spentUSD+cost > t.maxUSD:
		first = t.halt(fmt.Errorf("%w: claude api: the spend of the day (%.2f USD) plus the worst case of the next call (%.4f USD) passes the cap of %.2f USD",
			ErrUnavailable, t.spentUSD, cost, t.maxUSD), day.AddDate(0, 0, 1))
	}
	stop = t.stop
	if stop == nil {
		t.spentUSD += cost
	}
	t.mu.Unlock()
	if stop != nil && ok {
		if l := spendLedger(); l != nil {
			t.settleLedger(l, day, -cost) // reserved in the ledger, not sent
		}
	}
	if first && OnCreditError != nil {
		OnCreditError("claude-api", stop.Error())
	}
	if stop != nil {
		return day, stop
	}
	return day, nil
}

// settle replaces the reservation of a call made on day with its real cost.
func (t *ClaudeAPIEndpoint) settle(day time.Time, reserved, cost float64) {
	t.mu.Lock()
	t.spentUSD += cost - reserved
	t.mu.Unlock()
	if l := spendLedger(); l != nil {
		t.settleLedger(l, day, cost-reserved)
		if pl, ok := l.(PhaseLedger); ok && cost > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), claudeLedgerTimeout)
			defer cancel()
			if err := pl.AddPhase(ctx, day, currentSpendPhase(), cost); err != nil {
				log.Warn().Err(err).Msg("claude api: the spend per phase missed a call; the cap is not affected")
			}
		}
	}
}

// settleLedger writes delta to the ledger, even when the call was cancelled.
// A failed write leaves the reservation in the ledger: the cap then counts
// the worst case, never less than the spend.
func (t *ClaudeAPIEndpoint) settleLedger(l SpendLedger, day time.Time, delta float64) {
	ctx, cancel := context.WithTimeout(context.Background(), claudeLedgerTimeout)
	defer cancel()
	if err := l.Settle(ctx, day, delta); err != nil && delta < 0 {
		log.Warn().Err(err).Float64("delta_usd", delta).Msg("claude api: the spend ledger keeps the reservation of a call")
	} else if err != nil {
		log.Error().Err(err).Float64("delta_usd", delta).Msg("claude api: the spend ledger missed a cost above the reservation")
	}
}

// rollDay resets the spend and the stop when the UTC day changed. The caller
// holds t.mu.
func (t *ClaudeAPIEndpoint) rollDay() {
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	day := now().UTC().Truncate(24 * time.Hour)
	if day.Equal(t.day) {
		return
	}
	if !t.day.IsZero() {
		log.Info().Float64("spent_usd", t.spentUSD).Bool("was_stopped", t.stop != nil).
			Msg("claude api: a new day; the spend and the stop of the endpoint are reset")
	}
	t.day, t.spentUSD, t.stop = day, 0, nil
}

// halt stops the endpoint with err until retryAt (zero: not known) and logs
// it once. The stop ends at the latest with the day (rollDay). It reports
// whether this call stopped the endpoint. The caller holds t.mu.
func (t *ClaudeAPIEndpoint) halt(err error, retryAt time.Time) bool {
	if t.stop != nil {
		return false
	}
	t.stop = &UnavailableError{Endpoint: EndpointClaudeAPI, RetryAt: retryAt, Err: err}
	log.Error().Err(err).Float64("spent_usd", t.spentUSD).Time("retry_at", retryAt).
		Msg("claude api: endpoint stopped for the rest of the day")
	return true
}

// callError returns the error of a failed request. See Call.
func (t *ClaudeAPIEndpoint) callError(ctx context.Context, err error) error {
	var ce *ClientError
	isClient := errors.As(err, &ce)
	switch {
	case errors.Is(err, errClaudeCredit):
	case isClient && (ce.StatusCode == http.StatusUnauthorized || ce.StatusCode == http.StatusForbidden ||
		ce.StatusCode == http.StatusNotFound):
		err = fmt.Errorf("the API refuses the key or the URL: %w", err)
	case ctx.Err() != nil || isClient:
		return err
	default:
		return &UnavailableError{
			Endpoint: EndpointClaudeAPI,
			RetryAt:  time.Now().Add(claudeRetryDelay),
			Err:      fmt.Errorf("%w: claude api: %w", ErrUnavailable, err),
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// No credit: the reset time is not known; the day's end clears the stop
	// and the next call probes the account again.
	t.halt(fmt.Errorf("%w: claude api: %w", ErrUnavailable, err), time.Time{})
	return t.stop
}

// withPrefix returns creq with a cache breakpoint at the end of the learned
// static prefix of its prompt family, or creq unchanged when the family has
// no prefix yet. The cache holds the system blocks and the prefix.
func (t *ClaudeAPIEndpoint) withPrefix(task Task, creq ClaudeRequest) (ClaudeRequest, error) {
	family, text, ok := promptFamily(task, creq)
	if !ok {
		return creq, nil
	}
	t.mu.Lock()
	p := t.prefixes.next(family, text)
	t.mu.Unlock()
	if p == "" {
		return creq, nil
	}
	return creq.withCacheBreakpoint(len(p), claudeAPICacheTTL)
}

// UsageReport returns one line per task and model: calls, failures, tokens
// and cost.
func (t *ClaudeAPIEndpoint) UsageReport() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return usageReport(t.usage, true, fmt.Sprintf("list price; cap %.2f USD per day", t.maxUSD))
}

// ContextTokens is the context of one Claude call.
// Steady implements steadyEndpoint: a judge asks Claude once.
func (*ClaudeAPIEndpoint) Steady() bool { return true }

func (*ClaudeAPIEndpoint) ContextTokens() int { return claudeContextTokens }
