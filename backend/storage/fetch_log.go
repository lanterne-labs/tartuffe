package storage

import (
	"context"
	"sync"
	"time"

	"digital-mirror/backend/webfetch"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

const (
	fetchLogFlushEvery = 5 * time.Second
	fetchLogBatch      = 500
	fetchLogBuffer     = 20000
	fetchLogKeep       = 90 * 24 * time.Hour
)

// FetchLog is the webfetch.Recorder of the processes: the outcomes are
// written to fetch_log in batches, off the request path. When the buffer is
// full (the database is down) the outcomes are dropped and counted in the
// log, never blocking a fetch.
type FetchLog struct {
	pool    *pgxpool.Pool
	ch      chan webfetch.Outcome
	mu      sync.Mutex
	dropped int
}

// NewFetchLog starts the writer of the outcomes; it stops with ctx.
func NewFetchLog(ctx context.Context, s *PostgresStorer) *FetchLog {
	f := &FetchLog{pool: s.Pool(), ch: make(chan webfetch.Outcome, fetchLogBuffer)}
	go f.run(ctx)
	return f
}

// Record implements webfetch.Recorder.
func (f *FetchLog) Record(o webfetch.Outcome) {
	select {
	case f.ch <- o:
	default:
		f.mu.Lock()
		f.dropped++
		f.mu.Unlock()
	}
}

func (f *FetchLog) run(ctx context.Context) {
	tick := time.NewTicker(fetchLogFlushEvery)
	defer tick.Stop()
	var batch []webfetch.Outcome
	lastPrune := time.Time{}
	for {
		select {
		case o := <-f.ch:
			if batch = append(batch, o); len(batch) >= fetchLogBatch {
				batch = f.flush(ctx, batch)
			}
		case <-tick.C:
			batch = f.flush(ctx, batch)
			if time.Since(lastPrune) > 24*time.Hour {
				lastPrune = time.Now()
				if _, err := f.pool.Exec(ctx, `DELETE FROM fetch_log WHERE at < $1`, time.Now().Add(-fetchLogKeep)); err != nil {
					log.Warn().Err(err).Msg("fetch log: pruning failed")
				}
			}
		case <-ctx.Done():
			for len(f.ch) > 0 {
				batch = append(batch, <-f.ch)
			}
			f.flush(context.WithoutCancel(ctx), batch)
			return
		}
	}
}

func (f *FetchLog) flush(ctx context.Context, batch []webfetch.Outcome) []webfetch.Outcome {
	f.mu.Lock()
	if f.dropped > 0 {
		log.Warn().Int("dropped", f.dropped).Msg("fetch log: outcomes dropped, the buffer was full")
		f.dropped = 0
	}
	f.mu.Unlock()
	if len(batch) == 0 {
		return batch
	}
	rows := make([][]any, len(batch))
	for i, o := range batch {
		rows[i] = []any{o.At, o.Source, webfetch.Domain(o.Host), o.Host, o.Status, o.Outcome, o.Note, int(o.Duration / time.Millisecond)}
	}
	_, err := f.pool.CopyFrom(ctx, pgx.Identifier{"fetch_log"},
		[]string{"at", "source", "domain", "host", "status", "outcome", "note", "duration_ms"}, pgx.CopyFromRows(rows))
	if err != nil {
		log.Warn().Err(err).Int("rows", len(batch)).Msg("fetch log: writing failed; the outcomes are dropped")
	}
	return batch[:0]
}
