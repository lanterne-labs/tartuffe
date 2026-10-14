package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBatchAPI mimics the Message Batches API. A created batch ends after
// pollsToEnd reads of its status. Every request succeeds with the reply text
// "ok:<custom_id>", except the custom ids in errored.
type fakeBatchAPI struct {
	t          *testing.T
	srv        *httptest.Server
	mu         sync.Mutex
	pollsToEnd int
	batches    map[string][]ClaudeBatchItem
	polls      map[string]int
	creates    int
	errored    map[string]bool
	// failNext answers the next requests with these statuses first.
	failNext []int
}

func newFakeBatchAPI(t *testing.T) *fakeBatchAPI {
	f := &fakeBatchAPI{t: t, pollsToEnd: 1, batches: map[string][]ClaudeBatchItem{}, polls: map[string]int{}, errored: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBatchAPI) client() *ClaudeBatchClient {
	return &ClaudeBatchClient{claudeHTTP{httpClient: &http.Client{Timeout: 2 * time.Second}, baseURL: f.srv.URL, key: "k"}}
}

func (f *fakeBatchAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != claudeAPIVersion {
		f.t.Errorf("missing auth headers on %s", r.URL.Path)
	}
	if len(f.failNext) > 0 {
		status := f.failNext[0]
		f.failNext = f.failNext[1:]
		w.Header().Set("retry-after", "0")
		w.WriteHeader(status)
		body := `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`
		if status == http.StatusTooManyRequests {
			body = `{"type":"error","error":{"type":"rate_limit_error","details":{"error_code":"enforced_spend_limit_reached"}}}`
		}
		_, _ = io.WriteString(w, body)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/messages/batches")
	switch {
	case r.Method == http.MethodPost && path == "":
		var body struct {
			Requests []ClaudeBatchItem `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("decode create: %v", err)
		}
		f.creates++
		id := fmt.Sprintf("msgbatch_%d", f.creates)
		f.batches[id] = body.Requests
		f.writeBatch(w, id, false)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/results"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/results")
		for _, it := range f.batches[id] {
			var line string
			if f.errored[it.CustomID] {
				line = fmt.Sprintf(`{"custom_id":%q,"result":{"type":"errored","error":{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}}}`, it.CustomID)
			} else {
				line = fmt.Sprintf(`{"custom_id":%q,"result":{"type":"succeeded","message":{"id":"msg","model":%q,"stop_reason":"end_turn","content":[{"type":"text","text":"ok:%s"}],"usage":{"input_tokens":1000,"output_tokens":100}}}}`, it.CustomID, it.Params.Model, it.CustomID)
			}
			_, _ = io.WriteString(w, line+"\n")
		}
	case r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/")
		f.polls[id]++
		f.writeBatch(w, id, f.polls[id] >= f.pollsToEnd)
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}
}

func (f *fakeBatchAPI) writeBatch(w http.ResponseWriter, id string, ended bool) {
	b := map[string]any{"id": id, "processing_status": "in_progress", "created_at": time.Now(), "request_counts": map[string]int{"processing": len(f.batches[id])}}
	if ended {
		b["processing_status"] = "ended"
		b["results_url"] = f.srv.URL + "/messages/batches/" + id + "/results"
		b["request_counts"] = map[string]int{"succeeded": len(f.batches[id])}
	}
	_ = json.NewEncoder(w).Encode(b)
}

func testJobs(keys ...string) []ClaudeJob {
	var jobs []ClaudeJob
	for _, k := range keys {
		req, _ := NewClaudeRequest(ChatRequest{MaxTokens: 100, Messages: []ChatMessage{{Role: "user", Content: "same prompt"}}},
			ClaudeOptions{Model: "claude-haiku-5-5", Effort: EffortLow})
		jobs = append(jobs, ClaudeJob{Key: k, Request: req})
	}
	return jobs
}

func newTestRunner(f *fakeBatchAPI, dir string) *ClaudeBatchRunner {
	return &ClaudeBatchRunner{Client: f.client(), Dir: dir, MaxUSD: 1, PollInterval: time.Millisecond}
}

func TestClaudeBatchRunner_RunsOnceThenReadsTheStore(t *testing.T) {
	f := newFakeBatchAPI(t)
	f.pollsToEnd = 3
	r := newTestRunner(f, t.TempDir())
	// The three votes share one prompt: their keys keep them apart.
	jobs := testJobs("case1/vote0", "case1/vote1", "case1/vote2")
	got, err := r.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	for _, j := range jobs {
		res := got[j.Key]
		if res.Err != nil {
			t.Fatalf("%s: %+v", j.Key, res)
		}
		text, err := res.Reply.Text(false)
		if err != nil {
			t.Fatal(err)
		}
		texts[text] = true
		if want := (1000*0.10 + 100*0.50) / 1e6 / 2; res.CostUSD != want {
			t.Errorf("%s: cost %g, want %g", j.Key, res.CostUSD, want)
		}
	}
	if len(texts) != 3 || f.creates != 1 {
		t.Fatalf("want 3 distinct replies from 1 batch, got %d replies from %d batches", len(texts), f.creates)
	}

	again, err := r.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatalf("a second run must read the store, but it created batch %d", f.creates)
	}
	for _, j := range jobs {
		if again[j.Key].Err != nil || again[j.Key].Reply.Content[0].Text != got[j.Key].Reply.Content[0].Text {
			t.Fatalf("%s: stored reply differs: %+v", j.Key, again[j.Key])
		}
	}
}

func TestClaudeBatchRunner_ErroredJobIsSentAgain(t *testing.T) {
	f := newFakeBatchAPI(t)
	r := newTestRunner(f, t.TempDir())
	jobs := testJobs("a", "b")
	idB, _ := claudeCustomID(jobs[1])
	f.errored[idB] = true
	got, err := r.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].Err != nil || got["b"].Err == nil {
		t.Fatalf("want a ok and b errored, got %+v", got)
	}
	f.errored[idB] = false
	got, err = r.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].Err != nil || got["b"].Err != nil || len(f.batches["msgbatch_2"]) != 1 || f.batches["msgbatch_2"][0].CustomID != idB {
		t.Fatalf("second run must send only b, got %+v and batch %v", got, f.batches["msgbatch_2"])
	}
}

func TestClaudeBatchRunner_CollectsTheBatchOfAStoppedRun(t *testing.T) {
	f := newFakeBatchAPI(t)
	f.pollsToEnd = 1000
	dir := t.TempDir()
	r := newTestRunner(f, dir)
	jobs := testJobs("a")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, jobs); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the run cut by its context, got %v", err)
	}
	if pending, _ := filepath.Glob(filepath.Join(dir, "batches", "*.json")); len(pending) != 1 {
		t.Fatalf("want 1 pending batch file, got %v", pending)
	}
	// A poll of the cut run can still be in the handler: change the fake
	// under its lock.
	f.mu.Lock()
	f.pollsToEnd = 0
	f.mu.Unlock()
	got, err := r.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || got["a"].Err != nil {
		t.Fatalf("the next run must collect the pending batch, not submit again: creates=%d result=%+v", f.creates, got["a"])
	}
	if pending, _ := filepath.Glob(filepath.Join(dir, "batches", "*.json")); len(pending) != 0 {
		t.Fatalf("pending file not removed: %v", pending)
	}
}

func TestClaudeBatchRunner_SpendCap(t *testing.T) {
	f := newFakeBatchAPI(t)
	r := newTestRunner(f, t.TempDir())
	r.MaxUSD = 0
	if _, err := r.Run(context.Background(), testJobs("a")); err == nil || !strings.Contains(err.Error(), "spend cap") {
		t.Fatalf("want the spend cap to refuse, got %v", err)
	}
	if f.creates != 0 {
		t.Fatal("nothing may be submitted over the cap")
	}
}

func TestClaudeBatchRunner_DuplicateKey(t *testing.T) {
	r := newTestRunner(newFakeBatchAPI(t), t.TempDir())
	if _, err := r.Run(context.Background(), testJobs("a", "a")); err == nil {
		t.Fatal("want an error on a duplicate key")
	}
}

func TestClaudeBatchClient_RetriesOverloadAndStopsOnSpendLimit(t *testing.T) {
	f := newFakeBatchAPI(t)
	f.failNext = []int{529, 500}
	c := f.client()
	if _, err := c.Create(context.Background(), []ClaudeBatchItem{{CustomID: "x", Params: testJobs("x")[0].Request}}); err != nil {
		t.Fatalf("529 and 500 must be retried: %v", err)
	}

	var credit string
	OnCreditError = func(provider, msg string) { credit = provider }
	t.Cleanup(func() { OnCreditError = nil })
	f.failNext = []int{http.StatusTooManyRequests}
	_, err := c.Get(context.Background(), "msgbatch_1")
	var ce *ClientError
	if !errors.As(err, &ce) || credit != "claude" {
		t.Fatalf("the spend-limit 429 must be a permanent ClientError with a credit alert, got %v (alert %q)", err, credit)
	}

	f.failNext = []int{http.StatusBadRequest}
	if _, err := c.Get(context.Background(), "msgbatch_1"); !errors.As(err, &ce) || ce.StatusCode != 400 {
		t.Fatalf("a 400 must not be retried, got %v", err)
	}
}

func TestParseClaudeBatchResults(t *testing.T) {
	body := `{"custom_id":"a","result":{"type":"succeeded","message":{"id":"m","model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":7,"output_tokens_details":{"thinking_tokens":1}}}}}
{"custom_id":"b","result":{"type":"errored","error":{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}}}

{"custom_id":"c","result":{"type":"expired"}}
`
	got, err := parseClaudeBatchResults([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Message.Usage.CacheReadInputTokens != 7 || got[0].Message.Usage.OutputTokensDetails.ThinkingTokens != 1 ||
		!strings.Contains(got[1].Error, "invalid_request_error") || got[2].Type != "expired" {
		t.Fatalf("parsed %+v", got)
	}
	if _, err := parseClaudeBatchResults([]byte(`{"custom_id":"a","result":{"type":"new"}}`)); err == nil {
		t.Fatal("an unknown result type must fail")
	}
}

func TestClaudeBatchClient_NeedsTheKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := NewClaudeBatchClientFromEnv(); err == nil {
		t.Fatal("want an error without ANTHROPIC_API_KEY")
	}
}

func TestClaudeBatchEndpoint_ConcurrentCallsShareOneBatch(t *testing.T) {
	f := newFakeBatchAPI(t)
	tr := &ClaudeBatchEndpoint{
		Runner:   newTestRunner(f, t.TempDir()),
		Idle:     20 * time.Millisecond,
		CacheTTL: "off",
	}
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
	restore, err := SetRouting(ClaudeRouting(EndpointClaudeBatch, tr, "claude-haiku-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	c := Config{BaseURL: "http://unused.invalid", APIKey: "k", ChatModel: "local"}.NewClient("test")

	// Three identical calls (the votes of a judge) and one other call.
	prompts := []string{"vote", "vote", "vote", "other"}
	replies := make([]string, len(prompts))
	var wg sync.WaitGroup
	for i, p := range prompts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			content, resp, err := c.Chat(context.Background(), ChatRequest{
				MaxTokens: 50, Task: TaskDetect, Messages: []ChatMessage{{Role: "user", Content: p}},
			})
			if err != nil || resp.Usage.PromptTokens != 1000 {
				t.Errorf("call %d: %q %+v %v", i, content, resp, err)
			}
			replies[i] = content
		}()
	}
	wg.Wait()
	if f.creates != 1 || len(f.batches["msgbatch_1"]) != 4 {
		t.Fatalf("want 1 batch of 4 requests, got %d batches, first of %d", f.creates, len(f.batches["msgbatch_1"]))
	}
	distinct := map[string]bool{}
	for _, r := range replies {
		distinct[r] = true
	}
	if len(distinct) != 4 {
		t.Fatalf("each call must get its own reply, got %v", replies)
	}

	// A later call is a new round: a new batch.
	if _, _, err := c.Chat(context.Background(), ChatRequest{MaxTokens: 50, Task: TaskDetect, Messages: []ChatMessage{{Role: "user", Content: "vote"}}}); err != nil {
		t.Fatal(err)
	}
	if f.creates != 2 {
		t.Fatalf("the fourth send of a request must go in a new batch, got %d batches", f.creates)
	}
	if report := tr.UsageReport(); !strings.Contains(report, "detect          claude-haiku-5-5  calls=5 failed=0") {
		t.Fatalf("usage report:\n%s", report)
	}
}

func TestClaudeBatchEndpoint_CachesTheCommonPrefixOfAFamily(t *testing.T) {
	f := newFakeBatchAPI(t)
	tr := &ClaudeBatchEndpoint{
		Runner:   newTestRunner(f, t.TempDir()),
		Idle:     20 * time.Millisecond,
		CacheTTL: "5m",
	}
	rules := strings.Repeat("Rule: judge the pair.\n", 150) // 3,300 bytes
	call := func(prompts ...string) {
		var wg sync.WaitGroup
		for _, p := range prompts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, err := tr.Call(context.Background(), ChatRequest{MaxTokens: 10, Task: TaskVerify, Messages: []ChatMessage{{Role: "user", Content: p}}}, ClaudeOptions{Model: "claude-haiku-5-5"}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	breakpoint := func(batch string) []string {
		var out []string
		for _, it := range f.batches[batch] {
			blocks := it.Params.Messages[0].Content
			if len(blocks) != 2 || blocks[0].CacheControl == nil || blocks[0].CacheControl.TTL != "" {
				out = append(out, "none")
				continue
			}
			out = append(out, blocks[0].Text)
		}
		return out
	}

	// The two pairs share the rules and the start of "Pair: A".
	call(rules+"Pair: A1\nB1", rules+"Pair: A2\nB2", "short prompt")
	for _, got := range breakpoint("msgbatch_1") {
		if got != "none" && got != rules {
			t.Fatalf("the breakpoint must end the rules at a line end, got %d bytes", len(got))
		}
	}
	if n := strings.Count(strings.Join(breakpoint("msgbatch_1"), "|"), rules); n != 2 {
		t.Fatalf("want the 2 long prompts cached and the short one not, got %v", breakpoint("msgbatch_1"))
	}
	// A later batch with one call of the family reuses the prefix.
	call(rules + "Pair: A3\nB3")
	if got := breakpoint("msgbatch_2"); len(got) != 1 || got[0] != rules {
		t.Fatalf("a later batch must reuse the family prefix, got %v", got)
	}
	if report := tr.UsageReport(); !strings.Contains(report, "calls=4 failed=0 uncached=1") {
		t.Fatalf("usage report:\n%s", report)
	}
}
