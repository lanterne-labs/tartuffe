package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ClaudeAPIEndpoint answers each call with one synchronous POST /v1/messages
// on the Claude API, with the key of ANTHROPIC_API_KEY. The claude-team
// profile uses it as the fallback of the seat: the calls go to the API when
// the seat reaches its usage limit. The API bills each call at the list
// price, so the endpoint stops when the spend of the process reaches its cap.
type ClaudeAPIEndpoint struct {
	api    claudeHTTP
	maxUSD float64

	mu       sync.Mutex
	usage    map[usageKey]*TaskUsage
	prefixes prefixCache
	// spentUSD is the cost of the replies plus the reserved worst case of the
	// calls in flight and of the calls that the API can have billed without
	// a reply (a timeout, a transport error).
	spentUSD float64
	stop     *UnavailableError // set when the endpoint stops for the rest of the process
}

// claudeAPIRunCapUSD is the spend cap of the API endpoint in one process (one
// daily run). A sync call costs about 0.01 USD on Sonnet 5.5 and 0.02 USD on
// Opus 5.5 (the batch pilot of 2026-10-10: 0.01 USD per occurrence pair on
// Opus at half price). A full nightly makes about 2,000 calls, about 30 USD on
// the API. The seat serves the first part of the night, so 25 USD lets the
// overflow finish most nights. A bad night (a loop, a prompt that grows)
// costs 25 USD at most, and the next night resumes the work.
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

// NewClaudeAPIEndpointFromEnv reads ANTHROPIC_API_KEY. The spend cap is
// claudeAPIRunCapUSD.
func NewClaudeAPIEndpointFromEnv() (*ClaudeAPIEndpoint, error) {
	h, err := newClaudeHTTPFromEnv(claudeAPITimeout)
	if err != nil {
		return nil, fmt.Errorf("claude api endpoint: %w", err)
	}
	return &ClaudeAPIEndpoint{api: h, maxUSD: claudeAPIRunCapUSD}, nil
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
	if err := t.admit(reserve); err != nil {
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
			t.settle(reserve, 0) // the API rejected the request: no bill
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
	t.settle(reserve, cost)
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

// admit reserves cost for a call, or returns the stop error of the endpoint.
// It stops the endpoint when the reservation would pass the cap. The first
// stop by the cap calls OnCreditError, as an account without credit does.
func (t *ClaudeAPIEndpoint) admit(cost float64) error {
	t.mu.Lock()
	first := false
	if t.stop == nil && t.spentUSD+cost > t.maxUSD {
		first = t.halt(fmt.Errorf("%w: claude api: the spend of this run (%.2f USD) plus the worst case of the next call (%.4f USD) passes the cap of %.2f USD",
			ErrUnavailable, t.spentUSD, cost, t.maxUSD))
	}
	stop := t.stop
	if stop == nil {
		t.spentUSD += cost
	}
	t.mu.Unlock()
	if first && OnCreditError != nil {
		OnCreditError("claude-api", stop.Error())
	}
	if stop != nil {
		return stop
	}
	return nil
}

// settle replaces the reservation of a call with its real cost.
func (t *ClaudeAPIEndpoint) settle(reserved, cost float64) {
	t.mu.Lock()
	t.spentUSD += cost - reserved
	t.mu.Unlock()
}

// halt stops the endpoint with err for the rest of the process and logs it
// once. It reports whether this call stopped the endpoint. The caller holds
// t.mu.
func (t *ClaudeAPIEndpoint) halt(err error) bool {
	if t.stop != nil {
		return false
	}
	t.stop = &UnavailableError{Endpoint: EndpointClaudeAPI, Err: err}
	log.Error().Err(err).Float64("spent_usd", t.spentUSD).
		Msg("claude api: endpoint stopped for the rest of the run; the next run resumes the work")
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
	t.halt(fmt.Errorf("%w: claude api: %w", ErrUnavailable, err))
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
	return usageReport(t.usage, true, fmt.Sprintf("list price; cap %.2f USD per run", t.maxUSD))
}
