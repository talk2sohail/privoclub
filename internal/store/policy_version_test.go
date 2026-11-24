package store

import (
	"context"
	"errors"
	"testing"
)

func TestEveryPolicyWriteBumpsTheVersion(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	org, other := twoOrgs(t, s)

	ver := func(o OrgID) int64 {
		t.Helper()
		v, err := s.PolicyVersion(ctx, o)
		if err != nil {
			t.Fatalf("PolicyVersion(%q): %v", o, err)
		}
		return v
	}

	if v := ver(org); v != 1 {
		t.Fatalf("initial policy_version = %d, want 1", v)
	}
	if _, err := s.PolicyVersion(ctx, "no-such-org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PolicyVersion(unknown) = %v, want ErrNotFound", err)
	}

	// CreatePolicies bumps the version; a failed CreatePolicies does not.
	p1 := Policy{ID: "p1", Server: "gh", Tool: "*", Subject: "*", Action: ActionAsk, CreatedBy: "t", CreatedAt: t0}
	if err := s.CreatePolicies(ctx, org, p1); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 2 {
		t.Fatalf("after CreatePolicies: version = %d, want 2", v)
	}
	badRule := Policy{ID: "p-bad", Server: "*", Tool: "*", Subject: "team:missing", Action: ActionDeny, CreatedBy: "t", CreatedAt: t0}
	if err := s.CreatePolicies(ctx, org, badRule); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("failed CreatePolicies: %v", err)
	}
	if v := ver(org); v != 2 {
		t.Fatalf("after failed CreatePolicies: version = %d, want 2", v)
	}

	// DeletePolicy bumps; deleting an unknown id does not.
	if err := s.DeletePolicy(ctx, org, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeletePolicy(missing): %v", err)
	}
	if v := ver(org); v != 2 {
		t.Fatalf("after failed DeletePolicy: version = %d, want 2", v)
	}
	if err := s.DeletePolicy(ctx, org, "p1"); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 3 {
		t.Fatalf("after DeletePolicy: version = %d, want 3", v)
	}

	// ImportPolicies bumps (merge and replace); a failed import rolls back.
	mkTeam(t, s, org, "designers")
	if err := s.ImportPolicies(ctx, org, true, []Policy{badRule}, "policy.default", ActionDeny, adminEv("imp-bad")); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("failed ImportPolicies: %v", err)
	}
	if v := ver(org); v != 3 {
		t.Fatalf("after failed ImportPolicies: version = %d, want 3", v)
	}
	imp1 := Policy{ID: "imp1", Server: "*", Tool: "*", Subject: "team:designers", Action: ActionAllow, CreatedBy: "t", CreatedAt: t0}
	if err := s.ImportPolicies(ctx, org, false, []Policy{imp1}, "policy.default", "", adminEv("imp-merge")); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 4 {
		t.Fatalf("after ImportPolicies(merge): version = %d, want 4", v)
	}
	if err := s.ImportPolicies(ctx, org, true, nil, "policy.default", ActionAsk, adminEv("imp-replace")); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 5 {
		t.Fatalf("after ImportPolicies(replace): version = %d, want 5", v)
	}

	// SetSetting bumps for policy.* keys, not unrelated settings.
	if err := s.SetSetting(ctx, org, "audit.record_arguments", "true"); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 5 {
		t.Fatalf("after non-policy SetSetting: version = %d, want 5", v)
	}
	if err := s.SetSetting(ctx, org, "policy.default", ActionDeny); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 6 {
		t.Fatalf("after SetSetting(policy.default): version = %d, want 6", v)
	}
	if err := s.SetSetting(ctx, org, "policy.extra", "x"); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 7 {
		t.Fatalf("after SetSetting(policy.extra): version = %d, want 7", v)
	}

	// DeleteServer bumps (and a missing server does not).
	addServer(t, s, org, "s1", "gh", nil)
	if err := s.CreatePolicies(ctx, org, Policy{ID: "p2", Server: "gh", Tool: "*", Subject: "*", Action: ActionAllow, CreatedBy: "t", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 8 {
		t.Fatalf("version = %d, want 8", v)
	}
	if err := s.DeleteServer(ctx, org, "missing-srv"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteServer(missing): %v", err)
	}
	if v := ver(org); v != 8 {
		t.Fatalf("after failed DeleteServer: version = %d, want 8", v)
	}
	if err := s.DeleteServer(ctx, org, "s1"); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 9 {
		t.Fatalf("after DeleteServer: version = %d, want 9", v)
	}

	// DeleteTeam bumps (and a missing team does not).
	if err := s.DeleteTeam(ctx, org, "missing-team", adminEv("del-missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteTeam(missing): %v", err)
	}
	if v := ver(org); v != 9 {
		t.Fatalf("after failed DeleteTeam: version = %d, want 9", v)
	}
	if err := s.DeleteTeam(ctx, org, "designers", adminEv("del-designers")); err != nil {
		t.Fatal(err)
	}
	if v := ver(org); v != 10 {
		t.Fatalf("after DeleteTeam: version = %d, want 10", v)
	}

	// Org isolation: org's writes never bumped other's policy_version.
	if v := ver(other); v != 1 {
		t.Fatalf("other org policy_version = %d, want 1", v)
	}
}
