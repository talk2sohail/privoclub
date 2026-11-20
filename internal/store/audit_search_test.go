package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// F12: search, filters and pages of the audit log.
func TestSearchAudit(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	var evs []AuditEvent
	for i := 0; i < 6; i++ {
		srv, actor, decision := "github", "ada-laptop", "allow"
		if i%2 == 1 {
			srv, actor, decision = "linear", "bob-laptop", "denied:rule r1"
		}
		at := t0.Add(time.Duration(i) * time.Minute)
		evs = append(evs,
			AuditEvent{ID: fmt.Sprintf("i%d", i), CallID: fmt.Sprintf("c%d", i), Phase: "intent", Time: at, Actor: actor, ActorID: "tok-" + actor, Server: srv,
				Item: "t", Action: "tools/call", Decision: decision},
			AuditEvent{ID: fmt.Sprintf("o%d", i), CallID: fmt.Sprintf("c%d", i), Phase: "completion", Time: at.Add(time.Second), Outcome: "ok", LatencyMS: 12})
	}
	evs = append(evs, AuditEvent{ID: "a1", CallID: "a1", Phase: "admin", Time: t0, Actor: "boss", ActorID: "user:boss", Action: "policy.create:deny", Decision: "allow", Outcome: "ok"})
	if err := s.InsertAudit(ctx, org, evs...); err != nil {
		t.Fatal(err)
	}
	ids := func(rs []CallRecord) string {
		out := ""
		for _, r := range rs {
			out += r.Intent.ID + " "
		}
		return out
	}
	all, next, err := s.SearchAudit(ctx, org, AuditQuery{Limit: 4})
	if err != nil || ids(all) != "i5 i4 i3 i2 " || next == "" || all[0].Completion == nil || all[0].Completion.LatencyMS != 12 {
		t.Fatalf("first page: %q %q %v", ids(all), next, err)
	}
	rest, next2, _ := s.SearchAudit(ctx, org, AuditQuery{Limit: 4, Before: next})
	if ids(rest) != "i1 i0 " || next2 != "" {
		t.Fatalf("second page: %q %q", ids(rest), next2)
	}
	for name, q := range map[string]AuditQuery{
		"i4 i2 i0 ": {Actor: "ada-laptop"},
		"i5 i3 i1 ": {Server: "linear"},
		"i3 i1 ":    {Decision: "denied", To: t0.Add(4 * time.Minute)},
		"i3 i2 ":    {From: t0.Add(2 * time.Minute), To: t0.Add(4 * time.Minute)},
	} {
		got, _, err := s.SearchAudit(ctx, org, q)
		if err != nil || ids(got) != name {
			t.Errorf("%+v: %q %v, want %q", q, ids(got), err, name)
		}
	}
	admin, _, _ := s.SearchAudit(ctx, org, AuditQuery{Kind: "admin", Action: "policy."})
	if ids(admin) != "a1 " {
		t.Fatalf("admin: %q", ids(admin))
	}
	for _, bad := range []AuditQuery{{Kind: "secrets"}, {Before: "nonsense"}} {
		if _, _, err := s.SearchAudit(ctx, org, bad); !errors.Is(err, ErrBadAuditQuery) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if got, _, _ := s.SearchAudit(ctx, other.ID, AuditQuery{}); len(got) != 0 {
		t.Fatal("another org's events")
	}
}
