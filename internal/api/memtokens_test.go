package api

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// TestMemTokensDisabled: a non-positive capacity yields a nil gate that admits
// everything without blocking.
func TestMemTokensDisabled(t *testing.T) {
	g := newMemoryTokens(0)
	if g != nil {
		t.Fatalf("capacity 0 should yield nil gate, got %v", g)
	}
	if !g.Acquire(context.Background(), 1<<20) {
		t.Fatal("nil gate Acquire should return true")
	}
	g.Release(1 << 20) // must not panic
}

// TestMemTokensGating: tokens are gated by capacity, freed on Release, and a job
// larger than the whole budget is clamped so it can still run alone.
func TestMemTokensGating(t *testing.T) {
	g := newMemoryTokens(100)

	if !g.Acquire(context.Background(), 60) {
		t.Fatal("first 60 should fit")
	}
	if g.Used() != 60 {
		t.Fatalf("used=%d want 60", g.Used())
	}

	// Second 60 does not fit (60+60 > 100): a cancelled ctx must return false
	// rather than block forever.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if g.Acquire(ctx, 60) {
		t.Fatal("second 60 over budget with cancelled ctx should return false")
	}
	if g.Used() != 60 {
		t.Fatalf("used=%d want 60 after rejected acquire", g.Used())
	}

	g.Release(60)
	if g.Used() != 0 {
		t.Fatalf("used=%d want 0 after release", g.Used())
	}

	// Oversized request clamps to capacity and runs alone.
	if !g.Acquire(context.Background(), 1000) {
		t.Fatal("oversized request should clamp and acquire")
	}
	if g.Used() != 100 {
		t.Fatalf("used=%d want 100 (clamped)", g.Used())
	}
	g.Release(1000)
	if g.Used() != 0 {
		t.Fatalf("used=%d want 0 after oversized release", g.Used())
	}
}

// TestMemTokensReleaseWakesWaiter: a blocked acquirer proceeds once capacity is
// freed by a Release.
func TestMemTokensReleaseWakesWaiter(t *testing.T) {
	g := newMemoryTokens(100)
	if !g.Acquire(context.Background(), 100) {
		t.Fatal("holder should acquire full budget")
	}

	got := make(chan bool, 1)
	go func() { got <- g.Acquire(context.Background(), 80) }()

	time.Sleep(20 * time.Millisecond) // let the waiter park
	g.Release(100)

	select {
	case ok := <-got:
		if !ok {
			t.Fatal("woken waiter should report acquired")
		}
	case <-time.After(time.Second):
		t.Fatal("Release did not wake the blocked acquirer")
	}
}

// TestMemTokensNoLeakUnderCancel stresses the Acquire cancel path against
// concurrent Releases: after everything quiesces, used must be 0 (no token leak).
func TestMemTokensNoLeakUnderCancel(t *testing.T) {
	const cap = 100
	g := newMemoryTokens(cap)
	if !g.Acquire(context.Background(), cap) {
		t.Fatal("holder should acquire full budget")
	}

	const workers = 8000
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				time.Sleep(time.Duration(rand.Intn(40)) * time.Microsecond)
				cancel()
			}()
			if g.Acquire(ctx, cap) {
				g.Release(cap)
			}
			cancel()
		}()
	}

	for i := 0; i < workers/4; i++ {
		g.Release(cap)
		g.Acquire(context.Background(), cap)
	}

	wg.Wait()
	g.Release(cap) // drop holder

	if u := g.Used(); u != 0 {
		t.Fatalf("token leak: used=%d, want 0", u)
	}
}
