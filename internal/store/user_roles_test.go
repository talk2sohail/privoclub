package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// ADR 0025: a person holds a set of roles. The catalog-grant side (losing
// owner and admin drops it, promotion reads user_roles) is covered by
// TestCatalogGrantFirstManagerAndPromotion.

func TestRoleSetRules(t *testing.T) {
	for _, ok := range [][]string{
		{RoleOwner}, {RoleAdmin}, {RoleMember}, {RoleApprover}, {RoleAuditor},
		{RoleMember, RoleApprover}, {RoleAuditor, RoleApprover, RoleMember}, {" member ", RoleMember},
	} {
		if _, err := NormalizeRoles(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range [][]string{
		nil, {}, {""}, {"root"}, {RoleOwner, RoleAdmin}, {RoleOwner, RoleMember}, {RoleAdmin, RoleApprover}, {RoleMember, "root"},
	} {
		if _, err := NormalizeRoles(bad); !errors.Is(err, ErrInvalidRoles) {
			t.Errorf("%q accepted (%v)", bad, err)
		}
	}
	got, _ := NormalizeRoles([]string{RoleMember, RoleApprover, RoleMember})
	if !slices.Equal(got, []string{RoleApprover, RoleMember}) {
		t.Errorf("normalised to %q, want sorted without repeats", got)
	}
}

func TestSetUserRoles(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u-ada", "ada")
	ev := adminEv("member.roles:ada")
	ev.ActorID, ev.Time = "user:u-boss", t0.Add(time.Hour)
	got, err := s.SetUserRoles(ctx, org, "u-ada", []string{RoleApprover, RoleMember}, ev)
	if err != nil || !slices.Equal(got, []string{RoleApprover, RoleMember}) {
		t.Fatalf("set: %q %v", got, err)
	}
	u, _ := s.UserByID(ctx, org, "u-ada")
	if !slices.Equal(u.Roles, []string{RoleApprover, RoleMember}) || !u.Has(RoleApprover) || u.Has(RoleAdmin) {
		t.Fatalf("stored %q", u.Roles)
	}
	// A kept role keeps its grant; a new one records who granted it.
	var memberBy, approverBy string
	_ = s.r.QueryRowContext(ctx, `SELECT granted_by FROM user_roles WHERE org_id = ? AND user_id = 'u-ada' AND role = 'member'`, org).Scan(&memberBy)
	_ = s.r.QueryRowContext(ctx, `SELECT granted_by FROM user_roles WHERE org_id = ? AND user_id = 'u-ada' AND role = 'approver'`, org).Scan(&approverBy)
	if memberBy != "" || approverBy != "user:u-boss" {
		t.Errorf("granted by: member %q, approver %q", memberBy, approverBy)
	}
	if !slices.Contains(auditActions(t, s, org), "member.roles:ada") {
		t.Error("no audit event")
	}
	// Dropping a role removes it; an invalid set changes nothing.
	ev.ID = "ev-2"
	if _, err := s.SetUserRoles(ctx, org, "u-ada", []string{RoleApprover}, ev); err != nil {
		t.Fatal(err)
	}
	ev.ID = "ev-3"
	if _, err := s.SetUserRoles(ctx, org, "u-ada", []string{RoleAdmin, RoleMember}, ev); !errors.Is(err, ErrInvalidRoles) {
		t.Fatalf("mixed set: %v", err)
	}
	if u, _ := s.UserByID(ctx, org, "u-ada"); !slices.Equal(u.Roles, []string{RoleApprover}) {
		t.Fatalf("after drop and refusal: %q", u.Roles)
	}
	if _, err := s.SetUserRoles(ctx, org, "u-nobody", []string{RoleMember}, ev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestUserRolesAreIsolatedPerOrg(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	other, err := s.EnsureOrg(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	addUser(t, s, org, "u-ada", "ada")
	addUser(t, s, other.ID, "u-ada", "ada")
	if _, err := s.SetUserRoles(ctx, org, "u-ada", []string{RoleAdmin}, adminEv("x")); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, other.ID, "u-ada"); !slices.Equal(u.Roles, []string{RoleMember}) {
		t.Fatalf("the other org's ada has %q", u.Roles)
	}
	if acc, _ := s.AccessFor(ctx, other.ID, "u-ada"); !slices.Equal(acc.Roles, []string{RoleMember}) {
		t.Fatalf("the other org's access has %q", acc.Roles)
	}
}

func TestAccessCarriesRoles(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u-ada", "ada")
	if _, err := s.SetUserRoles(ctx, org, "u-ada", []string{RoleMember, RoleApprover}, adminEv("x")); err != nil {
		t.Fatal(err)
	}
	if acc, _ := s.AccessFor(ctx, org, "u-ada"); !slices.Equal(acc.Roles, []string{RoleApprover, RoleMember}) {
		t.Fatalf("roles %q", acc.Roles)
	}
	if acc, _ := s.AccessFor(ctx, org, ""); acc.Roles != nil {
		t.Fatalf("a token without a member has roles %q", acc.Roles)
	}
}

func TestInviteCarriesRoleSet(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	in := NewInvite{ID: "i1", SecretHash: "h1", Person: "ada", Roles: []string{RoleMember, RoleApprover}, CreatedBy: "t", Now: t0, ExpiresAt: t0.Add(time.Hour)}
	if err := s.CreateInvite(ctx, org, in, adminEv("invite.create:i1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Invite(ctx, org, "h1", t0)
	if err != nil || !slices.Equal(got.Roles, []string{RoleApprover, RoleMember}) {
		t.Fatalf("invite %+v %v", got, err)
	}
	u, err := s.ClaimInvite(ctx, org, claimFor("i1", "2001", "ignored", "sess-1", t0.Add(time.Minute)), adminEv("invite.claim:i1"))
	if err != nil || !slices.Equal(u.Roles, []string{RoleApprover, RoleMember}) {
		t.Fatalf("claimed %+v %v", u, err)
	}
	if stored, _ := s.UserByID(ctx, org, u.ID); !slices.Equal(stored.Roles, u.Roles) {
		t.Fatalf("stored %q", stored.Roles)
	}
	for name, roles := range map[string][]string{"owner": {RoleOwner}, "mixed": {RoleAdmin, RoleMember}, "none": nil} {
		bad := in
		bad.ID, bad.SecretHash, bad.Person, bad.Roles = "i-"+name, "h-"+name, "bob-"+name, roles
		if err := s.CreateInvite(ctx, org, bad, adminEv("bad-"+name)); err == nil {
			t.Errorf("invite with %s roles accepted", name)
		}
	}
}

// Each person keeps their role; gateway tokens bound to a member become
// member; automation tokens keep theirs.
func TestUserRolesMigration(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.w, mustSub(migrations, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 10); err != nil {
		t.Fatalf("down to 10: %v", err)
	}
	for _, u := range [][3]string{{"u-boss", "boss", "owner"}, {"u-carol", "carol", "approver"}} {
		if _, err := s.w.ExecContext(ctx, `INSERT INTO users (org_id, id, person, display_name, role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			org, u[0], u[1], u[1], u[2], ts(t0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO invites (org_id, id, secret_hash, person, role, created_by, created_at, expires_at) VALUES (?, 'i1', 'h1', 'dan', 'auditor', 't', ?, ?)`,
		org, ts(t0), ts(t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	for _, tk := range [][4]string{{"own", "owner", "gateway", "u-boss"}, {"ci", "admin", "gateway", ""}, {"ctl", "owner", "control", ""}} {
		if _, err := s.w.ExecContext(ctx, `INSERT INTO api_tokens (org_id, id, name, role, kind, person, user_id, secret_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, 'x', ?, ?, ?, ?)`,
			org, tk[0], tk[0], tk[1], tk[2], tk[3], "h-"+tk[0], ts(t0), ts(t0.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	for id, want := range map[string][]string{"u-boss": {RoleOwner}, "u-carol": {RoleApprover}} {
		if u, err := s.UserByID(ctx, org, id); err != nil || !slices.Equal(u.Roles, want) {
			t.Errorf("%s: %q %v, want %q", id, u.Roles, err, want)
		}
	}
	if in, err := s.Invite(ctx, org, "h1", t0); err != nil || !slices.Equal(in.Roles, []string{RoleAuditor}) {
		t.Errorf("invite: %+v %v", in, err)
	}
	for id, want := range map[string]string{"own": RoleMember, "ci": RoleAdmin, "ctl": RoleOwner} {
		if tok, _ := s.TokenByID(ctx, id); tok.Role != want {
			t.Errorf("token %s has role %q, want %q", id, tok.Role, want)
		}
	}
	// Pruning an invite takes its roles with it.
	if _, err := s.w.ExecContext(ctx, `DELETE FROM invites WHERE org_id = ? AND id = 'i1'`, org); err != nil {
		t.Fatalf("deleting an invite with roles: %v", err)
	}
}
