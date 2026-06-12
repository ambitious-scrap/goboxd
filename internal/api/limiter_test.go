package api

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// TestNoSlotLeakUnderCancel stresses the Acquire wake/cancel race: many waiters
// park on a saturated limiter and have their context cancelled at a random moment
// that overlaps with Release waking them. The invariant is that every granted slot
// is eventually released — so once all goroutines quiesce, active must be 0.
//
// Before the fix, a waiter woken by wakeWaiters (active++) that then selected
// ctx.Done() returned false without releasing, leaking the slot; active drifted
// upward until it pinned the limit and admission stalled.
func TestNoSlotLeakUnderCancel(t *testing.T) {
	const limit = 2
	l := NewAdaptiveLimiter(limit, limit)

	// Hold one slot so most callers park, maximising the wake/cancel overlap.
	if !l.Acquire(context.Background()) {
		t.Fatal("initial Acquire should succeed")
	}

	const workers = 10000
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			// Cancel at a random sub-ms offset to straddle the wakeWaiters window.
			go func() {
				time.Sleep(time.Duration(rand.Intn(40)) * time.Microsecond)
				cancel()
			}()
			if l.Acquire(ctx) {
				l.Release()
			}
			cancel()
		}()
	}

	// Churn the held slot so wakeWaiters fires repeatedly against the cancels.
	for i := 0; i < workers/4; i++ {
		l.Release()
		l.Acquire(context.Background())
	}

	wg.Wait()
	l.Release() // drop the initial holder

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active != 0 {
		t.Fatalf("slot leak: active=%d, want 0", l.active)
	}
	if len(l.waiters) != 0 {
		t.Fatalf("dangling waiters: %d, want 0", len(l.waiters))
	}
}

// TestAcquireRespectsLimit verifies Acquire grants up to the limit and that a
// caller with an already-cancelled context does not get a slot once saturated.
func TestAcquireRespectsLimit(t *testing.T) {
	const limit = 3
	l := NewAdaptiveLimiter(limit, limit)

	for i := 0; i < limit; i++ {
		if !l.Acquire(context.Background()) {
			t.Fatalf("Acquire %d within limit should succeed", i)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.Acquire(ctx) {
		t.Fatal("Acquire beyond limit with cancelled ctx should return false")
	}

	l.mu.Lock()
	active := l.active
	waiters := len(l.waiters)
	l.mu.Unlock()
	if active != limit {
		t.Fatalf("active=%d, want %d", active, limit)
	}
	if waiters != 0 {
		t.Fatalf("waiters=%d, want 0 (cancelled waiter must deregister)", waiters)
	}
}

// TestReleaseWakesWaiter verifies a parked waiter is handed the slot on Release.
func TestReleaseWakesWaiter(t *testing.T) {
	l := NewAdaptiveLimiter(1, 1)
	if !l.Acquire(context.Background()) {
		t.Fatal("holder Acquire should succeed")
	}

	woke := make(chan bool, 1)
	go func() { woke <- l.Acquire(context.Background()) }()

	// Give the waiter time to park.
	time.Sleep(20 * time.Millisecond)
	l.Release()

	select {
	case ok := <-woke:
		if !ok {
			t.Fatal("woken waiter should report acquired")
		}
	case <-time.After(time.Second):
		t.Fatal("Release did not wake the parked waiter")
	}
}
