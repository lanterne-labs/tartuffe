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

Give the date of the STATEMENT OR ACT ITSELF only when the fact text or the quote states it or plainly ties it to a date: a year, a month or a day, as precise as the text gives ("2019", "2024-03", "2023-07-14"). A relative phrase ("hier", "la semaine dernière", "dimanche") is resolved against the source date. When the text gives no date for the statement, or the statement is of the source's own day or interview, answer null: the source date then stands. Never guess a date from the topic or from events you know.

Person: %s
Source date: %s
Fact: %s
Quote: %s

Return ONLY strict JSON, the reasoning first:
{"reasoning": "<one sentence: what in the text dates the statement, or that nothing does>", "statement_date": "YYYY" | "YYYY-MM" | "YYYY-MM-DD" | null}`

type statementDateReply struct {
	Reasoning     string  `json:"reasoning"`
	StatementDate *string `json:"statement_date"`
}

// StatementDate returns the statement date of the fact (nil: the text gives
// none), with the model's reasoning.
func (d *StatementDater) StatementDate(ctx context.Context, person, factText, quote string, sourceDate *time.Time) (*time.Time, string, error) {
	src := backend.FormatFactDateISO(sourceDate)
	if src == "" {
		src = "unknown"
	}
	prompt := fmt.Sprintf(statementDatePrompt, person, src, factText, quote)
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
