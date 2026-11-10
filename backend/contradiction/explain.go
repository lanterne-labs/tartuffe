package contradiction

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// explainMaxTokens is the answer budget of an explanation: three sentences
// and a possible doubt, in a small JSON object.
const explainMaxTokens = 700

// maxExplanationRunes refuses a reply that is not the short text asked for
// (the prompt asks for 90 words: about 600 characters).
const maxExplanationRunes = 1200

// Explanation is the text the site shows with a contradiction, and the doubt
// the writer has about the pair after reading the source quotes ("": none).
type Explanation struct {
	Text  string
	Doubt string
}

// Explainer writes the explanation of one contradiction.
type Explainer interface {
	Explain(ctx context.Context, c storage.ContradictionToExplain) (Explanation, error)
}

// LLMExplainer is the Explainer of production.
type LLMExplainer struct {
	client *llm.Client
}

// NewExplainerFromEnv builds the LLM explainer from the OPEN_AI_ variables.
func NewExplainerFromEnv() (*LLMExplainer, error) {
	client, err := llm.NewClientFromEnv("contradiction-explain")
	if err != nil {
		return nil, err
	}
	return &LLMExplainer{client: client}, nil
}

// explainSide renders one fact for the prompt: its date, its text, and the
// quote of its source when it has one.
func explainSide(s storage.ExplainSide) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Statement (dated %s): %s", backend.FormatFactDate(s.Date), s.Text)
	if q := strings.TrimSpace(s.Quote); q != "" {
		src := "the source"
		if u, err := url.Parse(s.SourceURL); err == nil && u.Hostname() != "" {
			src = strings.TrimPrefix(u.Hostname(), "www.")
		}
		if s.SourceDate != nil && s.Date != nil && !s.SourceDate.Equal(*s.Date) && backend.DatePrecisionOf(*s.Date) != "" {
			src += ", published " + backend.FormatFactDate(s.SourceDate)
		}
		fmt.Fprintf(&b, "\nExcerpt of %s it was taken from: \"%s\"", src, q)
	}
	return b.String()
}

// explainOrder gives the two facts of c, the earlier first. A fact without a
// date keeps its place.
func explainOrder(c storage.ContradictionToExplain) (earlier, later storage.ExplainSide) {
	if c.A.Date != nil && c.B.Date != nil && c.B.Date.Before(*c.A.Date) {
		return c.B, c.A
	}
	return c.A, c.B
}

// ExplainPrompt is the prompt of one contradiction.
func ExplainPrompt(c storage.ContradictionToExplain) string {
	earlier, later := explainOrder(c)
	reason := strings.TrimSpace(c.Reason)
	if reason == "" {
		reason = "(no reason recorded)"
	}
	return fmt.Sprintf(promptrules.ExplanationPrompt, c.Person, explainSide(earlier), explainSide(later), reason)
}

type explainReply struct {
	Explanation string `json:"explanation"`
	Doubt       string `json:"doubt"`
}

// Explain asks the LLM for the explanation of c.
func (e *LLMExplainer) Explain(ctx context.Context, c storage.ContradictionToExplain) (Explanation, error) {
	content, _, err := e.client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: ExplainPrompt(c)}},
		Temperature:    0.0,
		MaxTokens:      explainMaxTokens,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskExplain,
	})
	if err != nil {
		return Explanation{}, fmt.Errorf("explanation LLM call failed: %w", err)
	}
	reply, err := llm.ParseJSONResponse[explainReply](content)
	if err != nil {
		return Explanation{}, fmt.Errorf("invalid explanation JSON: %w", err)
	}
	text := strings.TrimSpace(reply.Explanation)
	if text == "" {
		return Explanation{}, fmt.Errorf("explanation reply had no text: %q", content)
	}
	if n := len([]rune(text)); n > maxExplanationRunes {
		return Explanation{}, fmt.Errorf("explanation of %d characters, over the %d allowed", n, maxExplanationRunes)
	}
	return Explanation{Text: text, Doubt: strings.TrimSpace(reply.Doubt)}, nil
}

// ExplainStorer is the storage RunExplain uses.
type ExplainStorer interface {
	ListContradictionsToExplain(ctx context.Context, version string, f storage.ExplainFilter) ([]storage.ContradictionToExplain, error)
	SetContradictionExplanation(ctx context.Context, id int64, explanation, hash string) error
}

// ExplainOptions scopes a run of RunExplain.
type ExplainOptions struct {
	storage.ExplainFilter
	// DryRun writes nothing.
	DryRun bool
	// OnExplained is called for each explanation written (or, in a dry run,
	// that would be), one call at a time.
	OnExplained func(c storage.ContradictionToExplain, e Explanation)
}

// ExplainDoubt is a contradiction the writer doubts after reading its quotes.
type ExplainDoubt struct {
	ContradictionID int64
	Person          string
	Doubt           string
}

// ExplainResult is what a run of RunExplain did.
type ExplainResult struct {
	// Pending is how many contradictions the run had to explain.
	Pending int
	// Written is how many explanations it wrote, Failed how many replies it
	// could not use (those contradictions stay for the next run).
	Written, Failed int
	// Doubts are the contradictions to review.
	Doubts []ExplainDoubt
}

// RunExplain writes the explanation of the contradictions that have none, or
// one written from other facts or an older prompt. A reply that cannot be
// used is logged and the contradiction waits for the next run. The run stops
// at the first call the LLM could not serve, with an error wrapping
// llm.ErrUnavailable, and when ctx is cancelled, with an error wrapping
// ctx.Err(); what was written before stays.
func RunExplain(ctx context.Context, storer ExplainStorer, ex Explainer, opts ExplainOptions) (ExplainResult, error) {
	todo, err := storer.ListContradictionsToExplain(ctx, promptrules.ExplanationVersion, opts.ExplainFilter)
	if err != nil {
		return ExplainResult{}, err
	}
	res := ExplainResult{Pending: len(todo)}
	var mu sync.Mutex
	err = llm.ForEach(ctx, todo, func(ctx context.Context, c storage.ContradictionToExplain) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := ex.Explain(ctx, c)
		switch {
		case errors.Is(err, llm.ErrUnavailable):
			return err
		case err != nil && ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			log.Warn().Err(err).Int64("contradiction_id", c.ID).Msg("Explanation failed; the contradiction waits for the next run")
			mu.Lock()
			res.Failed++
			mu.Unlock()
			return nil
		}
		if !opts.DryRun {
			// A text already paid for is stored even when the deadline passed meanwhile.
			if err := storer.SetContradictionExplanation(context.WithoutCancel(ctx), c.ID, e.Text, c.Hash); err != nil {
				log.Error().Err(err).Int64("contradiction_id", c.ID).Msg("Failed to store an explanation")
				mu.Lock()
				res.Failed++
				mu.Unlock()
				return nil
			}
		}
		mu.Lock()
		defer mu.Unlock()
		res.Written++
		if e.Doubt != "" {
			res.Doubts = append(res.Doubts, ExplainDoubt{ContradictionID: c.ID, Person: c.Person, Doubt: e.Doubt})
			log.Warn().Int64("contradiction_id", c.ID).Str("person", c.Person).Str("doubt", e.Doubt).
				Msg("Explanation: the source quotes raise a doubt about this contradiction")
		}
		if opts.OnExplained != nil {
			opts.OnExplained(c, e)
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("explanations stopped after %d of %d: %w", res.Written, res.Pending, err)
	}
	return res, nil
}
