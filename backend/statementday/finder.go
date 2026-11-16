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

	"github.com/rs/zerolog/log"

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
	How       string // "stated": the text writes the day out; "published": the day of a text of the statement's own days
	Reasoning string
	// Page is the page that gives the day, when it was read: an article of
	// the statement's time, worth extracting for its other facts.
	Page *Page

	pub time.Time // the publication date a "published" day from the results was checked against
}

// Searcher is the web search the finder asks (search.WebSearcher).
type Searcher interface {
	SearchWeb(query string, limit int) ([]search.SearchResult, error)
}

// Finder finds the day of a statement.
type Finder struct {
	search Searcher
	read   Reader // nil: the search results only
	chat   func(ctx context.Context, req llm.ChatRequest) (string, error)
}

// NewFinder builds a finder on a searcher, an LLM client and a reader of
// pages (nil: the finder reads the search results only).
func NewFinder(s Searcher, client *llm.Client, read Reader) *Finder {
	return &Finder{search: s, read: read, chat: func(ctx context.Context, req llm.ChatRequest) (string, error) {
		content, _, err := client.Chat(ctx, req)
		return content, err
	}}
}

// NewFinderFromEnv builds the finder on the configured search chain and LLM
// endpoint, with read to read the pages found.
func NewFinderFromEnv(read Reader) (*Finder, error) {
	s, err := search.NewWebSearcherFromEnv()
	if err != nil {
		return nil, err
	}
	client, err := llm.NewClientFromEnv("statement-day")
	if err != nil {
		return nil, err
	}
	return NewFinder(s, client, read), nil
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
	found := check(st, results, reply)
	if f.read == nil {
		return found, nil
	}
	if found.Day != nil {
		// The page of the result that gives the day, for the caller to
		// queue: the day stands without it.
		if !isArticleURL(found.URL) {
			return found, nil
		}
		page, err := f.read.Read(ctx, found.URL)
		if err != nil {
			log.Debug().Err(err).Str("url", found.URL).Msg("statement day: the dating page could not be read")
			return found, nil
		}
		// A day taken from the date the search engine shows for a page
		// stands only when the page agrees: the engine often shows the
		// date of an update. When the page gives another date, the pages
		// are read for the day instead.
		if found.How != "published" || page.Published == nil || sameDay(*page.Published, found.pub) {
			found.Page = &page
			return found, nil
		}
		found = Found{Reasoning: "refused (the page gives another publication date than the search): " + found.Reasoning}
	}
	// The results alone give no day: the pages may.
	inPages, err := f.findInPages(ctx, st, results)
	if err != nil {
		return Found{}, err
	}
	if inPages.Day == nil {
		inPages.Reasoning = found.Reasoning + " | " + inPages.Reasoning
	}
	return inPages, nil
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// check keeps the day a reply gives from the search results only when the
// result it names holds it (checkDay).
func check(st Statement, results []search.SearchResult, reply dayReply) Found {
	if reply.Day != nil && strings.TrimSpace(*reply.Day) != "" && !strings.EqualFold(strings.TrimSpace(*reply.Day), "null") {
		if reply.Result == nil || *reply.Result < 1 || *reply.Result > len(results) {
			return Found{Reasoning: "refused (no result named): " + reply.Reasoning}
		}
	} else {
		return Found{Reasoning: reply.Reasoning}
	}
	r := results[*reply.Result-1]
	pub, hasPub := parsePubDate(r.Date)
	out := checkDay(st, reply, r.Title+" "+r.Snippet, pub, hasPub)
	if out.Day != nil {
		out.URL, out.pub = r.URL, pub
	}
	return out
}

// checkDay keeps the day of a reply only when the code can hold it to the
// text it was read from (a search result, a page): a real day inside the
// known period, not after the source, either written out in the text or the
// day of a text published in the statement's own days.
func checkDay(st Statement, reply dayReply, text string, pub time.Time, hasPub bool) Found {
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
		// The text writes the day out: its words are in the text and carry
		// the number of the day.
		if !holds(text, evidence) {
			return refuse("the evidence is not in the text")
		}
		if !hasNumber(evidence, day.Day()) {
			return refuse("the evidence does not name the day")
		}
		// "mardi 23 juillet" is not a Saturday: the weekday the text
		// names must be the day's.
		if wd, named := weekdayIn(evidence); named && wd != day.Weekday() {
			return refuse("the evidence names another weekday")
		}
		// A day written without its year ("le 26 septembre") is the last
		// such day before the text was published, not one of the year the
		// site knows: a text of 2021 that says "le 26 septembre" does not
		// date a statement of 2020.
		if !hasYear(evidence, day.Year()) {
			if !hasPub {
				return refuse("the evidence gives no year and the text no publication date")
			}
			if gap := pub.Sub(day.UTC()); gap < 0 || gap >= 365*24*time.Hour {
				return refuse("the evidence gives no year and the text is of another year")
			}
		}
	case "published":
		// The text is of the statement's own days: published on the day or
		// within the week after it.
		if !hasPub {
			return refuse("no publication date")
		}
		if gap := pub.Sub(day.UTC()); gap < 0 || gap > publishedWithin {
			return refuse("not published in the days of the statement")
		}
		evidence = "published " + pub.Format("2006-01-02")
	default:
		return refuse("no way given")
	}
	out.Day, out.Evidence, out.How = &day, evidence, how
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

// hasYear reports whether text holds the year as a number.
func hasYear(text string, year int) bool {
	for _, m := range numberRe.FindAllString(text, -1) {
		if v, err := strconv.Atoi(m); err == nil && v == year {
			return true
		}
	}
	return false
}

var weekdays = map[string]time.Weekday{
	"lundi": time.Monday, "mardi": time.Tuesday, "mercredi": time.Wednesday, "jeudi": time.Thursday,
	"vendredi": time.Friday, "samedi": time.Saturday, "dimanche": time.Sunday,
	"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
}

// weekdayIn returns the weekday text names, when it names exactly one.
func weekdayIn(text string) (time.Weekday, bool) {
	var found time.Weekday
	n := 0
	for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
		if wd, ok := weekdays[w]; ok && (n == 0 || wd != found) {
			found = wd
			n++
		}
	}
	return found, n == 1
}

// sameDay reports whether a and b are the same day, or a day apart (the
// time zones of a page and of a search engine).
func sameDay(a, b time.Time) bool {
	d := a.UTC().Sub(b.UTC())
	if d < 0 {
		d = -d
	}
	return d < 36*time.Hour
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
