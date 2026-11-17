package pairfilter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type failing struct{ calls int }

func (f *failing) Name() string { return "failing" }
func (f *failing) Keep(context.Context, []Pair) ([]bool, error) {
	f.calls++
	return nil, errors.New("down")
}

type none struct{}

func (none) Name() string { return "none" }
func (none) Keep(_ context.Context, pairs []Pair) ([]bool, error) {
	return make([]bool, len(pairs)), nil
}

func TestFallback(t *testing.T) {
	pairs := []Pair{{A: "a", B: "b"}, {A: "c", B: "d"}}

	// The first answers: the second is not asked.
	keep, err := Fallback(none{}, All{}).Keep(context.Background(), pairs)
	if err != nil || keep[0] || keep[1] {
		t.Fatalf("first filter's answer expected, got %v, %v", keep, err)
	}

	// The first fails: the second decides.
	first := &failing{}
	keep, err = Fallback(first, All{}).Keep(context.Background(), pairs)
	if err != nil || !keep[0] || !keep[1] || first.calls != 1 {
		t.Fatalf("fallback's answer expected, got %v, %v (calls %d)", keep, err, first.calls)
	}

	// Both fail: the error comes out.
	if _, err = Fallback(&failing{}, &failing{}).Keep(context.Background(), pairs); err == nil {
		t.Fatal("an error is expected when both filters fail")
	}
}

func TestDecision(t *testing.T) {
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		var req struct {
			State     string `json:"state"`
			Questions map[string]struct {
				Type         string `json:"type"`
				Instructions string `json:"instructions"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Questions["q"].Type != "noul" || req.Questions["q"].Instructions == "" {
			http.Error(w, "bad request", http.StatusUnprocessableEntity)
			return
		}
		prob := 0.02
		if strings.Contains(req.State, "Statement A (2022-01-01): retraite à 60 ans") && strings.Contains(req.State, "Statement B (date unknown): retraite à 62 ans") {
			prob = 0.9
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"q": map[string]any{"type": "noul", "noul": prob}}})
	}))
	defer srv.Close()

	d := NewDecision(srv.URL, "k", DefaultDecisionThreshold)
	keep, err := d.Keep(context.Background(), []Pair{
		{A: "retraite à 60 ans", DateA: "2022-01-01", B: "retraite à 62 ans"},
		{A: "le climat", B: "la pêche"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !keep[0] || keep[1] {
		t.Fatalf("want [true false], got %v", keep)
	}
	if auth.Load() != "Bearer k" {
		t.Fatalf("bearer token not sent: %v", auth.Load())
	}
}

func TestDecisionDownRestsThenFallsBack(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "no machine", http.StatusBadGateway)
	}))
	defer srv.Close()

	f := Fallback(NewDecision(srv.URL, "", DefaultDecisionThreshold), All{})
	for range 3 {
		keep, err := f.Keep(context.Background(), []Pair{{A: "a", B: "b"}})
		if err != nil || !keep[0] {
			t.Fatalf("a down model must keep every pair, got %v, %v", keep, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("a model that failed is not asked again at once: %d requests", calls.Load())
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("PAIR_FILTER_URL", "")
	f, err := FromEnv()
	if err != nil || f.Name() != "all" {
		t.Fatalf("no URL: want All, got %v, %v", f, err)
	}
	t.Setenv("PAIR_FILTER_URL", "http://127.0.0.1:1/v1/systemone")
	if f, err = FromEnv(); err != nil || f.Name() != "decision model, else all" {
		t.Fatalf("with a URL: got %v, %v", f, err)
	}
	t.Setenv("PAIR_FILTER_THRESHOLD", "2")
	if _, err = FromEnv(); err == nil {
		t.Fatal("a threshold of 2 must be refused")
	}
}
