package telemetry

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/store"
)

// Metrics keeps Prometheus counters in memory (ADR 0024 point 7). Labels are
// server, method and outcome only, so the number of series is bounded by the
// number of servers. A nil *Metrics counts nothing.
type Metrics struct {
	mu        sync.Mutex
	calls     map[[3]string]uint64 // server, method, outcome
	durations map[[2]string]*histogram
	dropped   atomic.Uint64
	auditLost atomic.Uint64
	// Upstreams reports each server's connection state at scrape time; nil
	// leaves the metric out.
	Upstreams func() []UpstreamState
}

// UpstreamState is one server's connection state for privo_upstream_up.
type UpstreamState struct {
	Server string
	Up     bool
}

// Buckets of the duration histogram, in seconds.
var Buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

type histogram struct {
	counts []uint64 // per bucket, not cumulative; the last is +Inf
	sum    float64
	n      uint64
}

// NewMetrics makes an empty set.
func NewMetrics() *Metrics {
	return &Metrics{calls: map[[3]string]uint64{}, durations: map[[2]string]*histogram{}}
}

// ObserveCall counts one call and its total duration.
func (m *Metrics) ObserveCall(c store.CallStat) {
	if m == nil {
		return
	}
	// The time a person took to approve is not the server's (T-10).
	secs := float64(max(c.TotalUS-c.WaitUS, 0)) / 1e6
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[[3]string{c.Server, c.Method, c.Outcome}]++
	if c.Outcome == store.OutcomeRefused {
		return // refusals are quick and would hide slow servers
	}
	k := [2]string{c.Server, c.Method}
	h := m.durations[k]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(Buckets)+1)}
		m.durations[k] = h
	}
	i, _ := slices.BinarySearch(Buckets, secs)
	h.counts[i]++
	h.sum += secs
	h.n++
}

// AddDropped counts one statistic that was not recorded.
func (m *Metrics) AddDropped() { m.AddDroppedN(1) }

// AddDroppedN counts n statistics that were not recorded.
func (m *Metrics) AddDroppedN(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.dropped.Add(uint64(n))
}

// AddAuditLost counts audit events the writer could not store.
func (m *Metrics) AddAuditLost(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.auditLost.Add(uint64(n))
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.Write(w)
	})
}

// Write writes the metrics in the Prometheus text format, sorted so that two
// scrapes of the same state are the same text.
func (m *Metrics) Write(w io.Writer) {
	var b strings.Builder
	b.WriteString("# HELP privo_build_info Build of this Privo.\n# TYPE privo_build_info gauge\n")
	fmt.Fprintf(&b, "privo_build_info{version=%s} 1\n", quote(buildinfo.Version))
	if m == nil {
		_, _ = io.WriteString(w, b.String())
		return
	}

	m.mu.Lock()
	callKeys := make([][3]string, 0, len(m.calls))
	for k := range m.calls {
		callKeys = append(callKeys, k)
	}
	slices.SortFunc(callKeys, func(a, b [3]string) int {
		return strings.Compare(a[0]+"\x00"+a[1]+"\x00"+a[2], b[0]+"\x00"+b[1]+"\x00"+b[2])
	})
	b.WriteString("# HELP privo_mcp_calls_total Calls through the gateway by server, method and outcome.\n# TYPE privo_mcp_calls_total counter\n")
	for _, k := range callKeys {
		fmt.Fprintf(&b, "privo_mcp_calls_total{server=%s,method=%s,outcome=%s} %d\n", quote(k[0]), quote(k[1]), quote(k[2]), m.calls[k])
	}
	durKeys := make([][2]string, 0, len(m.durations))
	for k := range m.durations {
		durKeys = append(durKeys, k)
	}
	slices.SortFunc(durKeys, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	b.WriteString("# HELP privo_mcp_call_duration_seconds Time of calls that ran, from arrival to answer.\n# TYPE privo_mcp_call_duration_seconds histogram\n")
	for _, k := range durKeys {
		h := m.durations[k]
		labels := "server=" + quote(k[0]) + ",method=" + quote(k[1])
		var cum uint64
		for i, le := range Buckets {
			cum += h.counts[i]
			fmt.Fprintf(&b, "privo_mcp_call_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(le, 'g', -1, 64), cum)
		}
		fmt.Fprintf(&b, "privo_mcp_call_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, h.n)
		fmt.Fprintf(&b, "privo_mcp_call_duration_seconds_sum{%s} %s\n", labels, strconv.FormatFloat(h.sum, 'g', -1, 64))
		fmt.Fprintf(&b, "privo_mcp_call_duration_seconds_count{%s} %d\n", labels, h.n)
	}
	m.mu.Unlock()

	if m.Upstreams != nil {
		states := m.Upstreams()
		slices.SortFunc(states, func(a, b UpstreamState) int { return strings.Compare(a.Server, b.Server) })
		b.WriteString("# HELP privo_upstream_up Whether the gateway's connection to an upstream server is up (1) or not (0).\n# TYPE privo_upstream_up gauge\n")
		for _, s := range states {
			v := 0
			if s.Up {
				v = 1
			}
			fmt.Fprintf(&b, "privo_upstream_up{server=%s} %d\n", quote(s.Server), v)
		}
	}
	b.WriteString("# HELP privo_audit_events_lost_total Audit events that could not be stored after retries.\n# TYPE privo_audit_events_lost_total counter\n")
	fmt.Fprintf(&b, "privo_audit_events_lost_total %d\n", m.auditLost.Load())
	b.WriteString("# HELP privo_telemetry_dropped_total Call statistics and diagnostic events not recorded (queue full or write failed).\n# TYPE privo_telemetry_dropped_total counter\n")
	fmt.Fprintf(&b, "privo_telemetry_dropped_total %d\n", m.dropped.Load())
	_, _ = io.WriteString(w, b.String())
}

// quote writes a label value as the text format wants it: in double quotes,
// with backslash, double quote and line feed escaped.
func quote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}
