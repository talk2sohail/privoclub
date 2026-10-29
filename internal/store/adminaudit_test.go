package store

import (
	"context"
	"testing"
	"time"

	"privo.club/privo/internal/ids"
)

// R-09 (independent review, 2026-10-29): admin events were written and could
// not be read back. They have their own phase, so RecentCalls never returned
// them.
func TestRecentAdminEvents(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	base := time.Now()
	admin := func(org OrgID, action string, at time.Duration) {
		t.Helper()
		ev := AuditEvent{ID: ids.New(), CallID: ids.New(), Phase: "admin", Time: base.Add(at), Actor: "system", ActorID: "t1", Server: "echo", Action: action, Decision: "allow", Outcome: "ok"}
		if err := s.InsertAudit(ctx, org, ev); err != nil {
			t.Fatal(err)
		}
	}
	admin(a, "token.create:first", 0)
	admin(a, "token.revoke:second", time.Second)
	admin(a, "policy.create:third", 2*time.Second)
	admin(b, "server.create", time.Second)
	// A call is not an admin event.
	call := ids.New()
	if err := s.InsertAudit(ctx, a, AuditEvent{ID: ids.New(), CallID: call, Phase: "intent", Time: base.Add(3 * time.Second), Actor: "ada", Server: "echo", Item: "echo", Action: "tools/call", Decision: "allow"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentAdminEvents(ctx, a, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Action != "policy.create:third" || got[1].Action != "token.revoke:second" || got[2].Action != "token.create:first" {
		t.Fatalf("events (newest first, only org a's admin events) = %+v", got)
	}
	if e := got[0]; e.Actor != "system" || e.ActorID != "t1" || e.Server != "echo" || e.Decision != "allow" || e.Outcome != "ok" || e.Phase != "admin" || e.Time.IsZero() {
		t.Fatalf("event fields %+v", e)
	}
	if calls, _ := s.RecentCalls(ctx, a, 10); len(calls) != 1 {
		t.Fatalf("admin events leaked into the call list: %+v", calls)
	}
	if limited, _ := s.RecentAdminEvents(ctx, a, 2); len(limited) != 2 || limited[0].Action != "policy.create:third" {
		t.Fatalf("limit 2 = %+v", limited)
	}
	if other, _ := s.RecentAdminEvents(ctx, b, 10); len(other) != 1 || other[0].Action != "server.create" {
		t.Fatalf("org b sees %+v", other)
	}
	// Same bounds as the call list: zero or too many means the default.
	for _, limit := range []int{0, -1, 501} {
		if events, err := s.RecentAdminEvents(ctx, a, limit); err != nil || len(events) != 3 {
			t.Fatalf("limit %d: %d events, %v", limit, len(events), err)
		}
	}
}
