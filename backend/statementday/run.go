package statementday

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend/llm"
)

// Pending is a stored fact whose statement date is a year or a month and
// whose day was never searched.
type Pending struct {
	FactID int64
	Statement
}

// Store is what the run needs of the storage.
type Store interface {
	// ListFactsForStatementDay returns up to limit facts to search, those of
	// a contradiction first.
	ListFactsForStatementDay(ctx context.Context, candidatesOnly bool, limit int, again bool) ([]Pending, error)
	// SetStatementDay records the search of a fact: the day found, or nil
	// (the fact keeps its year or month and is not searched again).
	SetStatementDay(ctx context.Context, factID int64, day *time.Time) error
}

// Options of a run.
type Options struct {
	// Limit is the most facts searched (0: no limit).
	Limit int
	// Apply writes the days found; without it the run only reports.
	Apply bool
	// Everyone searches the facts of every person, not only the candidates'.
	Everyone bool
	// Again also searches the facts an earlier run found no day for.
	Again bool
	// Report is called with each fact searched.
	Report func(p Pending, f Found)
}

// Result of a run.
type Result struct {
	Searched, Found, Failed int
}

// Run searches the day of the facts known to the year or the month.
func Run(ctx context.Context, store Store, finder *Finder, opts Options) (Result, error) {
	var res Result
	var mu sync.Mutex
	var lastErr error
	done := map[int64]bool{}
	for {
		batch := 200
		if opts.Limit > 0 && opts.Limit-len(done) < batch {
			batch = opts.Limit - len(done)
		}
		if batch <= 0 {
			break
		}
		// A fact searched again keeps its place in the list (--again): ask
		// for those of this run too, and leave them out.
		listed, err := store.ListFactsForStatementDay(ctx, !opts.Everyone, batch+len(done), opts.Again)
		if err != nil {
			return res, err
		}
		var facts []Pending
		for _, p := range listed {
			if !done[p.FactID] && len(facts) < batch {
				facts = append(facts, p)
			}
		}
		if len(facts) == 0 {
			break
		}
		before := res.Searched
		err = llm.ForEach(ctx, facts, func(ctx context.Context, p Pending) error {
			found, err := finder.Find(ctx, p.Statement)
			mu.Lock()
			done[p.FactID] = true
			if err != nil {
				res.Failed++
				lastErr = err
				mu.Unlock()
				log.Warn().Err(err).Int64("fact_id", p.FactID).Msg("statement day: the fact stays unsearched")
				return nil
			}
			res.Searched++
			if found.Day != nil {
				res.Found++
			}
			if opts.Report != nil {
				opts.Report(p, found)
			}
			mu.Unlock()
			if !opts.Apply {
				return nil
			}
			return store.SetStatementDay(ctx, p.FactID, found.Day)
		})
		if err != nil {
			return res, err
		}
		// A batch where no search succeeded (the LLM or the search is out)
		// would fail the same way on the next: stop, a later run goes on.
		if res.Searched == before {
			return res, fmt.Errorf("statement days: no fact of a batch of %d could be searched: %w", len(facts), lastErr)
		}
	}
	return res, nil
}
