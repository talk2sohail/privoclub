package control

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"privo.club/privo/internal/store"
)

// F28, ADR 0024: the telemetry endpoints.

func seedStats(t *testing.T, e *sessionEnv, userID string) {
	t.Helper()
	now := time.Now()
	var rows []store.CallStat
	for i := range 10 {
		rows = append(rows, store.CallStat{ID: "ok-" + string(rune('a'+i)), Time: now.Add(-time.Duration(i) * time.Minute), UserID: userID,
			TokenID: "t1", Client: "claude", Server: "github", Method: "tools/call", Item: "search", Outcome: store.OutcomeOK,
			DecideUS: 100, AuditUS: 200, ConnectUS: 300, UpstreamUS: int64(i+1) * 1000, TotalUS: int64(i+1)*1000 + 600, ArgsBytes: 20, ResultBytes: 2000})
	}
	rows = append(rows,
		store.CallStat{ID: "err-1", Time: now.Add(-2 * time.Minute), UserID: userID, Server: "linear", Method: "tools/call", Item: "issue",
			Outcome: store.OutcomeError, ErrorClass: "timeout", TotalUS: 50_000},
		store.CallStat{ID: "ref-1", Time: now.Add(-3 * time.Minute), UserID: userID, Server: "linear", Method: "tools/call", Item: "issue",
			Outcome: store.OutcomeRefused, ErrorClass: "denied", TotalUS: 90},
		// Outside a one-hour window.
		store.CallStat{ID: "old-1", Time: now.Add(-3 * time.Hour), Server: "github", Method: "tools/call", Item: "search", Outcome: store.OutcomeOK, TotalUS: 1})
	if err := e.st.InsertCallStats(context.Background(), e.org, rows...); err != nil {
		t.Fatal(err)
	}
	// Another org's statistics must never show.
	other, err := e.st.EnsureOrg(context.Background(), "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.InsertCallStats(context.Background(), other.ID, store.CallStat{ID: "x", Time: now, Server: "secret-server",
		Method: "tools/call", Outcome: store.OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.InsertDiagnostics(context.Background(), e.org,
		store.Diagnostic{ID: "d1", Time: now.Add(-time.Minute), Level: store.LevelWarn, Category: store.CategoryUpstream, Server: "linear", Message: "connection failed: 502"},
		store.Diagnostic{ID: "d2", Time: now, Level: store.LevelWarn, Category: store.CategoryGrant, Server: "linear", UserID: userID, Message: "the server refused a member's own API key", Repeats: 3}); err != nil {
		t.Fatal(err)
	}
}

func TestTelemetryAPI(t *testing.T) {
	e := newSessionEnv(t)
	e.api.TelemetryRetentionDays = 30
	ada, owner := e.user(t, "ada", store.RoleOwner)
	seedStats(t, e, ada.ID)

	sum := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/summary?window=1h&by=server", nil, http.StatusOK)
	total := sum["total"].(map[string]any)
	if total["calls"].(float64) != 12 || total["errors"].(float64) != 1 || total["refused"].(float64) != 1 || sum["sampled"] != false {
		t.Fatalf("total: %v", sum)
	}
	groups := sum["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("groups: %v (another org's server or the old row leaked?)", groups)
	}
	gh := groups[0].(map[string]any)
	if gh["key"] != "github" || gh["total_ms"].(map[string]any)["p50"].(float64) != 5.6 || gh["upstream_ms"].(map[string]any)["max"].(float64) != 10 {
		t.Fatalf("github: %v", gh)
	}
	byMember := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/summary?window=1h&by=member", nil, http.StatusOK)
	if labels := byMember["labels"].(map[string]any); labels[ada.ID] != "ada" {
		t.Fatalf("member labels: %v", byMember["labels"])
	}

	series := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/timeseries?window=1h&bucket=10m", nil, http.StatusOK)
	var calls float64
	for _, b := range series["buckets"].([]any) {
		calls += b.(map[string]any)["calls"].(float64)
	}
	if n := len(series["buckets"].([]any)); n < 6 || n > 7 || calls != 12 {
		t.Fatalf("series: %d buckets, %v calls", n, calls)
	}

	list := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/calls?server=linear", nil, http.StatusOK)
	cs := list["calls"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["error_class"] != "timeout" || cs[0].(map[string]any)["member"] != "ada" {
		t.Fatalf("calls: %v", cs)
	}
	if lim := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/calls?limit=3", nil, http.StatusOK); len(lim["calls"].([]any)) != 3 || lim["more"] != true {
		t.Fatalf("limit: %v", lim)
	}

	logs := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/logs?category=grant", nil, http.StatusOK)["logs"].([]any)
	if len(logs) != 1 || logs[0].(map[string]any)["repeats"].(float64) != 3 || logs[0].(map[string]any)["member"] != "ada" {
		t.Fatalf("logs: %v", logs)
	}

	// An answer is reused for a few seconds, so polling does not multiply the
	// work (review of 2026-11-15, T-03); another query is computed.
	if err := e.st.InsertCallStats(context.Background(), e.org, store.CallStat{ID: "late", Time: time.Now(), Server: "github",
		Method: "tools/call", Outcome: store.OutcomeOK, TotalUS: 1}); err != nil {
		t.Fatal(err)
	}
	again := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/summary?window=1h&by=server", nil, http.StatusOK)
	if again["total"].(map[string]any)["calls"].(float64) != 12 {
		t.Fatalf("the cached answer was not used: %v", again["total"])
	}
	fresh := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/summary?window=2h&by=server", nil, http.StatusOK)
	if fresh["total"].(map[string]any)["calls"].(float64) != 13 {
		t.Fatalf("another window: %v", fresh["total"])
	}
	// Over the bound reads the bound, not the default (T-09).
	if big := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/calls?limit=100000", nil, http.StatusOK); len(big["calls"].([]any)) != 14 {
		t.Fatalf("limit over the bound: %d", len(big["calls"].([]any)))
	}

	stor := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/storage", nil, http.StatusOK)
	if stor["recording"] != true || stor["retention_days"].(float64) != 30 || stor["calls"].(float64) != 14 || stor["logs"].(float64) != 2 || stor["oldest_call"] == nil {
		t.Fatalf("storage: %v", stor)
	}
}

func TestTelemetryAPINeedsAuditRead(t *testing.T) {
	e := newSessionEnv(t)
	_, member := e.user(t, "bob", store.RoleMember)
	_, approver := e.user(t, "cy", store.RoleApprover)
	_, auditor := e.user(t, "di", store.RoleAuditor)
	for _, path := range []string{"summary", "timeseries", "calls", "logs", "storage"} {
		e.call(t, member, http.MethodGet, "/api/v1/telemetry/"+path, nil, http.StatusForbidden)
		e.call(t, approver, http.MethodGet, "/api/v1/telemetry/"+path, nil, http.StatusForbidden)
		e.call(t, auditor, http.MethodGet, "/api/v1/telemetry/"+path, nil, http.StatusOK)
	}
	// Recording off: the pages say so, with no error.
	if s := e.call(t, auditor, http.MethodGet, "/api/v1/telemetry/storage", nil, http.StatusOK); s["recording"] != false {
		t.Fatalf("storage with recording off: %v", s)
	}
}

func TestTelemetryWindowLimits(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "ada", store.RoleOwner)
	for _, path := range []string{
		"summary?window=30s",
		"summary?window=32d",
		"summary?window=99999999999d", // no overflow (T-09)
		"summary?window=a+week",
		"summary?by=team",
		"summary?outcome=maybe",
		"timeseries?window=1h&bucket=1s",
		"timeseries?window=30d&bucket=1m", // 43,200 buckets
		"timeseries?bucket=soon",
		"calls?before=yesterday",
		"logs?category=secrets",
		"logs?level=debug",
	} {
		e.call(t, owner, http.MethodGet, "/api/v1/telemetry/"+path, nil, http.StatusBadRequest)
	}
	for _, path := range []string{"summary?window=7d&by=tool", "timeseries?window=30d", "timeseries?window=15m"} {
		e.call(t, owner, http.MethodGet, "/api/v1/telemetry/"+path, nil, http.StatusOK)
	}
}

// Review of 2026-11-15, T-09: the chart counts the same calls as the totals,
// even when a call falls in the first bucket but before the window; a limit
// over the bound reads the bound.
func TestTelemetryChartAndTotalsAgree(t *testing.T) {
	e := newSessionEnv(t)
	e.api.TelemetryRetentionDays = 30
	_, owner := e.user(t, "ada", store.RoleOwner)
	now := time.Now()
	var rows []store.CallStat
	for i := range 150 {
		rows = append(rows, store.CallStat{ID: fmt.Sprint("c", i), Time: now.Add(-time.Duration(i) * 20 * time.Second), Server: "github",
			Method: "tools/call", Outcome: store.OutcomeOK, TotalUS: 1000})
	}
	// Just before the window, in the first bucket's time.
	rows = append(rows, store.CallStat{ID: "before", Time: now.Add(-61 * time.Minute), Server: "github", Method: "tools/call", Outcome: store.OutcomeOK})
	if err := e.st.InsertCallStats(context.Background(), e.org, rows...); err != nil {
		t.Fatal(err)
	}
	sum := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/summary?window=1h", nil, http.StatusOK)
	series := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/timeseries?window=1h&bucket=30m", nil, http.StatusOK)
	var chart float64
	for _, b := range series["buckets"].([]any) {
		chart += b.(map[string]any)["calls"].(float64)
	}
	if total := sum["total"].(map[string]any)["calls"].(float64); chart != total || total != 150 {
		t.Fatalf("the chart counts %v calls, the totals %v, want 150", chart, total)
	}
	if got := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/calls?limit=100000", nil, http.StatusOK); len(got["calls"].([]any)) != 151 {
		t.Fatalf("a limit over the bound: %d calls, want all 151 (bound 500)", len(got["calls"].([]any)))
	}
}
