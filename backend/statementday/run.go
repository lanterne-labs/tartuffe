package statementday

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend"
	"digital-mirror/backend/llm"
)

// Pending is a stored fact whose statement date is a year or a month and
// whose day was never searched.
type Pending struct {
	FactID   int64
	PersonID int64
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
	// QueueArticle stores the page that dated a statement of the person as
	// a news page to extract, unless its URL is already known: the news
	// collection of the person extracts its facts.
	QueueArticle(ctx context.Context, personID int64, person string, page Page) (queued bool, err error)
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
	// Queued is how many dating articles were queued for extraction.
	Queued int
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
			queued, err := record(ctx, store, p, found)
			if queued {
				mu.Lock()
				res.Queued++
				mu.Unlock()
			}
			return err
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

// articleOf returns the page that dated a statement when it is an article
// worth extracting, dated at least by the day found.
func articleOf(f Found) *Page {
	if f.Day == nil || f.Page == nil || !isArticleURL(f.Page.URL) || len([]rune(strings.TrimSpace(f.Page.Text))) < ArticleMinRunes {
		return nil
	}
	page := *f.Page
	if page.Published == nil {
		day := f.Day.UTC()
		page.Published = &day
	}
	return &page
}

// record stores the result of a search: the day (or that none was found),
// then the article that gave it, queued for the extraction of its facts.
func record(ctx context.Context, store Store, p Pending, found Found) (queued bool, err error) {
	if err := store.SetStatementDay(ctx, p.FactID, found.Day); err != nil {
		return false, err
	}
	page := articleOf(found)
	if page == nil {
		return false, nil
	}
	queued, err = store.QueueArticle(ctx, p.PersonID, p.Person, *page)
	if err != nil {
		// The day is stored; the article is a bonus.
		log.Warn().Err(err).Int64("fact_id", p.FactID).Str("url", page.URL).Msg("statement day: the dating article could not be queued")
		return false, nil
	}
	return queued, nil
}

// Sharpener searches the day of one stored fact and records it: the hook of
// the fact inserter (core.StatementDayFinder), asked right after a fact
// dated to a year or a month is stored. The finder is built at the first
// fact that needs it.
type Sharpener struct {
	store Store
	build func() (*Finder, error)

	once   sync.Once
	finder *Finder
	err    error
}

// NewSharpener builds a sharpener on a store and a way to build the finder.
func NewSharpener(store Store, build func() (*Finder, error)) *Sharpener {
	return &Sharpener{store: store, build: build}
}

// FindStatementDay implements core.StatementDayFinder.
func (s *Sharpener) FindStatementDay(ctx context.Context, factID, personID int64, person, text, quote string, known time.Time, source *time.Time) error {
	s.once.Do(func() { s.finder, s.err = s.build() })
	if s.err != nil {
		return fmt.Errorf("statement day finder unavailable: %w", s.err)
	}
	p := Pending{FactID: factID, PersonID: personID, Statement: Statement{Person: person, Text: text, Quote: quote, Known: known, SourceDate: source}}
	found, err := s.finder.Find(ctx, p.Statement)
	if err != nil {
		return err
	}
	queued, err := record(ctx, s.store, p, found)
	if err != nil {
		return err
	}
	log.Info().Int64("fact_id", factID).Str("known", backend.FormatFactDateISO(&known)).Str("day", backend.FormatFactDateISO(found.Day)).
		Str("url", found.URL).Bool("article_queued", queued).Msg("Statement day searched at insertion")
	return nil
}
