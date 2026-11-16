// Package statementday finds the exact day of a statement the site knows
// only to the year or the month ("en 2013, elle réclamait…"): the article
// that recalls it gives no more, but the web often does (the interview, the
// vote, the tweet of that day). It searches the web for the statement inside
// the known period and keeps a day only when a result states it in words the
// code finds in that result. A day that cannot be shown stays unknown: the
// statement keeps its year or its month.
package statementday

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/search"
)

// Statement is a statement or act known to the year or the month.
type Statement struct {
	Person string
	Text   string
	Quote  string
	// Known is the statement date with its precision (backend.WithDatePrecision).
	Known time.Time
	// SourceDate is the date of the source that reports it: the statement is
	// never later.
	SourceDate *time.Time
}

// Found is the result of a search: Day is nil when no result gives the day.
type Found struct {
	Day       *time.Time
	URL       string // the result that gives the day
	Evidence  string // its words that give it
	Reasoning string
}

// Searcher is the web search the finder asks (search.WebSearcher).
type Searcher interface {
	SearchWeb(query string, limit int) ([]search.SearchResult, error)
}

// Finder finds the day of a statement.
type Finder struct {
	search Searcher
	chat   func(ctx context.Context, req llm.ChatRequest) (string, error)
}

// NewFinder builds a finder on a searcher and an LLM client.
func NewFinder(s Searcher, client *llm.Client) *Finder {
	return &Finder{search: s, chat: func(ctx context.Context, req llm.ChatRequest) (string, error) {
		content, _, err := client.Chat(ctx, req)
		return content, err
	}}
}

// NewFinderFromEnv builds the finder on the configured search chain and LLM
// endpoint.
func NewFinderFromEnv() (*Finder, error) {
	s, err := search.NewWebSearcherFromEnv()
	if err != nil {
		return nil, err
	}
	client, err := llm.NewClientFromEnv("statement-day")
	if err != nil {
		return nil, err
	}
	return NewFinder(s, client), nil
}

const (
	queriesMax       = 2
	resultsPerQuery  = 8
	resultsToTheLLM  = 14
	evidenceMinRunes = 6
)

// Format args: the person, the period, the fact, the quote.
const queryPrompt = `A fact-checking site knows that a politician said or did something in a given year or month, and looks for the exact day on the web.

Person: %s
Known period: %s
Fact: %s
Quote of the source: %s

Write %d web search queries, in the language of the fact, that would find a page of that time reporting this very statement or act: the person's name, the few words that are specific to it (the subject, the medium or the occasion: an interview, a vote, a meeting, a tweet), and the year (and the month when known). No quotation marks, no operators, at most 12 words each.

Return ONLY strict JSON:
{"queries": ["...", "..."]}`

// Format args: the person, the period, the source date, the fact, the quote,
// the results.
const dayPrompt = `A fact-checking site knows that a politician said or did something in a given year or month. Below are web search results. Find the exact DAY of that statement or act, if a result gives it.

Person: %s
Known period: %s
The source that recalls it was published on: %s
Fact: %s
Quote of the source: %s

SEARCH RESULTS (each with its publication date when the search engine gives one):
%s

Give a day only when ONE result shows it for THIS VERY statement or act (the same words, vote, interview or occasion, not another statement on the same subject), in one of two ways:
- "stated": the title or the snippet of the result writes the day out ("le 7 décembre 2021, le Sénat a rejeté…", "Samedi 29 août, aux journées d'été du PS"). "evidence" is then those words, copied WORD FOR WORD from the result, with the number of the day in them.
- "published": the result IS the statement or reports it as the news of its own days (the interview, the tweet, the video, the report of the vote), and it was published in the known period. The day is its publication date, or the day its words name against that date ("a déclaré lundi soir" in a result published on a Tuesday is the day before: the weekday of each publication date is given, do not compute it yourself). At most six days before the publication.
A politician speaks on one subject many times. When the fact gives the person's own words between quotation marks and the result quotes OTHER words of theirs on the same subject, it is another statement, unless the result names the same occasion as the fact (the same programme, interview, meeting or vote): answer null.
A result published later that only recalls the statement does not date it. A day outside the known period is wrong. When no result gives the day, or you hesitate between two days, answer null: an honest year is better than a wrong day. Never give a day from what you know yourself.

Return ONLY strict JSON, a short reasoning first:
{"reasoning": "<one sentence: which result gives the day and how, or that none does>", "result": <the number of the result, or null>, "how": "stated" | "published" | null, "evidence": "<the words of the result that write the day out, verbatim>" | null, "day": "YYYY-MM-DD" | null}`

type queryReply struct {
	Queries []string `json:"queries"`
}

type dayReply struct {
	Reasoning string  `json:"reasoning"`
	Result    *int    `json:"result"`
	How       *string `json:"how"`
	Evidence  *string `json:"evidence"`
	Day       *string `json:"day"`
}

// Find searches the web for the statement and returns its day when a result
// gives it.
func (f *Finder) Find(ctx context.Context, st Statement) (Found, error) {
	precision := backend.DatePrecisionOf(st.Known)
	if precision != backend.DatePrecisionYear && precision != backend.DatePrecisionMonth {
		return Found{}, fmt.Errorf("statement day: the statement date is not a year or a month")
	}
	period := backend.FormatFactDate(&st.Known)

	content, err := f.chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: fmt.Sprintf(queryPrompt, st.Person, period, st.Text, st.Quote, queriesMax)}},
		Temperature:    0.0,
		MaxTokens:      200,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskExtract,
	})
	if err != nil {
		return Found{}, fmt.Errorf("statement day: writing the queries: %w", err)
	}
	qr, err := llm.ParseJSONResponse[queryReply](content)
	if err != nil {
		return Found{}, fmt.Errorf("statement day: parsing %q: %w", content, err)
	}

	var results []search.SearchResult
	seen := map[string]bool{}
	var searchErr error
	searched := 0
	for _, q := range qr.Queries {
		q = strings.TrimSpace(q)
		if q == "" || searched == queriesMax {
			continue
		}
		searched++
		rs, err := f.search.SearchWeb(q, resultsPerQuery)
		if err != nil {
			searchErr = err
			continue
		}
		for _, r := range rs {
			if r.URL == "" || seen[r.URL] || len(results) == resultsToTheLLM {
				continue
			}
			seen[r.URL] = true
			results = append(results, r)
		}
	}
	if len(results) == 0 {
		if searchErr != nil {
			return Found{}, fmt.Errorf("statement day: searching: %w", searchErr)
		}
		return Found{Reasoning: "no search result"}, nil
	}

	var list strings.Builder
	for i, r := range results {
		date := "unknown"
		if t, ok := parsePubDate(r.Date); ok {
			date = t.Format("Monday 2006-01-02")
		} else if r.Date != "" {
			date = r.Date
		}
		fmt.Fprintf(&list, "[%d] %s\n    published: %s\n    %s\n    %s\n", i+1, r.Title, date, r.URL, r.Snippet)
	}
	src := backend.FormatFactDateISO(st.SourceDate)
	if src == "" {
		src = "unknown"
	}
	content, err = f.chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: fmt.Sprintf(dayPrompt, st.Person, period, src, st.Text, st.Quote, list.String())}},
		Temperature:    0.0,
		MaxTokens:      700,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskExtract,
	})
	if err != nil {
		return Found{}, fmt.Errorf("statement day: reading the results: %w", err)
	}
	reply, err := llm.ParseJSONResponse[dayReply](content)
	if err != nil {
		return Found{}, fmt.Errorf("statement day: parsing %q: %w", content, err)
	}
	return check(st, results, reply), nil
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// check keeps the day of a reply only when the code can hold it to the
// results: a real day inside the known period, not after the source, given
// by a result whose text holds the evidence.
func check(st Statement, results []search.SearchResult, reply dayReply) Found {
	out := Found{Reasoning: reply.Reasoning}
	refuse := func(why string) Found {
		out.Reasoning = "refused (" + why + "): " + reply.Reasoning
		return out
	}
	if reply.Day == nil || strings.TrimSpace(*reply.Day) == "" || strings.EqualFold(strings.TrimSpace(*reply.Day), "null") {
		return out
	}
	s := strings.TrimSpace(*reply.Day)
	if !dayRe.MatchString(s) {
		return refuse("not a day")
	}
	day, err := backend.ParseStatementDate(s)
	if err != nil {
		return refuse("no such day")
	}
	if day.Year() != st.Known.Year() {
		return refuse("outside the known year")
	}
	if backend.DatePrecisionOf(st.Known) == backend.DatePrecisionMonth && day.Month() != st.Known.Month() {
		return refuse("outside the known month")
	}
	if st.SourceDate != nil && day.After(st.SourceDate.UTC()) {
		return refuse("after the source")
	}
	if reply.Result == nil || *reply.Result < 1 || *reply.Result > len(results) {
		return refuse("no result named")
	}
	r := results[*reply.Result-1]
	evidence := ""
	if reply.Evidence != nil {
		evidence = strings.TrimSpace(*reply.Evidence)
	}
	how := ""
	if reply.How != nil {
		how = strings.ToLower(strings.TrimSpace(*reply.How))
	}
	switch how {
	case "stated":
		// The result writes the day out: its words are in the result and
		// carry the number of the day.
		if !holds(r.Title+" "+r.Snippet, evidence) {
			return refuse("the evidence is not in the result")
		}
		if !hasNumber(evidence, day.Day()) {
			return refuse("the evidence does not name the day")
		}
	case "published":
		// The result is of the statement's own days: published on the day
		// or within the week after it.
		pub, ok := parsePubDate(r.Date)
		if !ok {
			return refuse("the result has no publication date")
		}
		if gap := pub.Sub(day.UTC()); gap < 0 || gap > publishedWithin {
			return refuse("the result was not published in the days of the statement")
		}
		evidence = "published " + pub.Format("2006-01-02")
	default:
		return refuse("no way given")
	}
	out.Day, out.URL, out.Evidence = &day, r.URL, evidence
	return out
}

// publishedWithin is how long after the day a result may be published and
// still date it ("a déclaré lundi", in the week's paper).
const publishedWithin = 6 * 24 * time.Hour

var numberRe = regexp.MustCompile(`\d+`)

// hasNumber reports whether text holds n as a number of its own ("7" or
// "07", not the 7 of "17").
func hasNumber(text string, n int) bool {
	for _, m := range numberRe.FindAllString(text, -1) {
		if v, err := strconv.Atoi(m); err == nil && v == n && len(m) <= 2 {
			return true
		}
	}
	return false
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// holds reports whether text holds the evidence, word for word, whatever the
// case, the typography and the punctuation between the words.
func holds(text, evidence string) bool {
	fold := func(s string) string {
		return strings.Join(wordRe.FindAllString(strings.ToLower(s), -1), " ")
	}
	e := fold(evidence)
	if len([]rune(e)) < evidenceMinRunes {
		return false
	}
	return strings.Contains(fold(text), e)
}
