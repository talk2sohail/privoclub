package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// A removed member comes back with the same account and roles, and nothing
// else from before (F14 third slice).
func TestRestoreUser(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "2001", "ada", RoleMember)
	if _, err := s.SetUserRoles(ctx, org, ada.ID, []string{RoleMember, RoleApprover}, adminEv("roles")); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreUser(ctx, org, ada.ID, adminEv("member.restore:not-removed")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restoring a member who was not removed: %v", err)
	}
	if err := s.DisableUser(ctx, org, ada.ID, t0.Add(time.Hour), adminEv("member.remove:ada")); err != nil {
		t.Fatal(err)
	}
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if err := s.RestoreUser(ctx, other.ID, ada.ID, adminEv("member.restore:wrong-org")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org restored her: %v", err)
	}
	if err := s.RestoreUser(ctx, org, ada.ID, adminEv("member.restore:ada")); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByID(ctx, org, ada.ID)
	if err != nil || u.DisabledAt != nil || !slices.Equal(u.Roles, []string{RoleApprover, RoleMember}) {
		t.Fatalf("restored: %+v %v", u, err)
	}
	if _, err := s.UserByIdentity(ctx, org, ProviderGitHub, "2001"); err != nil {
		t.Fatalf("her GitHub account no longer signs her in: %v", err)
	}
	if acts := auditActions(t, s, org); !slices.Contains(acts, "member.restore:ada") {
		t.Fatalf("audit: %q", acts)
	}
}

// Two people may each call a token claude-code; one person may not have two,
// and service tokens stay unique among themselves.
func TestTokenNamesAreUniquePerPerson(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "2001", "ada", RoleMember)
	bob := member(t, s, org, "2002", "bob", RoleMember)
	tok := func(id, name, userID string) APIToken {
		return APIToken{OrgID: org, ID: id, Name: name, Role: RoleMember, Kind: KindGateway, UserID: userID, SecretHash: "h-" + id, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	}
	if err := s.CreateToken(ctx, tok("t1", "claude-code", ada.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, tok("t2", "claude-code", bob.ID)); err != nil {
		t.Fatalf("bob's claude-code: %v", err)
	}
	if err := s.CreateToken(ctx, tok("t3", "claude-code", ada.ID)); !errors.Is(err, ErrConflict) {
		t.Fatalf("ada's second claude-code: %v", err)
	}
	if err := s.CreateToken(ctx, tok("t4", "ci", "")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, tok("t5", "ci", "")); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second service token named ci: %v", err)
	}
}

// Security review of the third slice, L3: a token that removal did not
// manage to revoke does not come back with the member.
func TestRestoreRevokesLeftoverTokens(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "2001", "ada", RoleMember)
	if err := s.CreateToken(ctx, APIToken{OrgID: org, ID: "t-own", Name: "laptop", Role: RoleMember, Kind: KindGateway, UserID: ada.ID, Person: "ada",
		SecretHash: "h1", CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, APIToken{OrgID: org, ID: "t-label", Name: "ada-ci", Role: RoleMember, Kind: KindGateway, Person: "ada",
		SecretHash: "h2", CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, APIToken{OrgID: org, ID: "t-other", Name: "ci", Role: RoleMember, Kind: KindGateway, Person: "bob",
		SecretHash: "h3", CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// Removed, but the revocation after it never ran.
	if err := s.DisableUser(ctx, org, ada.ID, t0.Add(time.Hour), adminEv("member.remove:ada")); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreUser(ctx, org, ada.ID, adminEv("member.restore:ada")); err != nil {
		t.Fatal(err)
	}
	toks, _ := s.ListTokens(ctx, org)
	var live []string
	for _, tk := range toks {
		live = append(live, tk.ID)
	}
	if !slices.Equal(live, []string{"t-other"}) {
		t.Fatalf("live tokens after restore: %q", live)
	}
}
