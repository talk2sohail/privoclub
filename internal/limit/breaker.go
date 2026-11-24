package limit

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// State is the state of a per-(org, server) circuit breaker.
type State int

// Circuit breaker states reported by States() and privo_upstream_breaker_state.
const (
	Closed   State = 0
	Open     State = 1
	HalfOpen State = 2
)

// Outcome classifies an upstream call's result for the circuit breaker.
type Outcome int

const (
	// OutcomeNeutral neither trips nor closes the breaker (e.g. client canceled,
	// missing grant, malformed reply). For a HalfOpen probe it frees the probe
	// slot without changing state.
	OutcomeNeutral Outcome = iota
	// OutcomeSuccess closes a HalfOpen breaker or resets consecutive failures.
	OutcomeSuccess
	// OutcomeFailure counts toward tripping a Closed breaker or re-opens a
	// HalfOpen probe immediately.
	OutcomeFailure
)

// BreakerTransition describes a circuit breaker state edge.
type BreakerTransition struct {
	From State
	To   State
}

// BreakerState summarizes a server's breaker state for metrics without org_id.
type BreakerState struct {
	Server string
	State  State
}

const maxBreakerEntries = 10_000

type breakerKey struct {
	org      string
	serverID string
}

type breakerEntry struct {
	alias    string
	state    State
	failures int
	openedAt time.Time
	probing  bool
	probeGen uint64
}

// Breaker implements a 3-state per-(org, serverID) circuit breaker.
// A nil *Breaker disables circuit breaking and allows every request.
type Breaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu      sync.Mutex
	entries map[breakerKey]*breakerEntry
}

// NewBreaker returns a circuit breaker that opens after threshold consecutive
// failures and stays open for cooldown. If threshold <= 0, nil is returned.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return NewBreakerWithClock(threshold, cooldown, time.Now)
}

// NewBreakerWithClock is like NewBreaker using a caller-supplied clock.
func NewBreakerWithClock(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	if threshold <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &Breaker{
		threshold: threshold,
		cooldown:  cooldown,
		now:       now,
		entries:   make(map[breakerKey]*breakerEntry),
	}
}

// Allow checks whether a call to (org, serverID) may proceed.
// When allowed is true, finish MUST be called (typically via defer with
// OutcomeNeutral as fallback) to record the call outcome or release a HalfOpen
// probe slot; finish is idempotent via sync.Once.
func (b *Breaker) Allow(org, serverID, alias string) (allowed bool, transition *BreakerTransition, finish func(Outcome) *BreakerTransition) {
	if b == nil {
		return true, nil, noopBreakerFinish
	}
	now := b.now()
	k := breakerKey{org: org, serverID: serverID}

	b.mu.Lock()
	e, ok := b.entries[k]
	if !ok {
		if len(b.entries) >= maxBreakerEntries {
			b.evictLocked()
		}
		e = &breakerEntry{alias: alias, state: Closed}
		b.entries[k] = e
	} else if alias != "" {
		e.alias = alias
	}

	var tr *BreakerTransition
	isProbe := false
	var gen uint64

	switch e.state {
	case Closed:
		// proceed
	case Open:
		if now.Sub(e.openedAt) < b.cooldown {
			b.mu.Unlock()
			return false, nil, noopBreakerFinish
		}
		e.state = HalfOpen
		e.probing = true
		e.probeGen++
		gen = e.probeGen
		isProbe = true
		tr = &BreakerTransition{From: Open, To: HalfOpen}
	case HalfOpen:
		if e.probing {
			b.mu.Unlock()
			return false, nil, noopBreakerFinish
		}
		e.probing = true
		e.probeGen++
		gen = e.probeGen
		isProbe = true
	}
	b.mu.Unlock()

	var once sync.Once
	var res *BreakerTransition
	return true, tr, func(outcome Outcome) *BreakerTransition {
		once.Do(func() {
			res = b.finish(k, isProbe, gen, outcome)
		})
		return res
	}
}

func (b *Breaker) finish(k breakerKey, isProbe bool, gen uint64, outcome Outcome) *BreakerTransition {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[k]
	if !ok {
		return nil
	}
	if isProbe {
		if e.state != HalfOpen || e.probeGen != gen {
			return nil
		}
		switch outcome {
		case OutcomeSuccess:
			e.state = Closed
			e.failures = 0
			e.probing = false
			return &BreakerTransition{From: HalfOpen, To: Closed}
		case OutcomeFailure:
			e.state = Open
			e.openedAt = now
			e.failures = 0
			e.probing = false
			return &BreakerTransition{From: HalfOpen, To: Open}
		default: // OutcomeNeutral
			e.probing = false
			return nil
		}
	}

	if e.state != Closed {
		return nil
	}
	switch outcome {
	case OutcomeSuccess:
		e.failures = 0
	case OutcomeFailure:
		e.failures++
		if e.failures >= b.threshold {
			e.state = Open
			e.openedAt = now
			e.failures = 0
			return &BreakerTransition{From: Closed, To: Open}
		}
	}
	return nil
}

// States returns each tracked server's breaker state aggregated by server alias
// (worst state wins: Open > HalfOpen > Closed), sorted by server name, with no
// org_id label.
func (b *Breaker) States() []BreakerState {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	byServer := make(map[string]State, len(b.entries))
	for _, e := range b.entries {
		cur, exists := byServer[e.alias]
		if !exists || stateSeverity(e.state) > stateSeverity(cur) {
			byServer[e.alias] = e.state
		}
	}
	b.mu.Unlock()

	out := make([]BreakerState, 0, len(byServer))
	for srv, st := range byServer {
		out = append(out, BreakerState{Server: srv, State: st})
	}
	slices.SortFunc(out, func(a, c BreakerState) int { return strings.Compare(a.Server, c.Server) })
	return out
}

func stateSeverity(s State) int {
	switch s {
	case Open:
		return 2
	case HalfOpen:
		return 1
	default:
		return 0
	}
}

func (b *Breaker) evictLocked() {
	for k, e := range b.entries {
		if e.state == Closed && e.failures == 0 {
			delete(b.entries, k)
			return
		}
	}
	for k := range b.entries {
		delete(b.entries, k)
		return
	}
}

func noopBreakerFinish(Outcome) *BreakerTransition { return nil }
