package contradiction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"
)

type fakeExplainStore struct {
	todo   []storage.ContradictionToExplain
	filter storage.ExplainFilter
	stored map[int64][2]string // id -> text, hash
}

func (f *fakeExplainStore) ListContradictionsToExplain(_ context.Context, version string, filter storage.ExplainFilter) ([]storage.ContradictionToExplain, error) {
	if version != promptrules.ExplanationVersion {
		return nil, fmt.Errorf("version %q", version)
	}
	f.filter = filter
	return f.todo, nil
}

func (f *fakeExplainStore) SetContradictionExplanation(_ context.Context, id int64, text, hash string) error {
	if f.stored == nil {
		f.stored = map[int64][2]string{}
	}
	f.stored[id] = [2]string{text, hash}
	return nil
}

type fakeExplainer func(c storage.ContradictionToExplain) (Explanation, error)

func (f fakeExplainer) Explain(_ context.Context, c storage.ContradictionToExplain) (Explanation, error) {
	return f(c)
}

func explainCase(id int64) storage.ContradictionToExplain {
	return storage.ContradictionToExplain{ID: id, Person: "Jean Test", Reason: "r", Hash: fmt.Sprintf("h%d", id),
		A: storage.ExplainSide{FactID: 1, Text: "a"}, B: storage.ExplainSide{FactID: 2, Text: "b"}}
}

// A run stores each text with the hash of what it was written from, counts
// the reply it could not use and leaves that contradiction for the next run,
// and reports the doubts.
func TestRunExplainStoresTextsAndReportsDoubts(t *testing.T) {
	store := &fakeExplainStore{todo: []storage.ContradictionToExplain{explainCase(1), explainCase(2), explainCase(3)}}
	ex := fakeExplainer(func(c storage.ContradictionToExplain) (Explanation, error) {
		switch c.ID {
		case 2:
			return Explanation{}, errors.New("no JSON")
		case 3:
			return Explanation{Text: "t3", Doubt: "les mots sont d'un autre"}, nil
		}
		return Explanation{Text: "t1"}, nil
	})
	var seen []int64
	res, err := RunExplain(context.Background(), store, ex, ExplainOptions{
		ExplainFilter: storage.ExplainFilter{Limit: 7},
		OnExplained:   func(c storage.ContradictionToExplain, _ Explanation) { seen = append(seen, c.ID) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 3 || res.Written != 2 || res.Failed != 1 {
		t.Errorf("result = %+v, want 3 pending, 2 written, 1 failed", res)
	}
	if store.filter.Limit != 7 {
		t.Errorf("filter = %+v, want the limit passed on", store.filter)
	}
	if got := store.stored[1]; got != [2]string{"t1", "h1"} {
		t.Errorf("stored[1] = %v", got)
	}
	if _, ok := store.stored[2]; ok {
		t.Error("the failed contradiction was stored")
	}
	if len(res.Doubts) != 1 || res.Doubts[0].ContradictionID != 3 {
		t.Errorf("doubts = %+v, want contradiction 3", res.Doubts)
	}
	if len(seen) != 2 {
		t.Errorf("OnExplained called for %v, want 2 calls", seen)
	}
}

// A dry run writes nothing; an unavailable LLM stops the run with its error.
func TestRunExplainDryRunAndUnavailable(t *testing.T) {
	store := &fakeExplainStore{todo: []storage.ContradictionToExplain{explainCase(1), explainCase(2)}}
	ok := fakeExplainer(func(storage.ContradictionToExplain) (Explanation, error) { return Explanation{Text: "t"}, nil })
	res, err := RunExplain(context.Background(), store, ok, ExplainOptions{DryRun: true})
	if err != nil || res.Written != 2 || len(store.stored) != 0 {
		t.Errorf("dry run: res %+v, err %v, stored %v; want 2 drafted, nothing stored", res, err, store.stored)
	}

	down := fakeExplainer(func(storage.ContradictionToExplain) (Explanation, error) {
		return Explanation{}, fmt.Errorf("call: %w", llm.ErrUnavailable)
	})
	res, err = RunExplain(context.Background(), store, down, ExplainOptions{})
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v, want llm.ErrUnavailable", err)
	}
	if res.Written != 0 || len(store.stored) != 0 {
		t.Errorf("res %+v, stored %v; want nothing written", res, store.stored)
	}
}

// The prompt gives the earlier fact first whatever the order of the pair,
// each with its date and the quote of its source, then the judge's reason.
func TestExplainPromptOrdersByDateAndCarriesQuotes(t *testing.T) {
	d2021 := time.Date(2021, 10, 15, 0, 0, 0, 0, time.UTC)
	d2026 := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	c := storage.ContradictionToExplain{
		Person: "jean-luc melenchon",
		Reason: "Il avait dit que la campagne de 2022 serait la dernière.",
		A: storage.ExplainSide{Text: "Il a officialisé sa quatrième candidature.", Date: &d2026,
			Quote: "il a officialisé dimanche soir sa quatrième candidature", SourceURL: "https://www.letemps.ch/a"},
		B: storage.ExplainSide{Text: "Il a affirmé que cette campagne est sa dernière.", Date: &d2021,
			Quote: "« Sur mon nom ? Je crois, oui. »", SourceURL: "https://www.lemonde.fr/b"},
	}
	p := ExplainPrompt(c)
	earlier, later := strings.Index(p, "cette campagne est sa dernière"), strings.Index(p, "quatrième candidature.")
	if earlier < 0 || later < 0 || earlier > later {
		t.Errorf("the 2021 fact (at %d) must come before the 2026 fact (at %d)", earlier, later)
	}
	for _, want := range []string{"October 15, 2021", "May 3, 2026", "Excerpt of lemonde.fr", "Excerpt of letemps.ch",
		"« Sur mon nom ? Je crois, oui. »", "la campagne de 2022 serait la dernière", "jean-luc melenchon"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "%!") {
		t.Errorf("prompt has a formatting error:\n%s", p)
	}

	// A fact without a quote gets no excerpt line.
	c.A.Quote = ""
	if p := ExplainPrompt(c); strings.Contains(p, "Excerpt of letemps.ch") {
		t.Error("a fact without a quote must not have an excerpt line")
	}
}

// The writer reads the passage of the article around a fact's excerpt, when
// the article is stored: the question the quoted words answer is there.
func TestExplainPromptCarriesThePassage(t *testing.T) {
	c := explainCase(1)
	c.A.Quote = "Non, il faut que la droite européenne du PPE lâche le pouvoir"
	c.A.Article = "Désignez votre principal adversaire. Le RN.\nAu vu de son bilan, Ursula von der Leyen mérite-t-elle d'être reconduite?\nNon, il faut que la droite européenne du PPE lâche le pouvoir après deux décennies."
	p := ExplainPrompt(c)
	if !strings.Contains(p, "The passage of the article around this excerpt") || !strings.Contains(p, "mérite-t-elle d'être reconduite? Non, il faut") {
		t.Errorf("the prompt lacks the passage with the question:\n%s", p)
	}
	// An article that does not hold the excerpt adds nothing.
	c.A.Article = "Un autre article."
	if strings.Contains(ExplainPrompt(c), "The passage of the article") {
		t.Error("a passage was given for an excerpt the article does not hold")
	}
}
