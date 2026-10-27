package webfetch

import (
	"context"
	"sync"
	"time"
)

// Pace spaces the requests to one site: at most Rate a second and Parallel at
// once. A site that says to slow down (Slow) halves the rate and is left
// alone for the time it asks; the rate then climbs back by a quarter every
// ten requests that pass (Fine), up to Rate.
type Pace struct {
	Rate     float64
	Parallel int

	mu      sync.Mutex
	cur     float64
	next    time.Time
	passed  int
	slots   chan struct{}
	initOne sync.Once
}

func (p *Pace) init() {
	p.initOne.Do(func() {
		if p.Rate <= 0 {
			p.Rate = 1
		}
		if p.Parallel <= 0 {
			p.Parallel = 1
		}
		p.cur = p.Rate
		p.slots = make(chan struct{}, p.Parallel)
	})
}

// Wait blocks until a request may start, and returns the release to call
// when it ends.
func (p *Pace) Wait(ctx context.Context) (func(), error) {
	p.init()
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-p.slots }
	p.mu.Lock()
	now := time.Now()
	at := p.next
	if at.Before(now) {
		at = now
	}
	p.next = at.Add(time.Duration(float64(time.Second) / p.cur))
	p.mu.Unlock()
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}

// Slow halves the rate (down to a sixteenth of Rate) and holds the next
// request for at least d.
func (p *Pace) Slow(d time.Duration) {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur /= 2
	if min := p.Rate / 16; p.cur < min {
		p.cur = min
	}
	p.passed = 0
	if d <= 0 {
		d = time.Duration(float64(time.Second) / p.cur)
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	if at := time.Now().Add(d); at.After(p.next) {
		p.next = at
	}
}

// Fine counts a request that passed.
func (p *Pace) Fine() {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur >= p.Rate {
		return
	}
	if p.passed++; p.passed >= 10 {
		p.passed = 0
		p.cur *= 1.25
		if p.cur > p.Rate {
			p.cur = p.Rate
		}
	}
}

// Current is the rate the pace is at.
func (p *Pace) Current() float64 {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur
}

// Paces keeps one Pace per key (a domain), made by New on first use.
type Paces struct {
	New func() *Pace
	mu  sync.Mutex
	m   map[string]*Pace
}

// Of returns the pace of key.
func (ps *Paces) Of(key string) *Pace {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.m == nil {
		ps.m = map[string]*Pace{}
	}
	p, ok := ps.m[key]
	if !ok {
		p = ps.New()
		ps.m[key] = p
	}
	return p
}

// Paced runs one fetch under pace, with up to tries attempts: a failure that
// can pass later (Retryable) slows the pace, for the Retry-After the site
// gives, before the next attempt. A fetch that passes counts as fine.
func Paced(ctx context.Context, pace *Pace, tries int, fetch func(ctx context.Context) (*Page, error)) (*Page, error) {
	var p *Page
	var err error
	for i := 0; i < tries; i++ {
		release, werr := pace.Wait(ctx)
		if werr != nil {
			return nil, werr
		}
		p, err = fetch(ctx)
		release()
		if err == nil {
			pace.Fine()
			return p, nil
		}
		if !Retryable(err) || ctx.Err() != nil {
			return p, err
		}
		pace.Slow(RetryAfter(p))
	}
	return p, err
}
