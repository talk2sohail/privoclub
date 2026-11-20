package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// F10 second slice: pages of approvals, a person's own requests, withdrawing
// and rules for one person.

func approvalAt(t *testing.T, s *Store, org OrgID, id, requester string, created time.Time) {
	t.Helper()
	a := Approval{ID: id, RequesterID: requester, RequesterName: requester, RequesterPerson: "p", ServerID: "srv", ServerAlias: "github",
		Tool: "merge", ArgsDigest: id, Args: []byte("x"), RuleID: "rule-1", CreatedAt: created, ExpiresAt: created.Add(10 * time.Minute)}
	if err := s.CreateApproval(context.Background(), org, a); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalPages(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	now := t0.Add(time.Hour)
	// 7 pending (two created in the same instant), 3 that expired, 2 decided.
	for i := 0; i < 7; i++ {
		at := now.Add(-time.Duration(i) * time.Minute)
		if i == 6 {
			at = now.Add(-5 * time.Minute)
		}
		approvalAt(t, s, org, fmt.Sprintf("p%d", i), "tok", at)
	}
	for i := 0; i < 3; i++ {
		approvalAt(t, s, org, fmt.Sprintf("x%d", i), "tok", t0.Add(time.Duration(i)*time.Second))
	}
	for i := 0; i < 2; i++ {
		approvalAt(t, s, org, fmt.Sprintf("d%d", i), "tok", now.Add(-time.Duration(i)*time.Second))
		if ok, err := s.DecideApproval(ctx, org, fmt.Sprintf("d%d", i), ApprovalDenied, "boss", now); err != nil || !ok {
			t.Fatal(err)
		}
	}
	if got, _ := s.GetApproval(ctx, org, "p0"); got.RuleID != "rule-1" {
		t.Fatalf("rule id not kept: %+v", got)
	}

	walk := func(view string) []string {
		var ids []string
		before := ""
		for pages := 0; pages < 10; pages++ {
			page, err := s.ListApprovalsPage(ctx, org, view, before, 3, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range page.Approvals {
				ids = append(ids, a.ID)
			}
			if page.Next == "" {
				return ids
			}
			before = page.Next
		}
		t.Fatal("pages never end")
		return nil
	}
	pending := walk(ViewPending)
	if len(pending) != 7 || pending[0] != "p0" {
		t.Fatalf("pending %q", pending)
	}
	seen := map[string]bool{}
	for _, id := range pending {
		if seen[id] {
			t.Fatalf("%s twice in %q", id, pending)
		}
		seen[id] = true
	}
	history := walk(ViewHistory)
	if len(history) != 5 || history[0] != "d0" {
		t.Fatalf("history %q", history)
	}
	if _, err := s.ListApprovalsPage(ctx, org, ViewPending, "nonsense", 3, now); !errors.Is(err, ErrBadCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if page, _ := s.ListApprovalsPage(ctx, other.ID, ViewHistory, "", 50, now); len(page.Approvals) != 0 {
		t.Fatal("another org's approvals listed")
	}
}

func TestMyApprovalsAndWithdraw(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u-ada", "ada")
	addUser(t, s, org, "u-bob", "bob")
	for _, tok := range []APIToken{
		{OrgID: org, ID: "tok-ada", Name: "ada-laptop", Role: RoleMember, Kind: KindGateway, Person: "ada", UserID: "u-ada", SecretHash: "h1", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)},
		{OrgID: org, ID: "tok-bob", Name: "bob-laptop", Role: RoleMember, Kind: KindGateway, Person: "bob", UserID: "u-bob", SecretHash: "h2", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)},
		{OrgID: org, ID: "tok-ci", Name: "ci", Role: RoleMember, Kind: KindGateway, Person: "ada", SecretHash: "h3", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)},
	} {
		if err := s.CreateToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	approvalAt(t, s, org, "a1", "tok-ada", t0)
	approvalAt(t, s, org, "b1", "tok-bob", t0)
	approvalAt(t, s, org, "c1", "tok-ci", t0) // names ada but is no token of hers
	mine, err := s.MyApprovals(ctx, org, "u-ada", t0)
	if err != nil || len(mine) != 1 || mine[0].ID != "a1" {
		t.Fatalf("ada's requests: %+v %v", mine, err)
	}
	if got, _ := s.MyApprovals(ctx, org, "", t0); len(got) != 0 {
		t.Fatal("no member, yet requests")
	}
	for _, id := range []string{"b1", "c1", "nope"} {
		if ok, _ := s.WithdrawApproval(ctx, org, id, "u-ada", "ada", t0); ok {
			t.Fatalf("ada withdrew %s", id)
		}
	}
	if ok, err := s.WithdrawApproval(ctx, org, "a1", "u-ada", "ada", t0); err != nil || !ok {
		t.Fatalf("withdraw own: %v %v", ok, err)
	}
	a, _ := s.GetApproval(ctx, org, "a1")
	if a.Status != ApprovalWithdrawn || len(a.Args) != 0 || a.DecidedBy != "ada" {
		t.Fatalf("withdrawn: %+v", a)
	}
	if ok, _ := s.WithdrawApproval(ctx, org, "a1", "u-ada", "ada", t0); ok {
		t.Fatal("withdrawn twice")
	}
}

func TestUserRuleNeedsAMember(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u-ada", "ada")
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	addUser(t, s, other.ID, "u-eve", "eve")
	rule := Policy{ID: "r1", Server: "*", Tool: "*", Subject: "user:u-eve", Action: ActionAllow, CreatedBy: "t", CreatedAt: t0}
	if err := s.CreatePolicies(ctx, org, rule); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("a member of another org: %v", err)
	}
	rule.Subject = "user:u-ada"
	if err := s.CreatePolicies(ctx, org, rule); err != nil {
		t.Fatalf("a member: %v", err)
	}
	if err := s.DisableUser(ctx, org, "u-ada", t0, adminEv("remove")); err != nil {
		t.Fatal(err)
	}
	rule.ID = "r2"
	if err := s.CreatePolicies(ctx, org, rule); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("a removed member: %v", err)
	}
}
