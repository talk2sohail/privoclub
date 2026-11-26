package policy

import (
	"container/heap"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Cross-server flow tracker bounds and window limits (ADR 0032).
const (
	DefaultFlowWindow = 10 * time.Minute
	MinFlowWindow     = 10 * time.Second
	MaxFlowWindow     = 24 * time.Hour

	maxTrackedCallers = 10_000
	maxPerOrgTracked  = 2_048
)

// ParseFlowWindow parses and validates a per-org cross-server flow window.
// Valid durations lie within [MinFlowWindow, MaxFlowWindow].
func ParseFlowWindow(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("flow_window must be a duration between %s and %s", MinFlowWindow, MaxFlowWindow)
	}
	if d < MinFlowWindow || d > MaxFlowWindow {
		return 0, fmt.Errorf("flow_window must be between %s and %s", MinFlowWindow, MaxFlowWindow)
	}
	return d, nil
}

type flowKey struct {
	org       string
	callerKey string
	client    string
}

type flowRecord struct {
	key         flowKey
	serverAlias string
	toolName    string
	at          time.Time
	seq         uint64
	globalIdx   int
	orgIdx      int
}

type flowHeap struct {
	items []*flowRecord
	isOrg bool
}

func (h *flowHeap) Len() int { return len(h.items) }

func (h *flowHeap) Less(i, j int) bool {
	a, b := h.items[i], h.items[j]
	if !a.at.Equal(b.at) {
		return a.at.Before(b.at)
	}
	return a.seq < b.seq
}

func (h *flowHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	if h.isOrg {
		h.items[i].orgIdx = i
		h.items[j].orgIdx = j
	} else {
		h.items[i].globalIdx = i
		h.items[j].globalIdx = j
	}
}

func (h *flowHeap) Push(x any) {
	r := x.(*flowRecord)
	if h.isOrg {
		r.orgIdx = len(h.items)
	} else {
		r.globalIdx = len(h.items)
	}
	h.items = append(h.items, r)
}

func (h *flowHeap) Pop() any {
	n := len(h.items)
	r := h.items[n-1]
	h.items[n-1] = nil
	h.items = h.items[:n-1]
	if h.isOrg {
		r.orgIdx = -1
	} else {
		r.globalIdx = -1
	}
	return r
}

// FlowTracker remembers recent untrusted_source reads per (org, callerKey, client)
// in memory with bounded capacity and fail-closed overflow per org (ADR 0032).
// A zero-value FlowTracker is ready to use.
type FlowTracker struct {
	mu        sync.Mutex
	seq       uint64
	entries   map[flowKey]*flowRecord
	global    flowHeap
	orgs      map[string]*flowHeap
	overflows map[string]time.Time
}

// NewFlowTracker returns an initialized FlowTracker.
func NewFlowTracker() *FlowTracker {
	return &FlowTracker{
		entries:   map[flowKey]*flowRecord{},
		orgs:      map[string]*flowHeap{},
		overflows: map[string]time.Time{},
	}
}

func (t *FlowTracker) initLocked() {
	if t.entries == nil {
		t.entries = map[flowKey]*flowRecord{}
		t.orgs = map[string]*flowHeap{}
		t.overflows = map[string]time.Time{}
	}
}

func (t *FlowTracker) removeLocked(r *flowRecord) {
	heap.Remove(&t.global, r.globalIdx)
	if oh := t.orgs[r.key.org]; oh != nil {
		heap.Remove(oh, r.orgIdx)
		if oh.Len() == 0 {
			delete(t.orgs, r.key.org)
		}
	}
	delete(t.entries, r.key)
}

func (t *FlowTracker) pruneExpiredLocked(now time.Time) {
	for t.global.Len() > 0 {
		oldest := t.global.items[0]
		if now.Sub(oldest.at) < MaxFlowWindow {
			break
		}
		t.removeLocked(oldest)
	}
	for org, overAt := range t.overflows {
		if now.Sub(overAt) >= MaxFlowWindow {
			delete(t.overflows, org)
		}
	}
}

func (t *FlowTracker) markOverflowLocked(org string, now time.Time) {
	if prev, ok := t.overflows[org]; !ok || now.After(prev) {
		t.overflows[org] = now
	}
}

// RecordSource records a read from an untrusted_source server/item for the
// caller tuple (org, callerKey, client) at time now.
func (t *FlowTracker) RecordSource(org, callerKey, client, serverAlias, toolName string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.initLocked()

	k := flowKey{org: org, callerKey: callerKey, client: client}
	if r, ok := t.entries[k]; ok {
		if !now.Before(r.at) {
			t.seq++
			r.serverAlias, r.toolName, r.at, r.seq = serverAlias, toolName, now, t.seq
			heap.Fix(&t.global, r.globalIdx)
			if oh := t.orgs[org]; oh != nil {
				heap.Fix(oh, r.orgIdx)
			}
		}
		return
	}

	t.pruneExpiredLocked(now)

	if oh := t.orgs[org]; oh != nil && oh.Len() >= maxPerOrgTracked {
		evicted := oh.items[0]
		t.removeLocked(evicted)
		t.markOverflowLocked(evicted.key.org, now)
	}
	if len(t.entries) >= maxTrackedCallers {
		evicted := t.global.items[0]
		t.removeLocked(evicted)
		t.markOverflowLocked(evicted.key.org, now)
	}

	t.seq++
	r := &flowRecord{
		key:         k,
		serverAlias: serverAlias,
		toolName:    toolName,
		at:          now,
		seq:         t.seq,
	}
	t.entries[k] = r
	oh := t.orgs[org]
	if oh == nil {
		oh = &flowHeap{isOrg: true}
		t.orgs[org] = oh
	}
	heap.Push(&t.global, r)
	heap.Push(oh, r)
}

// CheckSink reports whether the caller tuple (org, callerKey, client) has an
// active untrusted_source read within window at time now, or whether org
// overflowed its tracking capacity within window (failing closed).
func (t *FlowTracker) CheckSink(org, callerKey, client string, window time.Duration, now time.Time) (bool, string, time.Duration) {
	if window <= 0 {
		return false, "", 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries != nil {
		if r, ok := t.entries[flowKey{org: org, callerKey: callerKey, client: client}]; ok {
			age := now.Sub(r.at)
			if age >= 0 && age < window {
				return true, formatFlowSource(r.serverAlias, r.toolName), age
			}
			if age >= MaxFlowWindow {
				t.removeLocked(r)
			}
		}
	}
	if t.overflows != nil {
		if overAt, ok := t.overflows[org]; ok {
			age := now.Sub(overAt)
			if age >= 0 && age < window {
				return true, "tracker capacity overflow", age
			}
			if age >= MaxFlowWindow {
				delete(t.overflows, org)
			}
		}
	}
	return false, "", 0
}

func formatFlowSource(serverAlias, toolName string) string {
	if toolName == "" {
		return serverAlias
	}
	return serverAlias + "/" + toolName
}
