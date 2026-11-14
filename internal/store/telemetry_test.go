package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func stat(id string, at time.Time, server, outcome string, totalUS int64) CallStat {
	return CallStat{ID: id, Time: at, UserID: "u1", TokenID: "t1", Client: "claude", Server: server, Method: "tools/call",
		Item: "search", Outcome: outcome, DecideUS: 10, AuditUS: 20, ConnectUS: 30, UpstreamUS: totalUS - 60, TotalUS: totalUS,
		ArgsBytes: 12, ResultBytes: 345}
}

func TestCallStatsRoundTripAndScope(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	if err := s.InsertCallStats(ctx, a,
		stat("c1", t0, "github", OutcomeOK, 1000),
		stat("c2", t0.Add(time.Minute), "github", OutcomeError, 2000),
		stat("c3", t0.Add(2*time.Minute), "linear", OutcomeOK, 3000)); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCallStats(ctx, b, stat("c9", t0, "github", OutcomeOK, 9000)); err != nil {
		t.Fatal(err)
	}
	// The same call id twice is one row: a retried flush must not double count.
	if err := s.InsertCallStats(ctx, a, stat("c1", t0, "github", OutcomeOK, 1000)); err != nil {
		t.Fatal(err)
	}

	all, more, err := s.CallStats(ctx, a, CallStatQuery{})
	if err != nil || more || len(all) != 3 {
		t.Fatalf("org a: %d rows, more %v, err %v; want 3 of its own", len(all), more, err)
	}
	if got := all[0]; got != stat("c1", t0, "github", OutcomeOK, 1000) {
		t.Fatalf("round trip: %+v", got)
	}
	for _, r := range all {
		if r.ID == "c9" {
			t.Fatal("another org's row was read")
		}
	}

	gh, _, _ := s.CallStats(ctx, a, CallStatQuery{Server: "github"})
	if len(gh) != 2 {
		t.Fatalf("server filter: %d rows", len(gh))
	}
	win, _, _ := s.CallStats(ctx, a, CallStatQuery{From: t0.Add(time.Minute), To: t0.Add(2 * time.Minute)})
	if len(win) != 1 || win[0].ID != "c2" {
		t.Fatalf("window [from, to): %+v", win)
	}
	newest, more, _ := s.CallStats(ctx, a, CallStatQuery{Newest: true, Limit: 2})
	if len(newest) != 2 || newest[0].ID != "c3" || !more {
		t.Fatalf("newest first with a limit: %+v more %v", newest, more)
	}
	errs, _, _ := s.CallStats(ctx, a, CallStatQuery{Outcome: OutcomeError})
	if len(errs) != 1 || errs[0].ID != "c2" {
		t.Fatalf("outcome filter: %+v", errs)
	}
}

func TestDiagnosticsQuery(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	if err := s.InsertDiagnostics(ctx, a,
		Diagnostic{ID: "d1", Time: t0, Level: LevelWarn, Category: CategoryUpstream, Server: "github", Message: "connect failed"},
		Diagnostic{ID: "d2", Time: t0.Add(time.Minute), Level: LevelInfo, Category: CategoryLifecycle, Message: "started", Repeats: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertDiagnostics(ctx, b, Diagnostic{ID: "d9", Time: t0, Level: LevelError, Category: CategoryAudit, Message: "lost"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Diagnostics(ctx, a, DiagnosticQuery{})
	if err != nil || len(got) != 2 || got[0].ID != "d2" || got[0].Repeats != 3 || got[1].Repeats != 1 {
		t.Fatalf("newest first, repeats kept (0 stored as 1): %+v, %v", got, err)
	}
	up, _ := s.Diagnostics(ctx, a, DiagnosticQuery{Category: CategoryUpstream})
	if len(up) != 1 || up[0].Server != "github" {
		t.Fatalf("category filter: %+v", up)
	}
	warn, _ := s.Diagnostics(ctx, a, DiagnosticQuery{Level: LevelWarn, Since: t0})
	if len(warn) != 1 {
		t.Fatalf("level filter: %+v", warn)
	}
	none, _ := s.Diagnostics(ctx, a, DiagnosticQuery{Category: CategoryAudit})
	if len(none) != 0 {
		t.Fatal("another org's event was read")
	}
}

func TestPruneTelemetry(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var old []CallStat
	for i := range pruneBatch + 7 { // more than one batch
		old = append(old, stat("old"+strconv.Itoa(i), t0.Add(time.Duration(i)*time.Millisecond), "github", OutcomeOK, 100))
	}
	if err := s.InsertCallStats(ctx, a, old...); err != nil {
		t.Fatal(err)
	}
	cut := t0.Add(24 * time.Hour)
	if err := s.InsertCallStats(ctx, a, stat("new", cut, "github", OutcomeOK, 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCallStats(ctx, b, stat("b-old", t0, "github", OutcomeOK, 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertDiagnostics(ctx, a, Diagnostic{ID: "d-old", Time: t0, Level: LevelInfo, Category: CategoryLifecycle, Message: "x"},
		Diagnostic{ID: "d-new", Time: cut, Level: LevelInfo, Category: CategoryLifecycle, Message: "y"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneTelemetry(ctx, a, cut)
	if err != nil || n != int64(len(old))+1 {
		t.Fatalf("pruned %d, err %v; want %d", n, err, len(old)+1)
	}
	info, err := s.TelemetryStorage(ctx, a)
	if err != nil || info.Calls != 1 || !info.OldestCall.Equal(cut) || info.Diagnostics != 1 || !info.OldestDiag.Equal(cut) {
		t.Fatalf("after prune: %+v, %v", info, err)
	}
	if bi, _ := s.TelemetryStorage(ctx, b); bi.Calls != 1 {
		t.Fatalf("pruning org a touched org b: %+v", bi)
	}
	empty, _ := s.TelemetryStorage(ctx, OrgID("none"))
	if empty.Calls != 0 || !empty.OldestCall.IsZero() {
		t.Fatalf("empty org: %+v", empty)
	}
}
