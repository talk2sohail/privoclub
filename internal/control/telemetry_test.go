package control

import (
	"context"
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
	if total["calls"].(float64) != 12 || total["errors"].(float64) != 1 || total["refused"].(float64) != 1 || sum["truncated"] != false {
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

	stor := e.call(t, owner, http.MethodGet, "/api/v1/telemetry/storage", nil, http.StatusOK)
	if stor["recording"] != true || stor["retention_days"].(float64) != 30 || stor["calls"].(float64) != 13 || stor["logs"].(float64) != 2 || stor["oldest_call"] == nil {
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
