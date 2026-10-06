// Package parallel runs work on a bounded number of goroutines.
package parallel

import (
	"context"
	"sync"
)

// ForEach calls fn for each item on workers goroutines, stops handing out
// items once ctx ends, and returns when every started call has returned.
func ForEach[T any](ctx context.Context, items []T, workers int, fn func(T)) {
	work := make(chan T)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for it := range work {
				fn(it)
			}
		})
	}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		work <- it
	}
	close(work)
	wg.Wait()
}
