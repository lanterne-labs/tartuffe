package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ClaudeBatchEndpoint sends calls through the Message Batches API. A
// call blocks until its reply arrives. The calls that arrive while no batch
// is open, or while one is in flight, go into the next batch: a batch opens
// when no new call came for Idle. So the callers must run concurrently to
// fill a batch, and a judge that calls again after a reply (a vote, the
// second order of a gate) waits one more batch.
type ClaudeBatchEndpoint struct {
	Runner *ClaudeBatchRunner
	Idle   time.Duration
	// CacheTTL is the TTL of the cache breakpoints: "5m", "1h" or "off".
	// In a batch, many requests can write the same prefix at the same time,
	// and a 1h write costs 2 times the input price (5m: 1.25 times), so the
	// pilot measures both.
	CacheTTL string

	mu       sync.Mutex
	queue    []*batchCall
	timer    *time.Timer
	sent     map[string]int // request hash -> times sent in this process
	prefixes prefixCache
	usage    map[usageKey]*TaskUsage
	running  sync.Mutex // one Runner.Run at a time: the store is not shared
}

type batchCall struct {
	job      ClaudeJob
	task     Task
	wantJSON bool
	done     chan batchReply
}

type batchReply struct {
	content string
	resp    *ChatResponse
	err     error
}

// usageKey is a line of a usage report: one task on one model.
type usageKey struct {
	task  Task
	model string
}

// TaskUsage sums the Claude usage of the calls of one task on one model.
type TaskUsage struct {
	Calls    int
	Failed   int
	Uncached int // calls sent without a cache breakpoint
	Usage    ClaudeUsage
	CostUSD  float64
}

// countTaskCall counts one call of task on model in usage and returns the
// entry of the pair. It makes the map and the entry when they do not exist.
// The caller holds the lock of usage.
func countTaskCall(usage *map[usageKey]*TaskUsage, task Task, model string) *TaskUsage {
	if *usage == nil {
		*usage = map[usageKey]*TaskUsage{}
	}
	key := usageKey{task: task, model: model}
	u := (*usage)[key]
	if u == nil {
		u = &TaskUsage{}
		(*usage)[key] = u
	}
	u.Calls++
	return u
}

// Call queues the call for the next batch and waits for its reply.
func (t *ClaudeBatchEndpoint) Call(ctx context.Context, req ChatRequest, opts ClaudeOptions) (string, *ChatResponse, error) {
	// A queued call is sent and billed: a caller that already gave up must
	// not queue one.
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	creq, err := NewClaudeRequest(req, opts)
	if err != nil {
		return "", nil, err
	}
	key, err := t.nextKey(creq)
	if err != nil {
		return "", nil, err
	}
	call := &batchCall{
		job:      ClaudeJob{Key: key, Request: creq},
		task:     req.Task,
		wantJSON: req.wantsJSON(),
		done:     make(chan batchReply, 1),
	}
	t.enqueue(call)
	select {
	case r := <-call.done:
		return r.content, r.resp, r.err
	case <-ctx.Done():
		// The batch goes on and its reply is stored: a rerun reads it.
		return "", nil, ctx.Err()
	}
}

// nextKey names the n-th send of one request in this process. The votes of a
// judge send the same request several times; the count keeps them apart, and
// a rerun of the same calls finds the same keys in the store. When two
// concurrent cases send one request, they can take #0 and #1 in another
// order on a rerun; a later vote of either then can miss the store and is
// sent again.
func (t *ClaudeBatchEndpoint) nextKey(creq ClaudeRequest) (string, error) {
	body, err := json.Marshal(creq)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:8])
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sent == nil {
		t.sent = map[string]int{}
	}
	n := t.sent[h]
	t.sent[h] = n + 1
	return fmt.Sprintf("%s#%d", h, n), nil
}

func (t *ClaudeBatchEndpoint) enqueue(c *batchCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queue = append(t.queue, c)
	if t.timer == nil {
		t.timer = time.AfterFunc(t.Idle, t.flush)
	} else {
		t.timer.Reset(t.Idle)
	}
}

// flush sends the queued calls as one batch and answers each call.
func (t *ClaudeBatchEndpoint) flush() {
	t.running.Lock()
	defer t.running.Unlock()
	t.mu.Lock()
	calls := t.queue
	t.queue = nil
	t.timer = nil
	t.mu.Unlock()
	if len(calls) == 0 {
		return
	}
	// A breakpoint error is local: it is not an outage of the API.
	err := t.placeBreakpoints(calls)
	if err != nil {
		err = fmt.Errorf("claude batch: %w", err)
	}
	var results map[string]ClaudeJobResult
	if err == nil {
		jobs := make([]ClaudeJob, len(calls))
		for i, c := range calls {
			jobs[i] = c.job
		}
		if results, err = t.Runner.Run(context.Background(), jobs); err != nil {
			// The batch API itself failed: no request was at fault.
			err = &UnavailableError{
				Endpoint: EndpointClaudeBatch,
				RetryAt:  time.Now().Add(claudeRetryDelay),
				Err:      fmt.Errorf("%w: claude batch: %w", ErrUnavailable, err),
			}
		}
	}
	for _, c := range calls {
		var r batchReply
		switch {
		case err != nil:
			r.err = err
		default:
			r = t.reply(c, results[c.job.Key])
		}
		c.done <- r
	}
}

func (t *ClaudeBatchEndpoint) reply(c *batchCall, res ClaudeJobResult) batchReply {
	t.mu.Lock()
	u := countTaskCall(&t.usage, c.task, c.job.Request.Model)
	if !c.job.Request.hasCacheBreakpoint() {
		u.Uncached++
	}
	t.mu.Unlock()
	fail := func(err error) batchReply {
		t.mu.Lock()
		u.Failed++
		t.mu.Unlock()
		return batchReply{err: err}
	}
	if res.Err != nil {
		return fail(res.Err)
	}
	t.mu.Lock()
	u.Usage = u.Usage.Add(res.Reply.Usage)
	u.CostUSD += res.CostUSD
	t.mu.Unlock()
	text, err := res.Reply.Text(c.wantJSON)
	if err != nil {
		return fail(err)
	}
	return batchReply{content: text, resp: claudeChatResponse(text, res.Reply.Usage)}
}

// UsageReport returns one line per task and model: calls, failures, tokens
// and cost.
func (t *ClaudeBatchEndpoint) UsageReport() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return usageReport(t.usage, true, "batch price; includes replies read from the store")
}

// usageReport returns one line per task and model, and a total line.
// uncached adds the count of calls sent without a cache breakpoint. costNote
// says what the cost is.
func usageReport(usage map[usageKey]*TaskUsage, uncached bool, costNote string) string {
	keys := make([]usageKey, 0, len(usage))
	for key := range usage {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].task != keys[j].task {
			return keys[i].task < keys[j].task
		}
		return keys[i].model < keys[j].model
	})
	var b strings.Builder
	var total float64
	for _, key := range keys {
		u := usage[key]
		total += u.CostUSD
		perCall := 0.0
		if ok := u.Calls - u.Failed; ok > 0 {
			perCall = u.CostUSD / float64(ok)
		}
		name := string(key.task)
		if name == "" {
			name = "(no task)"
		}
		fmt.Fprintf(&b, "%-15s %-17s calls=%d failed=%d ", name, key.model, u.Calls, u.Failed)
		if uncached {
			fmt.Fprintf(&b, "uncached=%d ", u.Uncached)
		}
		fmt.Fprintf(&b, "input=%d cache_write=%d cache_read=%d output=%d thinking=%d cost=$%.4f per_call=$%.5f\n",
			u.Usage.InputTokens, u.Usage.CacheCreationInputTokens, u.Usage.CacheReadInputTokens,
			u.Usage.OutputTokens, u.Usage.OutputTokensDetails.ThinkingTokens, u.CostUSD, perCall)
	}
	fmt.Fprintf(&b, "total cost=$%.4f (%s)\n", total, costNote)
	return b.String()
}

// Validate reports an endpoint that cannot run.
func (t *ClaudeBatchEndpoint) Validate() error {
	if t.Runner == nil || t.Runner.Client == nil {
		return errors.New("claude batch endpoint: no runner")
	}
	if t.Idle <= 0 {
		return errors.New("claude batch endpoint: Idle must be > 0")
	}
	switch t.CacheTTL {
	case "5m", "1h", "off":
	default:
		return fmt.Errorf("claude batch endpoint: CacheTTL %q is not 5m, 1h or off", t.CacheTTL)
	}
	return nil
}

// placeBreakpoints puts a cache breakpoint on each call at the end of the
// static prefix of its prompt family. The prefix is the longest common start
// of the prompts of the family, cut back to a line end. A family keeps its
// prefix across batches: a cache entry matches only at the same position.
func (t *ClaudeBatchEndpoint) placeBreakpoints(calls []*batchCall) error {
	if t.CacheTTL == "off" {
		return nil
	}
	ttl := ""
	if t.CacheTTL == "1h" {
		ttl = "1h"
	}
	families := map[string][]*batchCall{}
	var order []string
	for _, c := range calls {
		key, _, ok := promptFamily(c.task, c.job.Request)
		if !ok {
			continue
		}
		if _, seen := families[key]; !seen {
			order = append(order, key)
		}
		families[key] = append(families[key], c)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range order {
		var rest []*batchCall
		for _, c := range families[key] {
			ok, err := t.applyKnownPrefix(key, c, ttl)
			if err != nil {
				return err
			}
			if !ok {
				rest = append(rest, c)
			}
		}
		if len(rest) < 2 {
			continue
		}
		texts := make([]string, len(rest))
		for i, c := range rest {
			_, texts[i], _ = c.job.Request.firstUserText()
		}
		if t.prefixes.learn(key, texts) == "" {
			continue
		}
		for _, c := range rest {
			if _, err := t.applyKnownPrefix(key, c, ttl); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyKnownPrefix puts the breakpoint of the longest known prefix of the
// family that starts the prompt of c. The caller holds t.mu.
func (t *ClaudeBatchEndpoint) applyKnownPrefix(family string, c *batchCall, ttl string) (bool, error) {
	_, text, _ := c.job.Request.firstUserText()
	best := t.prefixes.known(family, text)
	if best == "" {
		return false, nil
	}
	req, err := c.job.Request.withCacheBreakpoint(len(best), ttl)
	if err != nil {
		return false, err
	}
	c.job.Request = req
	return true, nil
}

// ContextTokens is the context of one Claude call.
func (*ClaudeBatchEndpoint) ContextTokens() int { return claudeContextTokens }
