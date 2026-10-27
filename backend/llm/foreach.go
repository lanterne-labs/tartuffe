package llm

import (
	"context"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

// ForEach runs fn on each item, as many at once as the endpoint of the active
// routing serves (Concurrency): on an endpoint that serves one call, one item
// at a time, in order. The items start in order. After the first error no new
// item starts, and ForEach returns that error once the running items finish.
// ForEach does not read ctx itself: fn gets it and decides, as a serial loop
// would.
//
// Every caller whose work is LLM calls goes through ForEach, so the endpoint
// alone decides how much runs at once.
func ForEach[T any](ctx context.Context, items []T, fn func(ctx context.Context, item T) error) error {
	n := Concurrency()
	if n <= 1 || len(items) <= 1 {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}
	var g errgroup.Group
	g.SetLimit(n)
	var failed atomic.Bool
	for _, item := range items {
		if failed.Load() {
			break
		}
		g.Go(func() error {
			err := fn(ctx, item)
			if err != nil {
				failed.Store(true)
			}
			return err
		})
	}
	return g.Wait()
}
