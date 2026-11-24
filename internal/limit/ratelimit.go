// Package limit provides in-memory rate limits (GCRA), in-flight concurrency
// caps, per-upstream bulkheads, and per-(org, server) circuit breakers.
package limit

import (
	"container/heap"
	"math"
	"sync"
	"time"
)

// DefaultMaxEntries bounds the number of keys retained in memory by a RateLimiter.
const DefaultMaxEntries = 100_000

// RateLimiter enforces per-key request rates using the Generic Cell Rate
// Algorithm (GCRA) in integer nanosecond arithmetic with bounded memory.
// A nil *RateLimiter disables rate limiting and allows every request.
type RateLimiter struct {
	mu         sync.Mutex
	emission   int64 // nanoseconds per request
	tolerance  int64 // burst delay tolerance in nanoseconds
	maxEntries int
	entries    map[string]*gcraEntry
	heap       gcraHeap
}

type gcraEntry struct {
	key string
	tat int64 // theoretical arrival time, Unix nanoseconds
	idx int   // index in heap
}

type gcraHeap []*gcraEntry

func (h gcraHeap) Len() int           { return len(h) }
func (h gcraHeap) Less(i, j int) bool { return h[i].tat < h[j].tat }
func (h gcraHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx = i
	h[j].idx = j
}
func (h *gcraHeap) Push(x any) {
	e := x.(*gcraEntry)
	e.idx = len(*h)
	*h = append(*h, e)
}
func (h *gcraHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	e.idx = -1
	*h = old[:n-1]
	return e
}

// NewRateLimiter returns a GCRA rate limiter allowing rpm requests per minute
// with burst consecutive requests allowed at once. If rpm <= 0, nil is returned
// (disabled). If burst <= 0, burst defaults to rpm. If maxEntries <= 0,
// maxEntries defaults to DefaultMaxEntries.
func NewRateLimiter(rpm, burst, maxEntries int) *RateLimiter {
	if rpm <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = rpm
	}
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	emission := int64(time.Minute) / int64(rpm)
	if emission < 1 {
		emission = 1
	}
	var tolerance int64
	if burst > 1 {
		if int64(burst-1) > math.MaxInt64/emission {
			tolerance = math.MaxInt64
		} else {
			tolerance = int64(burst-1) * emission
		}
	}
	return &RateLimiter{
		emission:   emission,
		tolerance:  tolerance,
		maxEntries: maxEntries,
		entries:    make(map[string]*gcraEntry),
	}
}

// Allow reports whether a request for key at time now is within the rate limit.
// When denied, retryAfter is the wait until the next request would be allowed.
func (l *RateLimiter) Allow(key string, now time.Time) (allowed bool, retryAfter time.Duration) {
	if l == nil {
		return true, 0
	}
	nowNS := now.UnixNano()

	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.entries[key]; ok {
		allowAt := subClamped(e.tat, l.tolerance)
		if nowNS < allowAt {
			return false, time.Duration(allowAt - nowNS)
		}
		base := e.tat
		if nowNS > base {
			base = nowNS
		}
		e.tat = addClamped(base, l.emission)
		heap.Fix(&l.heap, e.idx)
		return true, 0
	}

	if len(l.entries) >= l.maxEntries {
		l.evictLocked(nowNS)
	}
	e := &gcraEntry{
		key: key,
		tat: addClamped(nowNS, l.emission),
	}
	l.entries[key] = e
	heap.Push(&l.heap, e)
	return true, 0
}

// Len returns the number of keys currently tracked.
func (l *RateLimiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *RateLimiter) evictLocked(nowNS int64) {
	// Evict expired/idle entries (tat <= nowNS) first, draining up to a small
	// bounded batch so stale keys clear without O(N) latency spikes.
	evicted := 0
	for len(l.heap) > 0 && l.heap[0].tat <= nowNS && (len(l.entries) >= l.maxEntries || evicted < 16) {
		e := heap.Pop(&l.heap).(*gcraEntry)
		delete(l.entries, e.key)
		evicted++
	}
	// If still at capacity with active entries only, evict the entry with the
	// smallest TAT (soonest to expire / least accumulated burst).
	for len(l.entries) >= l.maxEntries && len(l.heap) > 0 {
		e := heap.Pop(&l.heap).(*gcraEntry)
		delete(l.entries, e.key)
	}
}

func addClamped(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func subClamped(a, b int64) int64 {
	if b > 0 && a < math.MinInt64+b {
		return math.MinInt64
	}
	return a - b
}
