package limit

import "sync"

// InFlightConfig configures global, per-organisation, and per-upstream
// concurrency caps. Zero or negative disables that specific cap.
type InFlightConfig struct {
	Global   int
	Org      int
	Upstream int
}

type upstreamKey struct {
	org      string
	serverID string
}

// InFlight enforces global, per-org, and per-(org, serverID) bulkhead
// concurrency limits with immediate shedding and idempotent slot release.
// A nil *InFlight disables all concurrency limits.
type InFlight struct {
	mu          sync.Mutex
	globalMax   int
	orgMax      int
	upstreamMax int
	global      int
	orgs        map[string]int
	upstreams   map[upstreamKey]int
}

// NewInFlight creates an InFlight concurrency limiter. If all caps are <= 0,
// nil is returned (all limits disabled).
func NewInFlight(cfg InFlightConfig) *InFlight {
	if cfg.Global <= 0 && cfg.Org <= 0 && cfg.Upstream <= 0 {
		return nil
	}
	return &InFlight{
		globalMax:   cfg.Global,
		orgMax:      cfg.Org,
		upstreamMax: cfg.Upstream,
		orgs:        make(map[string]int),
		upstreams:   make(map[upstreamKey]int),
	}
}

// Acquire attempts to take one global and one per-org in-flight slot.
// The returned release function is idempotent and must be called via defer.
func (f *InFlight) Acquire(org string) (release func(), ok bool) {
	if f == nil || (f.globalMax <= 0 && f.orgMax <= 0) {
		return noopRelease, true
	}
	f.mu.Lock()
	if f.globalMax > 0 && f.global >= f.globalMax {
		f.mu.Unlock()
		return nil, false
	}
	if f.orgMax > 0 && f.orgs[org] >= f.orgMax {
		f.mu.Unlock()
		return nil, false
	}
	f.global++
	if f.orgMax > 0 {
		f.orgs[org]++
	}
	f.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.global--
			if f.orgMax > 0 {
				if n := f.orgs[org] - 1; n <= 0 {
					delete(f.orgs, org)
				} else {
					f.orgs[org] = n
				}
			}
			f.mu.Unlock()
		})
	}, true
}

// AcquireUpstream attempts to take one per-(org, serverID) bulkhead slot.
// The returned release function is idempotent and must be called via defer.
func (f *InFlight) AcquireUpstream(org, serverID string) (release func(), ok bool) {
	if f == nil || f.upstreamMax <= 0 {
		return noopRelease, true
	}
	k := upstreamKey{org: org, serverID: serverID}
	f.mu.Lock()
	if f.upstreams[k] >= f.upstreamMax {
		f.mu.Unlock()
		return nil, false
	}
	f.upstreams[k]++
	f.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			if n := f.upstreams[k] - 1; n <= 0 {
				delete(f.upstreams, k)
			} else {
				f.upstreams[k] = n
			}
			f.mu.Unlock()
		})
	}, true
}

// Global returns the current global in-flight count.
func (f *InFlight) Global() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.global
}

// Org returns the current in-flight count for org.
func (f *InFlight) Org(org string) int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.orgs[org]
}

// Upstream returns the current in-flight count for (org, serverID).
func (f *InFlight) Upstream(org, serverID string) int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upstreams[upstreamKey{org: org, serverID: serverID}]
}

func noopRelease() {}
