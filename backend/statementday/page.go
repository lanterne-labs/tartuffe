package statementday

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/search"
)

// Page is a page of the web as read: its text and the publication date the
// page gives.
type Page struct {
	URL       string
	Title     string
	Text      string
	Published *time.Time
}

// Reader reads a page of the web.
type Reader interface {
	Read(ctx context.Context, url string) (Page, error)
}

// ExtractorReader reads pages with a content extractor (the one the news
// collection reads its articles with: it goes through webfetch).
func ExtractorReader(e extractor.ContentExtractor) Reader { return extractorReader{e} }

type extractorReader struct{ e extractor.ContentExtractor }

func (r extractorReader) Read(_ context.Context, u string) (Page, error) {
	c, err := r.e.ExtractContent(u)
	if err != nil {
		return Page{}, err
	}
	if c == nil {
		return Page{URL: u}, nil
	}
	p := Page{URL: c.URL, Title: c.Title, Text: c.Content, Published: c.DatePublished}
	if p.URL == "" {
		p.URL = u
	}
	return p, nil
}

const (
	// pagesMax is how many result pages are read when the results alone
	// give no day.
	pagesMax = 5
	// pageMinRunes is the least text a page must have to be read for a day.
	pageMinRunes = 200
	// ArticleMinRunes is the least text a page must have to be queued for
	// the extraction of its facts: under it, a video page or a stub.
	ArticleMinRunes = 1500

	pageBefore, pageAfter, pageHead, pageNoQuote = 2500, 1500, 500, 8000
)

// notArticles are the hosts whose pages are not articles to read or to
// extract: the plugins of their own read them, or nothing does.
var notArticles = []string{"x.com", "twitter.com", "youtube.com", "youtu.be", "facebook.com", "instagram.com", "tiktok.com", "dailymotion.com", "linkedin.com"}

// notToExtract are the hosts whose pages may date a statement but are not
// articles of its time: an encyclopedia recalls years of a person's words in
// its own summary, and its page carries the day it was created (prod, 2026-11-18:
// 27 facts of five Wikipedia pages dated 2005 to 2016).
var notToExtract = []string{"wikipedia.org", "wikiwand.com", "wikimonde.com", "wikiquote.org"}

func hostIn(raw string, hosts []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func isArticleURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	for _, h := range notArticles {
		if host == h || strings.HasSuffix(host, "."+h) {
			return false
		}
	}
	return true
}

// pagesToRead orders the results for reading: the pages published in the
// known period first (the reports of the statement's own days), then the
// undated ones, then the later ones (an article that recalls the statement
// may write its day out).
func pagesToRead(st Statement, results []search.SearchResult) []search.SearchResult {
	rank := func(r search.SearchResult) int {
		pub, ok := parsePubDate(r.Date)
		switch {
		case !ok:
			return 1
		case inPeriod(st.Known, pub):
			return 0
		}
		return 2
	}
	var out []search.SearchResult
	for _, r := range results {
		if isArticleURL(r.URL) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	if len(out) > pagesMax {
		out = out[:pagesMax]
	}
	return out
}

// inPeriod reports whether day is in the year, or the month, known is.
func inPeriod(known, day time.Time) bool {
	if day.Year() != known.Year() {
		return false
	}
	return backend.DatePrecisionOf(known) != backend.DatePrecisionMonth || day.Month() == known.Month()
}

// excerpt is the part of the page given to the LLM: its opening, then the
// text around the quote of the statement when the page holds it.
func excerpt(text, quote string) string {
	r := []rune(text)
	pos := -1
	if strings.TrimSpace(quote) != "" {
		pos = backend.QuotePosition(text, quote)
	}
	if pos < 0 {
		return string(r[:min(len(r), pageNoQuote)])
	}
	a, b := max(0, pos-pageBefore), min(len(r), pos+pageAfter)
	out := string(r[a:b])
	if a > pageHead {
		out = string(r[:pageHead]) + "\n[…]\n" + out
	}
	return out
}

// Format args: the person, the period, the source date, the fact, the quote,
// the page's title, its publication date, the excerpt.
const pagePrompt = `A fact-checking site knows that a politician said or did something in a given year or month. Below is a page found on the web. Find the exact DAY of that statement or act, if the page gives it.

Person: %s
Known period: %s
The source that recalls it was published on: %s
Fact: %s
Quote of the source: %s

PAGE
Title: %s
Published: %s
Text:
%s

Give a day only when the page shows it for THIS VERY statement or act (the same words, vote, interview or occasion, not another statement on the same subject), in one of two ways:
- "stated": the page writes the day out next to the statement ("le 7 décembre 2021, le Sénat a rejeté…", "interrogée le 14 juin sur France Inter"). "evidence" is then those words, copied WORD FOR WORD from the page, with the number of the day in them.
- "published": the page IS the statement or reports it as the news of its own days (the interview, the report of the vote or of the meeting), and it was published in the known period. The day is its publication date, or the day its words name against that date ("a déclaré lundi soir" in a page published on a Tuesday is the day before: the weekday of the publication date is given, do not compute it yourself). At most six days before the publication.
A politician speaks on one subject many times. When the fact gives the person's own words between quotation marks and the page quotes OTHER words of theirs on the same subject, it is another statement, unless the page names the same occasion as the fact: answer null.
A page published later that only recalls the statement without its day does not date it. A day outside the known period is wrong. When the page does not give the day, or you hesitate between two days, answer null: an honest year is better than a wrong day. Never give a day from what you know yourself.

Return ONLY strict JSON, a short reasoning first:
{"reasoning": "<one sentence: how the page gives the day, or that it does not>", "how": "stated" | "published" | null, "evidence": "<the words of the page that write the day out, verbatim>" | null, "day": "YYYY-MM-DD" | null}`

// findInPages reads the pages of the results and returns the first day a
// page gives. A page that cannot be read is left.
func (f *Finder) findInPages(ctx context.Context, st Statement, results []search.SearchResult) (Found, error) {
	period := backend.FormatFactDate(&st.Known)
	src := backend.FormatFactDateISO(st.SourceDate)
	if src == "" {
		src = "unknown"
	}
	var last Found
	read := 0
	for _, r := range pagesToRead(st, results) {
		page, err := f.read.Read(ctx, r.URL)
		if err != nil {
			log.Debug().Err(err).Str("url", r.URL).Msg("statement day: the page could not be read")
			continue
		}
		if len([]rune(strings.TrimSpace(page.Text))) < pageMinRunes {
			continue
		}
		read++
		pub, hasPub := time.Time{}, false
		if page.Published != nil && !page.Published.IsZero() {
			p := page.Published.UTC()
			pub, hasPub = time.Date(p.Year(), p.Month(), p.Day(), 0, 0, 0, 0, time.UTC), true
		} else {
			pub, hasPub = parsePubDate(r.Date)
		}
		published := "unknown"
		if hasPub {
			published = pub.Format("Monday 2006-01-02")
		}
		text := excerpt(page.Text, st.Quote)
		content, err := f.chat(ctx, llm.ChatRequest{
			Messages:       []llm.ChatMessage{{Role: "user", Content: fmt.Sprintf(pagePrompt, st.Person, period, src, st.Text, st.Quote, page.Title, published, text)}},
			Temperature:    0.0,
			MaxTokens:      700,
			ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
			Task:           llm.TaskExtract,
		})
		if err != nil {
			return Found{}, fmt.Errorf("statement day: reading a page: %w", err)
		}
		reply, err := llm.ParseJSONResponse[dayReply](content)
		if err != nil {
			log.Warn().Err(err).Str("url", page.URL).Msg("statement day: the reply on a page could not be read")
			continue
		}
		got := checkDay(st, reply, page.Title+" "+text, pub, hasPub)
		if got.Day != nil {
			got.URL = page.URL
			if hasPub && page.Published == nil {
				page.Published = &pub
			}
			got.Page = &page
			return got, nil
		}
		last = got
	}
	if read == 0 {
		return Found{Reasoning: "no result gives the day and no page could be read"}, nil
	}
	last.Reasoning = fmt.Sprintf("no day in the results nor in the %d pages read; the last: %s", read, last.Reasoning)
	return last, nil
}
