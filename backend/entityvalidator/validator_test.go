package entityvalidator

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- WikiFilter (uses httptest, no network) ---

// fakeWikiTransport intercepts MediaWiki "generator=search+prop=extracts" calls
// and returns canned page lists. Maps are keyed by gsrsearch (the query).
type fakeWikiTransport struct {
	pagesByQuery map[string][]struct {
		Title, Extract string
	}
}

func (f *fakeWikiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	q := req.URL.Query()
	srch := q.Get("gsrsearch")
	pages := f.pagesByQuery[srch]

	type p struct {
		PageID  int    `json:"pageid"`
		Index   int    `json:"index"`
		Title   string `json:"title"`
		Extract string `json:"extract"`
	}
	arr := make([]p, 0, len(pages))
	for i, hit := range pages {
		arr = append(arr, p{PageID: i + 1, Index: i + 1, Title: hit.Title, Extract: hit.Extract})
	}
	body, _ := json.Marshal(map[string]any{
		"query": map[string]any{"pages": arr},
	})
	return &http.Response{
		StatusCode: 200,
		Body:       &closingReader{r: strings.NewReader(string(body))},
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

type closingReader struct{ r *strings.Reader }

func (c *closingReader) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *closingReader) Close() error               { return nil }

func newFakeWikiFilter() (*wikiFilter, *fakeWikiTransport) {
	tr := &fakeWikiTransport{pagesByQuery: map[string][]struct{ Title, Extract string }{}}
	w := newWikiFilter()
	w.HTTP = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	return w, tr
}

func TestWiki_Defers_When_NoPagesFound_AttachesEmptyExtra(t *testing.T) {
	w, _ := newFakeWikiFilter()
	res, err := w.Check(context.Background(), Candidate{
		Name:       "entourage d'emmanuel macron",
		EntityType: "organization",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wiki no longer auto-rejects; it always Defers with whatever evidence it
	// has so the LLM (with Google evidence too) can decide.
	if res.Decision != Defer {
		t.Fatalf("want Defer, got %s (reason=%q)", res.Decision, res.Reason)
	}
	if !strings.Contains(res.Reason, "no matching page") {
		t.Fatalf("reason %q does not mention 'no matching page'", res.Reason)
	}
	// Even with zero pages, Extra must be valid JSON so the LLM gets a clean
	// (empty) wiki_pages array rather than the raw nil.
	var w2 wikiExtra
	if err := json.Unmarshal(res.Extra, &w2); err != nil {
		t.Fatalf("Extra not valid JSON: %v", err)
	}
	if len(w2.Pages) != 0 {
		t.Fatalf("expected empty pages list, got %d", len(w2.Pages))
	}
}

func TestWiki_Defers_With_PagesAttached_AsJSON(t *testing.T) {
	w, tr := newFakeWikiFilter()
	tr.pagesByQuery["Emmanuel Macron"] = []struct{ Title, Extract string }{
		{Title: "Emmanuel Macron", Extract: "Emmanuel Macron est un homme d'État français, président de la République depuis 2017."},
		{Title: "Brigitte Macron", Extract: "Brigitte Macron est une femme politique française, épouse d'Emmanuel Macron."},
	}
	res, err := w.Check(context.Background(), Candidate{Name: "Emmanuel Macron", EntityType: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != Defer {
		t.Fatalf("want Defer (with snippets), got %s", res.Decision)
	}
	if len(res.Extra) == 0 {
		t.Fatalf("expected Extra payload with wiki snippets")
	}
	var w2 wikiExtra
	if err := json.Unmarshal(res.Extra, &w2); err != nil {
		t.Fatalf("Extra not valid JSON: %v", err)
	}
	if len(w2.Pages) != 2 || w2.Pages[0].Title != "Emmanuel Macron" {
		t.Fatalf("unexpected pages: %+v", w2.Pages)
	}
	if !strings.Contains(w2.Pages[0].Extract, "président") {
		t.Fatalf("extract didn't round-trip: %q", w2.Pages[0].Extract)
	}
}

func TestWiki_StripsParenthetical_AsFallbackQuery(t *testing.T) {
	w, tr := newFakeWikiFilter()
	// Original query returns nothing; the stripped form hits.
	tr.pagesByQuery["norwegian refugee council (nrc)"] = nil
	tr.pagesByQuery["norwegian refugee council"] = []struct{ Title, Extract string }{
		{Title: "Norwegian Refugee Council", Extract: "The Norwegian Refugee Council is an independent humanitarian organization."},
	}
	res, err := w.Check(context.Background(), Candidate{Name: "norwegian refugee council (nrc)", EntityType: "organization"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != Defer {
		t.Fatalf("want Defer after parens-stripped fallback, got %s", res.Decision)
	}
	if len(res.Extra) == 0 {
		t.Fatalf("expected Extra payload")
	}
}

func TestWiki_DropsDisambiguationStubs(t *testing.T) {
	w, tr := newFakeWikiFilter()
	tr.pagesByQuery["paris"] = []struct{ Title, Extract string }{
		{Title: "Paris (homonymie)", Extract: "Paris peut faire référence à plusieurs choses…"},
	}
	res, err := w.Check(context.Background(), Candidate{Name: "paris", EntityType: "organization"})
	if err != nil {
		t.Fatal(err)
	}
	// Disambig page is filtered out of pages list; result is Defer (LLM decides
	// based on combined wiki + google evidence).
	if res.Decision != Defer {
		t.Fatalf("disambig-only should Defer (LLM decides), got %s", res.Decision)
	}
	var w2 wikiExtra
	_ = json.Unmarshal(res.Extra, &w2)
	if len(w2.Pages) != 0 {
		t.Fatalf("disambig page should be filtered out of Extra, got %d pages", len(w2.Pages))
	}
}

// --- Validator pipeline orchestration ---

type fixedFilter struct {
	name string
	res  FilterResult
	err  error
	hit  *int // counter for "was this filter called?"
}

func (s *fixedFilter) Name() string { return s.name }
func (s *fixedFilter) Check(_ context.Context, _ Candidate) (FilterResult, error) {
	if s.hit != nil {
		*s.hit++
	}
	return s.res, s.err
}

func TestValidator_ShortCircuits_OnFirstApprove(t *testing.T) {
	hit2 := 0
	v := New(
		&fixedFilter{name: "first", res: FilterResult{Decision: Approve, Reason: "ok"}},
		&fixedFilter{name: "second", res: FilterResult{Decision: Reject, Reason: "x"}, hit: &hit2},
	)
	verdict := v.Validate(context.Background(), "anything", "human")
	if !verdict.Real || verdict.Filter != "first" {
		t.Fatalf("unexpected verdict %+v", verdict)
	}
	if hit2 != 0 {
		t.Fatalf("second filter should not have been called")
	}
}

func TestValidator_ShortCircuits_OnFirstReject(t *testing.T) {
	hit2 := 0
	v := New(
		&fixedFilter{name: "first", res: FilterResult{Decision: Reject, Reason: "junk"}},
		&fixedFilter{name: "second", res: FilterResult{Decision: Approve, Reason: "ok"}, hit: &hit2},
	)
	verdict := v.Validate(context.Background(), "anything", "human")
	if verdict.Real || verdict.Filter != "first" {
		t.Fatalf("unexpected verdict %+v", verdict)
	}
	if hit2 != 0 {
		t.Fatalf("second filter should not have been called")
	}
}

func TestValidator_Defer_FallsThrough_ToDefault(t *testing.T) {
	v := New(
		&fixedFilter{name: "first", res: FilterResult{Decision: Defer}},
		&fixedFilter{name: "second", res: FilterResult{Decision: Defer}},
	)
	v.DefaultDecision = Approve
	verdict := v.Validate(context.Background(), "anything", "human")
	if !verdict.Real || verdict.Filter != "default" {
		t.Fatalf("unexpected verdict %+v", verdict)
	}

	v.DefaultDecision = Reject
	verdict = v.Validate(context.Background(), "anything", "human")
	if verdict.Real || verdict.Filter != "default" {
		t.Fatalf("unexpected verdict %+v", verdict)
	}
}

func TestValidator_FilterError_TreatedAsDefer(t *testing.T) {
	hit2 := 0
	v := New(
		&fixedFilter{name: "first", res: FilterResult{Decision: Defer}, err: errTest("network down")},
		&fixedFilter{name: "second", res: FilterResult{Decision: Approve, Reason: "ok"}, hit: &hit2},
	)
	verdict := v.Validate(context.Background(), "x", "human")
	if !verdict.Real || verdict.Filter != "second" {
		t.Fatalf("unexpected verdict %+v", verdict)
	}
	if hit2 != 1 {
		t.Fatalf("second filter should have been called once, got %d", hit2)
	}
}

// inspectingFilter records the Candidate it was called with so we can assert
// the earlier filter's results were threaded through.
type inspectingFilter struct {
	name string
	res  FilterResult
	got  Candidate
}

func (i *inspectingFilter) Name() string { return i.name }
func (i *inspectingFilter) Check(_ context.Context, c Candidate) (FilterResult, error) {
	i.got = c
	return i.res, nil
}

func TestValidator_PropagatesPreviousResults_ToDownstream(t *testing.T) {
	wikiExtraBytes, _ := json.Marshal(wikiExtra{Pages: []WikiPage{
		{Lang: "fr", Title: "Emmanuel Macron", Extract: "Président de la République."},
	}})
	wiki := &fixedFilter{
		name: "wiki",
		res:  FilterResult{Decision: Defer, Reason: "wiki: 1 page found", Extra: wikiExtraBytes},
	}
	inspector := &inspectingFilter{
		name: "llm",
		res:  FilterResult{Decision: Approve, Reason: "llm: matched"},
	}
	v := New(wiki, inspector)
	v.Validate(context.Background(), "Emmanuel Macron", "human")

	if got := len(inspector.got.Previous); got != 1 {
		t.Fatalf("downstream got %d previous, want 1", got)
	}
	prev := inspector.got.Previous[0]
	if prev.Filter != "wiki" || prev.Result.Decision != Defer {
		t.Fatalf("unexpected propagated run: %+v", prev)
	}
	// Spot-check by-name lookup
	got, ok := inspector.got.PrevByName("wiki")
	if !ok {
		t.Fatalf("PrevByName missed wiki run")
	}
	var w wikiExtra
	if err := json.Unmarshal(got.Result.Extra, &w); err != nil {
		t.Fatalf("propagated Extra wasn't valid JSON: %v", err)
	}
	if len(w.Pages) != 1 || w.Pages[0].Title != "Emmanuel Macron" {
		t.Fatalf("propagated wiki extra wrong: %+v", w)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
