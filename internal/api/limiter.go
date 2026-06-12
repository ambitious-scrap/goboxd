package api

import (
	"context"
	"sync"
	"sync/atomic"
)

// AdaptiveLimiter dynamically adjusts the concurrency limit based on request
// latency using an Additive Increase / Multiplicative Decrease (AIMD) algorithm.
// The baseline is dynamically learned as the minimum response time observed.
// It also provides a dynamic channel-based queue for thread-safe slot acquisition.
type AdaptiveLimiter struct {
	mu           sync.Mutex
	limit        atomic.Int32
	minLimit     int32
	maxLimit     int32
	active       int32
	baselineMs   atomic.Int64
	successTimes []int64
	waiters      []chan struct{}
}

// NewAdaptiveLimiter initializes the limiter with a minimum and maximum concurrency bounds.
func NewAdaptiveLimiter(minLimit, maxLimit int32) *AdaptiveLimiter {
	l := &AdaptiveLimiter{
		minLimit:     minLimit,
		maxLimit:     maxLimit,
		successTimes: make([]int64, 0, 10),
	}
	l.limit.Store(minLimit)
	l.baselineMs.Store(999999)
	return l
}

// Acquire blocks until a slot is available under the dynamic concurrency limit,
// or until the context is cancelled. Returns true if acquired, false if cancelled.
func (l *AdaptiveLimiter) Acquire(ctx context.Context) bool {
	l.mu.Lock()
	if l.active < l.limit.Load() {
		l.active++
		l.mu.Unlock()
		return true
	}

	ch := make(chan struct{})
	l.waiters = append(l.waiters, ch)
	l.mu.Unlock()

	select {
	case <-ch:
		return true
	case <-ctx.Done():
		l.mu.Lock()
		defer l.mu.Unlock()
		for i, w := range l.waiters {
			if w == ch {
				l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
				break
			}
		}
		return false
	}
}

// Release relinquishes an active concurrency slot and wakes up any queued waiters.
func (l *AdaptiveLimiter) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.active--
	l.wakeWaiters()
}

// wakeWaiters wakes up queued waiters up to the current concurrency limit.
// Must be called with l.mu held.
func (l *AdaptiveLimiter) wakeWaiters() {
	limit := l.limit.Load()
	for len(l.waiters) > 0 && l.active < limit {
		ch := l.waiters[0]
		l.waiters = l.waiters[1:]
		l.active++
		close(ch)
	}
}

// RecordSuccess registers a successful execution time and adjusts the concurrency limit.
func (l *AdaptiveLimiter) RecordSuccess(durationMs int64) {
	if durationMs <= 5 { // ignore Rentention/Cached fast runs
		return
	}

	// Dynamically update baselineMs to be the lowest observed latency
	for {
		currBase := l.baselineMs.Load()
		if durationMs < currBase {
			if l.baselineMs.CompareAndSwap(currBase, durationMs) {
				break
			}
		} else {
			break
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.successTimes = append(l.successTimes, durationMs)
	if len(l.successTimes) >= 10 {
		var sum int64
		for _, t := range l.successTimes {
			sum += t
		}
		avg := sum / int64(len(l.successTimes))
		l.successTimes = l.successTimes[:0]

		base := l.baselineMs.Load()
		currLimit := l.limit.Load()

		// If average latency is within 125% of the baseline, we increase concurrency
		if avg <= base*125/100 {
			if currLimit < l.maxLimit {
				l.limit.Store(currLimit + 1)
				l.wakeWaiters()
			}
		} else {
			// If average latency starts bending upwards, scale back the concurrency limit
			newLimit := int32(float64(currLimit) * 0.8)
			if newLimit < l.minLimit {
				newLimit = l.minLimit
			}
			l.limit.Store(newLimit)
		}
	}
}

// Limit returns the current dynamic concurrency limit.
func (l *AdaptiveLimiter) Limit() int {
	return int(l.limit.Load())
}
