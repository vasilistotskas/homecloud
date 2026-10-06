package parallel

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestForEachVisitsEveryItemWithinTheBound(t *testing.T) {
	const workers = 3
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	var (
		mu      sync.Mutex
		seen    = map[int]bool{}
		running atomic.Int32
		peak    atomic.Int32
	)
	ForEach(context.Background(), items, workers, func(i int) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		mu.Lock()
		seen[i] = true
		mu.Unlock()
	})
	if len(seen) != len(items) {
		t.Fatalf("visited %d items, want %d", len(seen), len(items))
	}
	if p := peak.Load(); p > workers {
		t.Fatalf("%d calls ran at once, want at most %d", p, workers)
	}
}

func TestForEachStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	ForEach(ctx, make([]int, 100), 1, func(int) {
		if calls.Add(1) == 5 {
			cancel()
		}
	})
	// The item handed out while the fifth call ran may still run.
	if n := calls.Load(); n > 6 {
		t.Fatalf("%d calls after cancelling at the fifth", n)
	}
}
