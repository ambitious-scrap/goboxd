package api

import (
	"context"
	"sync"
)

type memoryTokens struct {
	mu       sync.Mutex
	cond     *sync.Cond
	capacity int
	used     int
}

func newMemoryTokens(capacity int) *memoryTokens {
	if capacity <= 0 {
		return nil
	}

	g := &memoryTokens{capacity: capacity}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *memoryTokens) Acquire(ctx context.Context, tokens int) bool {
	if g == nil || tokens <= 0 {
		return true
	}

	tokens = g.clamp(tokens)

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.used+tokens <= g.capacity {
		g.used += tokens
		return true
	}

	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.cond.Broadcast()
		g.mu.Unlock()
	})
	defer stop()

	for g.used+tokens > g.capacity {
		if ctx.Err() != nil {
			return false
		}
		g.cond.Wait()
	}

	g.used += tokens
	return true
}

func (g *memoryTokens) Release(tokens int) {
	if g == nil || tokens <= 0 {
		return
	}

	tokens = g.clamp(tokens)

	g.mu.Lock()
	g.used -= tokens
	if g.used < 0 {
		g.used = 0
	}
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *memoryTokens) Capacity() int {
	if g == nil {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capacity
}

func (g *memoryTokens) Used() int {
	if g == nil {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

func (g *memoryTokens) clamp(tokens int) int {
	if tokens > g.capacity {
		return g.capacity
	}
	return tokens
}
