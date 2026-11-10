package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/llm"
)

// StatementDater finds the statement date of a stored fact from its text, its
// quote and its source's date: the repair of the facts extracted before the
// extraction gave the date (mirror-fmf; new facts get it from the extractor,
// backend/promptrules/fact_extraction.go rule 10).
type StatementDater struct {
	client *llm.Client
}

// NewStatementDaterFromEnv builds the dater on the configured LLM endpoint.
func NewStatementDaterFromEnv() (*StatementDater, error) {
	client, err := llm.NewClientFromEnv("statement-dater")
	if err != nil {
		return nil, err
	}
	return &StatementDater{client: client}, nil
}

const statementDatePrompt = `You date a statement of a politician stored by a fact-checking site.

The fact below was extracted from a source (an article, a tweet, a transcript) published on the SOURCE DATE. Most facts state what the person said or did around that date. Some state what they said or did long before: an old tweet the article brings back, "en 2019, elle avait déclaré…", "lors de la campagne de 2017", a vote of a past year.

Give the date of the STATEMENT OR ACT ITSELF only when the fact text or the quote states it or plainly ties it to a date: a year, a month or a day, as precise as the text gives ("2019", "2024-03", "2023-07-14"). A relative phrase ("hier", "la semaine dernière", "dimanche") is resolved against the source date.
- A date that the text only TALKS ABOUT is not the date of the statement or act: "il évoque la rupture de 2015", "elle promet une réforme pour 2027", "l'affaire qui débute à l'été 2013 lui vaut une condamnation" (the conviction is not of 2013), "il appelle à voter dimanche 7 juillet" (the call is not of that Sunday) say nothing of when the words were said or the act done.
- A past moment the person mentions INSIDE their own words does not date those words: « Je me suis présentée à l'automne dernier devant le tribunal » is a statement of the source's days.
- The statement is never later than the source that reports it.
- When the text gives no date for the statement, or the statement is of the source's own day or interview, answer null: the source date then stands. Never guess a date from the topic or from events you know.

Person: %s
Source date: %s
Fact: %s
Quote: %s

Return ONLY strict JSON, the reasoning first:
{"reasoning": "<one sentence: what in the text dates the statement, or that nothing does>", "statement_date": "YYYY" | "YYYY-MM" | "YYYY-MM-DD" | null}`

// statementDateArticlePrompt is statementDatePrompt for a fact whose source's
// text is stored: the article around the quote dates what the fact alone does
// not (a portrait that walks through the person's past, « lançait-il »).
// Measured on the GPU box model against the extractor's dates of 160 news
// facts (2026-11-10): 147 agree, and most of the 13 others are the
// reference's own errors or within a month.
//
// Format args: the person, the source date, the article's title, the fact,
// the quote, the excerpt.
const statementDateArticlePrompt = `You date a statement of a politician stored by a fact-checking site.

The fact below was extracted from an article published on the SOURCE DATE. Most facts state what the person said or did around that date. Some state what they said or did long before: an old tweet or interview the article brings back, "en 2019, elle avait déclaré…", "lors de la campagne de 2017", a vote of a past year, a portrait that walks through the person's past.

Find in the ARTICLE EXCERPT the passage the fact comes from (the quote marks it) and read what dates THAT statement or act: a date or a year in the sentence or just before it, the occasion (a named interview, meeting, vote, campaign, book), the tense (« lançait-il », « avait-elle dit », « à l'époque » point to the past the article is recalling at that place).

Give the date of the STATEMENT OR ACT ITSELF, as precise as the article gives it ("2019", "2024-03", "2023-07-14"). A relative phrase ("hier", "la semaine dernière", "dimanche") is resolved against the source date.
- The fact text may state the date itself ("a félicité Maduro en avril 2021", "lors du débat du 23 septembre 2021"): that date counts even when the excerpt does not show the passage.
- A date that the statement only TALKS ABOUT is not its date: "il évoque la rupture de 2015", "elle promet une réforme pour 2027", "candidat à la présidentielle de 2027", "il affirme que le 15 décembre dernier l'État a refusé…", "l'affaire qui débute à l'été 2013 lui vaut une condamnation" (the conviction is not of 2013), "il appelle à voter dimanche 7 juillet" (the call is not of that Sunday) say nothing of when the words were said or the act done.
- The statement is never later than the source that reports it.
- A past moment the person mentions INSIDE their own words does not date those words: « Je me suis présentée à l'automne dernier devant le tribunal », said in an interview of this week, is a statement of this week.
- When the article reports the statement as news of its own days (said this week, in this interview, yesterday), or gives no date for it, answer null: the source date then stands.
- Never guess a date from the topic or from events you know. Only what the excerpt or the fact states.

Person: %s
Source date: %s
Article title: %s
Fact: %s
Quote: %s

ARTICLE EXCERPT:
%s

Return ONLY strict JSON, the reasoning first:
{"reasoning": "<one sentence: the words of the article that date the statement, or that nothing does>", "statement_date": "YYYY" | "YYYY-MM" | "YYYY-MM-DD" | null}`

// The excerpt of an article given to the dater: what comes before the quote
// dates it more often than what follows, and the opening names the occasion.
const (
	excerptBefore   = 2500
	excerptAfter    = 1500
	excerptHead     = 500
	excerptNotFound = 9000
)

// ArticleExcerpt returns the part of article around quote: the opening, then
// the text before and after the quote (backend.QuotePosition finds it); an
// article where it is not found is given from its start.
func ArticleExcerpt(article, quote string) string {
	r := []rune(article)
	pos := backend.QuotePosition(article, quote)
	if pos < 0 {
		return string(r[:min(len(r), excerptNotFound)])
	}
	a, b := max(0, pos-excerptBefore), min(len(r), pos+excerptAfter)
	out := string(r[a:b])
	if a > excerptHead {
		out = string(r[:excerptHead]) + "\n[…]\n" + out
	}
	return out
}

type statementDateReply struct {
	Reasoning     string  `json:"reasoning"`
	StatementDate *string `json:"statement_date"`
}

// StatementDate returns the statement date of the fact (nil: the text gives
// none), with the model's reasoning.
func (d *StatementDater) StatementDate(ctx context.Context, person, factText, quote string, sourceDate *time.Time) (*time.Time, string, error) {
	return d.StatementDateInArticle(ctx, person, factText, quote, sourceDate, "", "")
}

// StatementDateInArticle is StatementDate for a fact whose source's text is
// known: the dater reads the article around the quote. An empty article is
// StatementDate.
func (d *StatementDater) StatementDateInArticle(ctx context.Context, person, factText, quote string, sourceDate *time.Time, title, article string) (*time.Time, string, error) {
	src := backend.FormatFactDateISO(sourceDate)
	if src == "" {
		src = "unknown"
	}
	prompt := fmt.Sprintf(statementDatePrompt, person, src, factText, quote)
	if strings.TrimSpace(article) != "" {
		prompt = fmt.Sprintf(statementDateArticlePrompt, person, src, title, factText, quote, ArticleExcerpt(article, quote))
	}
	content, _, err := d.client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.0,
		MaxTokens:      300,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskExtract,
	})
	if err != nil {
		return nil, "", fmt.Errorf("statement dater: %w", err)
	}
	reply, err := llm.ParseJSONResponse[statementDateReply](content)
	if err != nil {
		return nil, "", fmt.Errorf("statement dater: parsing %q: %w", content, err)
	}
	if reply.StatementDate == nil || strings.TrimSpace(*reply.StatementDate) == "" || strings.EqualFold(strings.TrimSpace(*reply.StatementDate), "null") {
		return nil, reply.Reasoning, nil
	}
	t, err := backend.ParseStatementDate(strings.TrimSpace(*reply.StatementDate))
	if err != nil {
		return nil, reply.Reasoning, fmt.Errorf("statement dater: %w", err)
	}
	// A source cannot report what was not said yet: a later date is the
	// date of something the statement talks about (a vote called for
	// "dimanche"), and the source's date stands.
	if sourceDate != nil && t.After(sourceDate.UTC().Add(24*time.Hour)) {
		return nil, reply.Reasoning, nil
	}
	return &t, reply.Reasoning, nil
}

// SameStatementDay reports whether a statement date only repeats the source's
// date (same day, or same month or year at that precision): storing it adds
// nothing.
func SameStatementDay(statement, source *time.Time) bool {
	if statement == nil || source == nil {
		return false
	}
	s, o := *statement, source.UTC()
	switch backend.DatePrecisionOf(s) {
	case backend.DatePrecisionYear:
		return s.Year() == o.Year()
	case backend.DatePrecisionMonth:
		return s.Year() == o.Year() && s.Month() == o.Month()
	}
	return s.Year() == o.Year() && s.YearDay() == o.YearDay()
}
