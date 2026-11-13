// Package digest sends the daily message of the worker: four lines a person
// can act on. The new facts, the new contradictions, the ballots of the
// vote, and whether something needs them now. It replaces the alerts that
// fired without telling what to do.
package digest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Job is the job_metrics job of the digests sent: its rows say when the last
// one went out and what it counted.
const Job = "digest"

// Store is what the digest reads and records.
type Store interface {
	// DigestCounts counts what happened since a time.
	DigestCounts(ctx context.Context, since time.Time) (Counts, error)
	// LastJobAt is when a job last recorded a row (ok false: never).
	LastJobAt(ctx context.Context, job string) (at time.Time, ok bool, err error)
	// LastDigest is when the last digest went out and the ballots it counted.
	LastDigest(ctx context.Context) (at time.Time, ballots int, ok bool, err error)
	// RecordDigest keeps a digest as sent.
	RecordDigest(ctx context.Context, c Counts, ballots int) error
}

// Counts is what happened over the period of a digest.
type Counts struct {
	Facts, Contradictions    int
	FailedTasks, FailedPosts int
}

// Digest sends one message a day at Hour:Min of Zone.
type Digest struct {
	Store Store
	Send  func(ctx context.Context, message string) error
	// Ballots gives the ballots the vote counted so far (ok false: no open
	// vote); nil: no vote line.
	Ballots   func(ctx context.Context) (counted int, ok bool, err error)
	Hour, Min int
	Zone      *time.Location
	// MaintenanceEvery is the longest the maintenance pass waits while tasks
	// are pending: twice that without a pass ended is a pass that is stuck.
	MaintenanceEvery time.Duration
	// Errors counts the errors the process logged (nil: not counted).
	Errors *ErrorCounter
	Now    func() time.Time
}

// ErrorsWorthALook is how many errors logged over the period make a line of
// the digest: under it they are the day's ordinary failures (a page that
// does not answer, a call tried again).
const ErrorsWorthALook = 50

// Run sends the digests until ctx ends.
func (d *Digest) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := d.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("digest: tick failed")
		}
		select {
		case <-time.After(time.Minute):
		case <-ctx.Done():
		}
	}
}

// Tick sends today's digest once its time has come and none went out since.
func (d *Digest) Tick(ctx context.Context) error {
	now := d.Now()
	local := now.In(d.Zone)
	due := time.Date(local.Year(), local.Month(), local.Day(), d.Hour, d.Min, 0, 0, d.Zone)
	if now.Before(due) {
		return nil
	}
	last, before, sent, err := d.Store.LastDigest(ctx)
	if err != nil {
		return err
	}
	if sent && !last.Before(due) {
		return nil
	}
	since := now.Add(-24 * time.Hour)
	if sent && last.After(since) {
		since = last
	}
	counts, err := d.Store.DigestCounts(ctx, since)
	if err != nil {
		return err
	}
	r := Report{Day: local, Counts: counts}
	ballots := before
	if d.Ballots != nil {
		n, ok, err := d.Ballots(ctx)
		switch {
		case err != nil:
			log.Warn().Err(err).Msg("digest: the ballots of the vote could not be read")
			r.Attention = append(r.Attention, "le décompte du vote est illisible")
		case ok:
			r.Vote, r.Ballots, ballots = true, n, n
			if sent {
				r.NewBallots = n - before
			}
		}
	}
	attention, err := d.attention(ctx, now, counts)
	if err != nil {
		return err
	}
	r.Attention = append(r.Attention, attention...)
	if err := d.Send(ctx, r.Message()); err != nil {
		return fmt.Errorf("send the digest: %w", err)
	}
	if d.Errors != nil {
		d.Errors.Reset()
	}
	log.Info().Int("facts", counts.Facts).Int("contradictions", counts.Contradictions).Int("ballots", ballots).Strs("attention", r.Attention).Msg("digest: sent")
	return d.Store.RecordDigest(ctx, counts, ballots)
}

// attention lists what needs a person now, each in a few words.
func (d *Digest) attention(ctx context.Context, now time.Time, c Counts) ([]string, error) {
	var out []string
	if d.MaintenanceEvery > 0 {
		at, ok, err := d.Store.LastJobAt(ctx, "maintenance")
		if err != nil {
			return nil, err
		}
		if ok && now.Sub(at) > 2*d.MaintenanceEvery {
			out = append(out, fmt.Sprintf("maintenance bloquée depuis %d h", int(now.Sub(at).Hours())))
		}
	}
	// The feed runs after 03:00 UTC and has ended by 06:00.
	day := now.UTC().Truncate(24 * time.Hour)
	if now.After(day.Add(6 * time.Hour)) {
		at, ok, err := d.Store.LastJobAt(ctx, "feed")
		if err != nil {
			return nil, err
		}
		if ok && at.Before(day.Add(3*time.Hour)) {
			out = append(out, "le flux du jour n'a pas tourné")
		}
	}
	if c.FailedTasks > 0 {
		out = append(out, fmt.Sprintf("%d collecte(s) en échec", c.FailedTasks))
	}
	if c.FailedPosts > 0 {
		out = append(out, fmt.Sprintf("%d tweet(s) non publié(s)", c.FailedPosts))
	}
	if d.Errors != nil {
		if n := d.Errors.Count(); n >= ErrorsWorthALook {
			out = append(out, fmt.Sprintf("%d erreurs dans les logs", n))
		}
	}
	return out, nil
}

// Report is a digest before it is written.
type Report struct {
	Day    time.Time
	Counts Counts
	// Vote: a vote is open; Ballots its ballots counted, NewBallots those
	// since the last digest.
	Vote                bool
	Ballots, NewBallots int
	// Attention is what needs a person now (empty: nothing).
	Attention []string
}

// Message is the digest: four lines.
func (r Report) Message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "📋 <b>Bilan du %s</b>\n", r.Day.Format("02/01"))
	fmt.Fprintf(&b, "Faits : +%d\n", r.Counts.Facts)
	fmt.Fprintf(&b, "Contradictions : +%d\n", r.Counts.Contradictions)
	if r.Vote {
		fmt.Fprintf(&b, "Vote : %d bulletins (%+d)\n", r.Ballots, r.NewBallots)
	} else {
		b.WriteString("Vote : aucun vote ouvert\n")
	}
	if len(r.Attention) == 0 {
		b.WriteString("✅ Rien d'urgent")
	} else {
		b.WriteString("⚠️ À regarder : " + strings.Join(r.Attention, " ; "))
	}
	return b.String()
}

// ErrorCounter counts the errors a process logs: a zerolog hook.
type ErrorCounter struct {
	mu sync.Mutex
	n  int
}

// Run counts an event logged at the error level or above.
func (c *ErrorCounter) Run(_ *zerolog.Event, level zerolog.Level, _ string) {
	if level < zerolog.ErrorLevel || level > zerolog.PanicLevel {
		return
	}
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

// Count is how many errors were logged since the last Reset.
func (c *ErrorCounter) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// Reset starts the count again.
func (c *ErrorCounter) Reset() {
	c.mu.Lock()
	c.n = 0
	c.mu.Unlock()
}
