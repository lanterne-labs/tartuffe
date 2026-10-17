package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestMajorityVote(t *testing.T) {
	id := func(s string) string { return s }

	t.Run("single vote makes exactly one call", func(t *testing.T) {
		calls := 0
		got, err := MajorityVote(1, func() (string, error) { calls++; return "keep", nil }, id)
		if err != nil || got != "keep" {
			t.Fatalf("got (%q, %v), want (keep, nil)", got, err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (votes<=1 must not add cost)", calls)
		}
	})

	t.Run("majority wins and short-circuits", func(t *testing.T) {
		seq := []string{"drop", "drop", "keep"}
		calls := 0
		got, err := MajorityVote(3, func() (string, error) {
			v := seq[calls]
			calls++
			return v, nil
		}, id)
		if err != nil || got != "drop" {
			t.Fatalf("got (%q, %v), want (drop, nil)", got, err)
		}
		if calls != 2 {
			t.Errorf("calls = %d, want 2 — the third is pointless once two agree", calls)
		}
	})

	t.Run("minority is outvoted", func(t *testing.T) {
		seq := []string{"keep", "drop", "drop"}
		calls := 0
		got, _ := MajorityVote(3, func() (string, error) {
			v := seq[calls]
			calls++
			return v, nil
		}, id)
		if got != "drop" {
			t.Errorf("got %q, want drop (2 of 3)", got)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3 — no majority existed until the last vote", calls)
		}
	})

	t.Run("errors cast no vote", func(t *testing.T) {
		seq := []struct {
			v   string
			err error
		}{{"", errors.New("boom")}, {"keep", nil}, {"keep", nil}}
		calls := 0
		got, err := MajorityVote(3, func() (string, error) {
			s := seq[calls]
			calls++
			return s.v, s.err
		}, id)
		if err != nil || got != "keep" {
			t.Fatalf("got (%q, %v), want (keep, nil) — a failed call must not vote", got, err)
		}
	})

	t.Run("all errors surface the last one", func(t *testing.T) {
		want := errors.New("third")
		seq := []error{errors.New("first"), errors.New("second"), want}
		calls := 0
		_, err := MajorityVote(3, func() (string, error) {
			e := seq[calls]
			calls++
			return "", e
		}, id)
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("three-way split resolves to the first seen, not map order", func(t *testing.T) {
		for i := 0; i < 20; i++ { // repeat: a map-order bug would show intermittently
			seq := []string{"a", "b", "c"}
			calls := 0
			got, _ := MajorityVote(3, func() (string, error) {
				v := seq[calls]
				calls++
				return v, nil
			}, id)
			if got != "a" {
				t.Fatalf("got %q, want a (deterministic tie-break)", got)
			}
		}
	})
	t.Run("an unavailable LLM ends the vote without a verdict", func(t *testing.T) {
		// keep, then the LLM goes away: deciding "keep" on 1 of 3 samples is
		// a reduced-quorum verdict, so the vote must fail instead.
		seq := []struct {
			v   string
			err error
		}{{"keep", nil}, {"", fmt.Errorf("%w: primary down", ErrUnavailable)}, {"keep", nil}}
		calls := 0
		got, err := MajorityVote(3, func() (string, error) {
			s := seq[calls]
			calls++
			return s.v, s.err
		}, id)
		if !errors.Is(err, ErrUnavailable) || got != "" {
			t.Fatalf("got (%q, %v), want (\"\", ErrUnavailable)", got, err)
		}
		if calls != 2 {
			t.Errorf("calls = %d, want 2 — no sample is drawn after the LLM went away", calls)
		}
	})

	t.Run("a cut context ends the vote without a verdict", func(t *testing.T) {
		calls := 0
		_, err := MajorityVote(3, func() (string, error) {
			calls++
			return "", fmt.Errorf("claude team: %w", context.DeadlineExceeded)
		}, id)
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
			t.Fatalf("err = %v after %d calls; want the deadline after 1 call", err, calls)
		}
	})
}
