package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"privo.club/privo/internal/store"
)

type memStore struct {
	mu    sync.Mutex
	calls []store.CallStat
	diags []store.Diagnostic
	block chan struct{} // when set, inserts wait for it
	fail  bool
}

func (m *memStore) InsertCallStats(_ context.Context, _ store.OrgID, rows ...store.CallStat) error {
	if m.block != nil {
		<-m.block
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("disk full")
	}
	m.calls = append(m.calls, rows...)
	return nil
}

func (m *memStore) InsertDiagnostics(_ context.Context, _ store.OrgID, rows ...store.Diagnostic) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("disk full")
	}
	m.diags = append(m.diags, rows...)
	return nil
}

func TestRecorderWritesAndNilIsSafe(t *testing.T) {
	var none *Recorder
	none.Call("o", store.CallStat{}) // telemetry off: no panic, nothing kept
	none.Diagnose("o", store.Diagnostic{})
	none.Close()
	if none.Dropped() != 0 {
		t.Fatal("nil recorder counted drops")
	}

	m := &memStore{}
	met := NewMetrics()
	r := New(m, Options{FlushInterval: time.Hour, Metrics: met})
	r.Call("o", store.CallStat{Server: "github", Method: "tools/call", Outcome: store.OutcomeOK, TotalUS: 1500})
	r.Close() // Close flushes
	if len(m.calls) != 1 || m.calls[0].ID == "" {
		t.Fatalf("written on close with an id: %+v", m.calls)
	}
	var b strings.Builder
	met.Write(&b)
	if !strings.Contains(b.String(), `privo_mcp_calls_total{server="github",method="tools/call",outcome="ok"} 1`) {
		t.Fatalf("the call was not counted in the metrics:\n%s", b.String())
	}
	r.Call("o", store.CallStat{}) // after Close: dropped, no panic
	if r.Dropped() != 1 {
		t.Fatalf("dropped after close = %d", r.Dropped())
	}
}

func TestRecorderDropsWhenFull(t *testing.T) {
	m := &memStore{block: make(chan struct{})}
	r := New(m, Options{QueueSize: 2, BatchSize: 1, FlushInterval: time.Hour})
	// The first row is taken by the writer, which then blocks on the store;
	// two more fill the queue; the rest must be dropped without waiting.
	start := time.Now()
	for range 20 {
		r.Call("o", store.CallStat{Outcome: store.OutcomeOK})
	}
	if time.Since(start) > time.Second {
		t.Fatal("a full queue made the caller wait")
	}
	if d := r.Dropped(); d < 17 {
		t.Fatalf("dropped %d of 20 with a queue of 2", d)
	}
	close(m.block)
	r.Close()
	if got := len(m.calls) + int(r.Dropped()); got != 20 {
		t.Fatalf("written %d + dropped %d != 20", len(m.calls), r.Dropped())
	}
}

func TestRecorderCountsRowsItCannotWrite(t *testing.T) {
	m := &memStore{fail: true}
	met := NewMetrics()
	r := New(m, Options{FlushInterval: time.Hour, Metrics: met})
	r.Call("o", store.CallStat{Outcome: store.OutcomeOK})
	r.Call("o", store.CallStat{Outcome: store.OutcomeOK})
	r.Close()
	if r.Dropped() != 2 {
		t.Fatalf("failed writes counted as %d dropped, want 2", r.Dropped())
	}
	var b strings.Builder
	met.Write(&b)
	if !strings.Contains(b.String(), "privo_telemetry_dropped_total 2\n") {
		t.Fatalf("metrics:\n%s", b.String())
	}
}

func TestDiagnosticsRepeatCollapse(t *testing.T) {
	m := &memStore{}
	var mu sync.Mutex
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r := New(m, Options{FlushInterval: time.Hour, Now: clock, RepeatWindow: time.Minute})
	d := store.Diagnostic{Level: store.LevelWarn, Category: store.CategoryUpstream, Server: "github", Message: "connect failed"}
	for range 5 {
		r.Diagnose("o", d)
	}
	other := d
	other.Server = "linear"
	r.Diagnose("o", other)
	// After the window the same event is written again, preceded by the count
	// of the four that were held back.
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	r.Diagnose("o", d)
	r.Close()

	var gh []store.Diagnostic
	for _, x := range m.diags {
		if x.Server == "github" {
			gh = append(gh, x)
		}
	}
	if len(gh) != 3 || gh[0].Repeats != 1 || gh[1].Repeats != 4 || gh[2].Repeats != 1 {
		t.Fatalf("github rows: %+v", gh)
	}
	if len(m.diags) != 4 {
		t.Fatalf("a different server's event was merged: %d rows", len(m.diags))
	}
}

func TestDiagnosticsHeldRepeatsAreWrittenOnClose(t *testing.T) {
	m := &memStore{}
	r := New(m, Options{FlushInterval: time.Hour})
	d := store.Diagnostic{Level: store.LevelError, Category: store.CategoryAudit, Message: "audit events lost"}
	r.Diagnose("o", d)
	r.Diagnose("o", d)
	r.Diagnose("o", d)
	r.Close()
	sum := 0
	for _, x := range m.diags {
		sum += x.Repeats
	}
	if len(m.diags) != 2 || sum != 3 {
		t.Fatalf("rows %+v: the held repeats were lost", m.diags)
	}
}

func sample(key string, at time.Time, latencyMS int64) store.StatSample {
	return store.StatSample{Key: key, Time: at, LatencyUS: latencyMS * 1000, UpstreamUS: latencyMS * 900, DecideUS: 50, ArgsBytes: 10, ResultBytes: 100}
}

func TestSummarize(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	var xs []store.StatSample
	for i := 1; i <= 100; i++ { // github: 1..100 ms
		xs = append(xs, sample("github", t0, int64(i)))
	}
	xs = append(xs, sample("linear", t0, 500), sample("linear", t0, 300))
	counts := []store.StatCount{
		{Key: "linear", Calls: 3, Errors: 1, ToolErrors: 1, Refused: 1},
		// More github calls than the sample holds: counts cover all.
		{Key: "github", Calls: 150, OK: 150},
	}
	total, groups := Summarize(counts, xs)
	if total.Calls != 153 || total.OK != 150 || total.Errors != 1 || total.ToolErrors != 1 || total.Refused != 1 {
		t.Fatalf("total counts: %+v", total)
	}
	if len(groups) != 2 || groups[0].Key != "github" || groups[1].Key != "linear" {
		t.Fatalf("groups, largest first: %+v", groups)
	}
	gh := groups[0]
	if gh.Calls != 150 || gh.Total.P50 != 50 || gh.Total.P95 != 95 || gh.Total.P99 != 99 || gh.Total.Min != 1 || gh.Total.Max != 100 || gh.Total.Avg != 50.5 {
		t.Fatalf("github: %+v", gh)
	}
	if gh.Decide.P50 != 0.05 || gh.Result.Avg != 100 {
		t.Fatalf("phases and sizes: decide %+v result %+v", gh.Decide, gh.Result)
	}
	li := groups[1]
	if li.Calls != 3 || li.Refused != 1 || li.Total.Min != 300 || li.ErrorRate != 0.5 {
		t.Fatalf("linear: %+v", li)
	}
	if empty, none := Summarize(nil, nil); empty.Calls != 0 || empty.Total != (Stats{}) || len(none) != 0 {
		t.Fatalf("empty: %+v", empty)
	}
	if !ValidBy("tool") || ValidBy("team") {
		t.Fatal("ValidBy")
	}
}

func TestSeries(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	counts := []store.StatCount{{Key: "0", Calls: 2}, {Key: "2", Calls: 2, Errors: 1, Refused: 1}, {Key: "7", Calls: 9}, {Key: "x", Calls: 9}}
	xs := []store.StatSample{
		sample("", t0, 10), sample("", t0.Add(59*time.Second), 30),
		sample("", t0.Add(2*time.Minute), 1000),
		sample("", t0.Add(-time.Second), 5),  // before the window
		sample("", t0.Add(3*time.Minute), 5), // at the end: excluded
	}
	from, to := t0, t0.Add(3*time.Minute)
	if err := CheckSeries(from, to, time.Minute); err != nil {
		t.Fatal(err)
	}
	s := Series(counts, xs, from, to, time.Minute)
	if len(s) != 3 {
		t.Fatalf("buckets: %d", len(s))
	}
	if s[0].Calls != 2 || s[0].P50 != 10 || s[0].P95 != 30 {
		t.Fatalf("bucket 0: %+v", s[0])
	}
	if s[1].Calls != 0 || !s[1].Start.Equal(t0.Add(time.Minute)) {
		t.Fatalf("an empty bucket must be present: %+v", s[1])
	}
	if s[2].Calls != 2 || s[2].Errors != 1 || s[2].Refused != 1 || s[2].P50 != 1000 {
		t.Fatalf("bucket 2: %+v (a count outside the range must be ignored)", s[2])
	}
	// The limits: at most 1,000 buckets, so exactly 1,000 is allowed (T-09).
	if err := CheckSeries(from, from.Add(1000*time.Minute), time.Minute); err != nil {
		t.Fatalf("1,000 buckets: %v", err)
	}
	for _, bad := range []struct {
		from, to time.Time
		bucket   time.Duration
	}{
		{to, from, time.Minute},                           // reversed
		{from, from.Add(33 * 24 * time.Hour), time.Hour},  // too long
		{from, to, time.Second},                           // bucket too small
		{from, from.Add(1001 * time.Minute), time.Minute}, // too many buckets
	} {
		if err := CheckSeries(bad.from, bad.to, bad.bucket); !errors.Is(err, ErrSeries) {
			t.Fatalf("CheckSeries(%v, %v, %v) = %v", bad.from, bad.to, bad.bucket, err)
		}
	}
}

// Review of 2026-11-15, T-01: a message an upstream wrote is cut, on a rune
// boundary, wherever it comes from.
func TestDiagnosticsAreClipped(t *testing.T) {
	m := &memStore{}
	r := New(m, Options{FlushInterval: time.Hour})
	long := strings.Repeat("é", MaxMessage) // two bytes each
	r.Diagnose("o", store.Diagnostic{Level: store.LevelWarn, Category: store.CategoryUpstream, Server: strings.Repeat("s", 200), Message: "connection failed: " + long})
	r.Close()
	d := m.diags[0]
	if len(d.Message) > MaxMessage || !strings.HasSuffix(d.Message, "…") || !utf8.ValidString(d.Message) || len(d.Server) > 64 {
		t.Fatalf("clipped to %d bytes (valid %v), server %d", len(d.Message), utf8.ValidString(d.Message), len(d.Server))
	}
	if Clip("short", 10) != "short" {
		t.Fatal("a short text was changed")
	}
}

func TestMetricsText(t *testing.T) {
	m := NewMetrics()
	m.Upstreams = func() []UpstreamState { return []UpstreamState{{"linear", false}, {"git\"hub", true}} }
	m.ObserveCall(store.CallStat{Server: "git\"hub", Method: "tools/call", Outcome: store.OutcomeOK, TotalUS: 7_000})      // 0.007 s
	m.ObserveCall(store.CallStat{Server: "git\"hub", Method: "tools/call", Outcome: store.OutcomeOK, TotalUS: 90_000_000}) // 90 s
	m.ObserveCall(store.CallStat{Server: "git\"hub", Method: "tools/call", Outcome: store.OutcomeRefused})
	// 20 s waiting for a person, 6 ms of work: in the 0.01 bucket (T-10).
	m.ObserveCall(store.CallStat{Server: "linear", Method: "tools/call", Outcome: store.OutcomeOK, TotalUS: 20_006_000, WaitUS: 20_000_000})
	m.AddAuditLost(3)
	var b strings.Builder
	m.Write(&b)
	out := b.String()
	for _, want := range []string{
		`privo_mcp_calls_total{server="git\"hub",method="tools/call",outcome="ok"} 2`,
		`privo_mcp_calls_total{server="git\"hub",method="tools/call",outcome="refused"} 1`,
		`privo_mcp_call_duration_seconds_bucket{server="git\"hub",method="tools/call",le="0.005"} 0`,
		`privo_mcp_call_duration_seconds_bucket{server="git\"hub",method="tools/call",le="0.01"} 1`,
		`privo_mcp_call_duration_seconds_bucket{server="git\"hub",method="tools/call",le="60"} 1`,
		`privo_mcp_call_duration_seconds_bucket{server="git\"hub",method="tools/call",le="+Inf"} 2`,
		`privo_mcp_call_duration_seconds_count{server="git\"hub",method="tools/call"} 2`,
		"privo_upstream_up{server=\"git\\\"hub\"} 1\nprivo_upstream_up{server=\"linear\"} 0\n",
		`privo_mcp_call_duration_seconds_bucket{server="linear",method="tools/call",le="0.01"} 1`,
		"privo_audit_events_lost_total 3\n",
		"privo_telemetry_dropped_total 0\n",
		"# TYPE privo_mcp_call_duration_seconds histogram\n",
		"privo_build_info{version=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Labels are server, method and outcome only: no member, token or tool.
	for _, banned := range []string{"user", "token", "item", "tool"} {
		if strings.Contains(out, banned+"=\"") {
			t.Errorf("label %q present", banned)
		}
	}
	var none *Metrics
	b.Reset()
	none.Write(&b) // telemetry off: build info only, no panic
	if !strings.Contains(b.String(), "privo_build_info") {
		t.Fatal("nil metrics wrote nothing")
	}
}

func TestBreakerMetricHasNoOrgLabel(t *testing.T) {
	m := NewMetrics()
	m.Breakers = func() []BreakerState {
		return []BreakerState{
			{Server: "linear", State: 1},
			{Server: "git\"hub", State: 0},
			{Server: "git\"hub", State: 2},
			{Server: "slack", State: 0},
		}
	}
	var b strings.Builder
	m.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# HELP privo_upstream_breaker_state",
		"# TYPE privo_upstream_breaker_state gauge",
		"privo_upstream_breaker_state{server=\"git\\\"hub\"} 2\n",
		"privo_upstream_breaker_state{server=\"linear\"} 1\n",
		"privo_upstream_breaker_state{server=\"slack\"} 0\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, banned := range []string{"org", "org_id", "user", "token", "item", "tool"} {
		if strings.Contains(out, banned+"=\"") {
			t.Errorf("banned label %q present in:\n%s", banned, out)
		}
	}
}
