package pairfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// decisionQuestion is the question the threshold was measured with: a change
// of a word moves the probabilities, so measure again before changing it.
const decisionQuestion = "Could these two statements by the same politician conflict with each other: the person takes opposite " +
	"positions on the same question, says the opposite of what they said or did, or a claim of theirs is against " +
	"a finding about them? True if there is any such tension, even partial. False if they are about different " +
	"subjects or are compatible."

// DefaultDecisionThreshold is the probability of "could conflict" under which
// a pair needs no judge. Measured on 2026-11-17 with OpenJev-4B on 430
// contradictions (333 a person reviewed as real, 97 of the judge) and 6,000
// pairs the judge called "no": 0.10 keeps 419 of the 430 (97.4 %) and clears
// half of the others. 0.02 keeps 427 and clears 12 %; 0.129 keeps 414 and
// clears 58 %; 0.236 keeps 399 and clears 76 %.
const DefaultDecisionThreshold = 0.10

const (
	// decisionAtOnce is how many requests are on their way at once. The server
	// runs one at a time (about 20 a second): the others cover the trip there.
	decisionAtOnce = 8
	// decisionTimeout covers the load of the model: the server gives the GPU
	// to it on the first request (about 15 s).
	decisionTimeout = 2 * time.Minute
	// decisionRest is how long a filter that failed answers "cannot" without
	// trying, so a machine that is off costs one timeout, not one per call.
	decisionRest = 2 * time.Minute
)

// Decision asks a decision model (OpenJev's HTTP server, POST /v1/systemone:
// probabilities over given answers, no text) whether two statements could
// conflict, one request per pair. It has no rulebook and gives no reason: it
// only clears the pairs that plainly do not conflict.
type Decision struct {
	url       string
	key       string
	threshold float64
	client    *http.Client

	mu        sync.Mutex
	restUntil time.Time
}

// NewDecision returns the filter for the server at url (the full address of
// its /v1/systemone). key is sent as a bearer token when not empty.
func NewDecision(url, key string, threshold float64) *Decision {
	return &Decision{url: url, key: key, threshold: threshold, client: &http.Client{Timeout: decisionTimeout}}
}

func (d *Decision) Name() string { return "decision model" }

func (d *Decision) Keep(ctx context.Context, pairs []Pair) ([]bool, error) {
	d.mu.Lock()
	rest := time.Until(d.restUntil)
	d.mu.Unlock()
	if rest > 0 {
		return nil, fmt.Errorf("decision model: resting %s after a failure", rest.Round(time.Second))
	}

	keep := make([]bool, len(pairs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(decisionAtOnce)
	for i, p := range pairs {
		g.Go(func() error {
			prob, err := d.couldConflict(gctx, p)
			if err != nil {
				return err
			}
			keep[i] = prob >= d.threshold
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		if ctx.Err() == nil {
			d.mu.Lock()
			d.restUntil = time.Now().Add(decisionRest)
			d.mu.Unlock()
		}
		return nil, fmt.Errorf("decision model: %w", err)
	}
	return keep, nil
}

// couldConflict returns the model's probability that the pair could conflict.
func (d *Decision) couldConflict(ctx context.Context, p Pair) (float64, error) {
	body, err := json.Marshal(map[string]any{
		"state": fmt.Sprintf("Statement A (%s): %s\n\nStatement B (%s): %s", dateOr(p.DateA), p.A, dateOr(p.DateB), p.B),
		"questions": map[string]any{
			"q": map[string]string{"type": "noul", "instructions": decisionQuestion},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("encoding the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if d.key != "" {
		req.Header.Set("Authorization", "Bearer "+d.key)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("reading the answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d: %.200s", resp.StatusCode, raw)
	}
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("decoding the answer: %w", err)
	}
	prob := out.Answers["q"].Noul
	if prob == nil {
		return 0, errors.New("the answer holds no probability")
	}
	return *prob, nil
}

func dateOr(date string) string {
	if date == "" {
		return "date unknown"
	}
	return date
}
