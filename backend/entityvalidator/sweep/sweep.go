// Package sweep runs the entity validator over the existing persons table:
// the `dm entities validate` driver. It lives next to, not inside,
// entityvalidator because it needs storage types and storage already imports
// the validator.
//
// Every validated row costs one Wikipedia lookup, one Serper query and one LLM
// call, so the has-facts guard (Options.OnlyWithoutFacts) is what keeps a
// full-table run affordable.
package sweep

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"digital-mirror/backend/entityvalidator"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// Storer is the storage surface the sweep uses.
type Storer interface {
	GetAllPersons(ctx context.Context) ([]storage.PersonInfo, error)
	GetFactCountsByPersonIDs(ctx context.Context, ids []int64) (map[int64]int, error)
	DeletePersonByID(ctx context.Context, id int64) error
}

// Options scopes one sweep.
type Options struct {
	// Concurrency is the number of persons validated in parallel (min 1).
	Concurrency int
	// Limit validates only the first N rows (alphabetical); 0 = no limit.
	Limit int
	// OnlyWithoutFacts skips persons that already have facts attached (they
	// are presumed validated by prior use).
	OnlyWithoutFacts bool
}

// Row is the per-person outcome, also the JSONL report line.
type Row struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	EntityType string `json:"entity_type"`
	Real       bool   `json:"real"`
	Filter     string `json:"filter"`
	Reason     string `json:"reason"`
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// Report is the outcome of one sweep. Rows follows the persons order.
type Report struct {
	Rows      []Row
	Elapsed   time.Duration
	Validated int // rows that went through the validator
	Approved  int
	Rejected  int
	Skipped   int // has-facts guard
}

// RejectedRows returns the rows the validator rejected (skipped rows excluded).
func (r Report) RejectedRows() []Row {
	out := make([]Row, 0, r.Rejected)
	for _, row := range r.Rows {
		if !row.Skipped && !row.Real {
			out = append(out, row)
		}
	}
	return out
}

// ValidatorFromEnv is the validator the sweep uses: the same chain the
// extractor's pre-insert guard runs, but with DefaultDecision tuned for
// cleanup (Reject on unknown, i.e. when every filter defers the candidate is
// treated as suspect rather than safe).
func ValidatorFromEnv() *entityvalidator.Validator {
	v := entityvalidator.BuildFromEnv()
	v.DefaultDecision = entityvalidator.Reject
	return v
}

// Run validates every person through v and returns the report. Nothing is
// deleted; pass Report.RejectedRows to DeleteRejected for that.
func Run(ctx context.Context, storer Storer, v *entityvalidator.Validator, opts Options) (Report, error) {
	persons, err := storer.GetAllPersons(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list persons: %w", err)
	}
	if opts.Limit > 0 && opts.Limit < len(persons) {
		persons = persons[:opts.Limit]
	}
	log.Info().Int("count", len(persons)).Msg("Validating persons through wiki+LLM pipeline")

	// fact-count skip: hit the DB once for the whole list rather than per-row.
	// A failure here must abort: falling back to "everyone has zero facts"
	// would validate the whole table and burn the search and LLM credits the
	// skip exists to protect.
	factCounts := map[int64]int{}
	if opts.OnlyWithoutFacts {
		var err error
		if factCounts, err = collectFactCounts(ctx, storer, persons); err != nil {
			return Report{}, err
		}
	}

	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	type job struct {
		idx    int
		person storage.PersonInfo
	}
	jobs := make(chan job)
	rows := make([]Row, len(persons))
	var (
		wg        sync.WaitGroup
		nApproved atomic.Int64
		nRejected atomic.Int64
		nSkipped  atomic.Int64
		nFiltered atomic.Int64
	)
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				row := Row{
					ID:         j.person.ID,
					Name:       j.person.Name,
					EntityType: j.person.EntityType,
				}
				if opts.OnlyWithoutFacts && factCounts[j.person.ID] > 0 {
					row.Skipped = true
					row.SkipReason = fmt.Sprintf("has %d existing facts", factCounts[j.person.ID])
					rows[j.idx] = row
					nSkipped.Add(1)
					continue
				}
				verdict := v.Validate(ctx, j.person.Name, j.person.EntityType)
				row.Real = verdict.Real
				row.Filter = verdict.Filter
				row.Reason = verdict.Reason
				rows[j.idx] = row
				if verdict.Real {
					nApproved.Add(1)
				} else {
					nRejected.Add(1)
				}
				nFiltered.Add(1)
			}
		}()
	}
	t0 := time.Now()
	for i, p := range persons {
		jobs <- job{idx: i, person: p}
	}
	close(jobs)
	wg.Wait()

	return Report{
		Rows:      rows,
		Elapsed:   time.Since(t0),
		Validated: int(nFiltered.Load()),
		Approved:  int(nApproved.Load()),
		Rejected:  int(nRejected.Load()),
		Skipped:   int(nSkipped.Load()),
	}, nil
}

// collectFactCounts batches a single query for fact counts of all candidate IDs.
func collectFactCounts(ctx context.Context, s Storer, persons []storage.PersonInfo) (map[int64]int, error) {
	ids := make([]int64, len(persons))
	for i, p := range persons {
		ids[i] = p.ID
	}
	counts, err := s.GetFactCountsByPersonIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("loading fact counts for the has-facts skip: %w", err)
	}
	return counts, nil
}

// DeleteStats counts the outcome of DeleteRejected.
type DeleteStats struct {
	Deleted int
	Failed  int
}

// DeleteRejected deletes the given rows from the persons table one by one;
// a failed delete is logged and counted, the rest continue.
func DeleteRejected(ctx context.Context, storer Storer, rows []Row) DeleteStats {
	var stats DeleteStats
	for _, r := range rows {
		if err := storer.DeletePersonByID(ctx, r.ID); err != nil {
			stats.Failed++
			log.Error().Err(err).Int64("id", r.ID).Str("name", r.Name).Msg("delete failed")
			continue
		}
		stats.Deleted++
	}
	return stats
}

// WriteJSONL writes one JSON object per row to path.
func WriteJSONL(path string, rows []Row) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create report file %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("write report row %d: %w", r.ID, err)
		}
	}
	log.Info().Str("path", path).Int("rows", len(rows)).Msg("Wrote validation report")
	return nil
}
