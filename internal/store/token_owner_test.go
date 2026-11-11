package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"privo.club/privo/internal/ids"
)

// F27: a token belongs to a member by user id, never by the name it carries.

func TestTokenBelongsToAMember(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	addUser(t, s, a, "u-ada", "ada")
	addUser(t, s, b, "u-eve", "eve")
	exp := time.Now().Add(time.Hour)

	bound := APIToken{OrgID: a, ID: "t-bound", Name: "laptop", Role: RoleMember, Kind: KindGateway, Person: "ada", UserID: "u-ada", SecretHash: "h", CreatedAt: time.Now(), ExpiresAt: exp}
	if err := s.CreateToken(ctx, bound); err != nil {
		t.Fatal(err)
	}
	// The same person label without a user id is a token of nobody.
	label := APIToken{OrgID: a, ID: "t-label", Name: "ci", Role: RoleMember, Kind: KindGateway, Person: "ada", SecretHash: "h", CreatedAt: time.Now(), ExpiresAt: exp}
	if err := s.CreateToken(ctx, label); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TokenByID(ctx, "t-bound"); err != nil || got.UserID != "u-ada" {
		t.Fatalf("bound token: %+v %v", got, err)
	}
	if got, err := s.TokenByID(ctx, "t-label"); err != nil || got.UserID != "" {
		t.Fatalf("labelled token: %+v %v", got, err)
	}
	list, err := s.ListTokens(ctx, a)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	owners := map[string]string{}
	for _, tok := range list {
		owners[tok.ID] = tok.UserID
	}
	if owners["t-bound"] != "u-ada" || owners["t-label"] != "" {
		t.Fatalf("listed owners %v", owners)
	}
}

func TestTokenCannotBeBoundToSomeoneWhoIsNotAnActiveMember(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	addUser(t, s, a, "u-ada", "ada")
	addUser(t, s, b, "u-eve", "eve")
	addUser(t, s, a, "u-gone", "gone")
	if _, err := s.w.ExecContext(ctx, `UPDATE users SET disabled_at = ? WHERE org_id = ? AND id = 'u-gone'`, ts(t0), a); err != nil {
		t.Fatal(err)
	}
	mk := func(id, user string) error {
		return s.CreateToken(ctx, APIToken{OrgID: a, ID: id, Name: id, Role: RoleMember, Kind: KindGateway, UserID: user, SecretHash: "h", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	}
	for name, user := range map[string]string{"unknown": "u-nobody", "another org's member": "u-eve", "removed member": "u-gone"} {
		if err := mk("t-"+user, user); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
		if _, err := s.TokenByID(ctx, "t-"+user); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: a token was stored", name)
		}
	}
	if err := mk("t-ok", "u-ada"); err != nil {
		t.Fatal(err)
	}
}

// Tokens made before this change carry no owner: the migration invents none.
func TestMigrationLeavesExistingTokensWithoutAnOwner(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := twoOrgs(t, s)
	addUser(t, s, a, "u-ada", "ada")
	// A row as version 0.6.0 wrote it: no user_id column in the statement.
	_, err := s.w.ExecContext(ctx, `INSERT INTO api_tokens (org_id, id, name, role, kind, person, secret_hash, created_at, expires_at)
		VALUES (?, 'old', 'old', 'member', 'gateway', 'ada', 'h', ?, ?)`, a, ts(t0), ts(t0.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.TokenByID(ctx, "old"); err != nil || got.UserID != "" || got.Person != "ada" {
		t.Fatalf("old token: %+v %v", got, err)
	}
}

func TestUserByID(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	addUser(t, s, a, "u-ada", "ada")
	if u, err := s.UserByID(ctx, a, "u-ada"); err != nil || u.Person != "ada" || u.DisabledAt != nil {
		t.Fatalf("ada: %+v %v", u, err)
	}
	if _, err := s.UserByID(ctx, b, "u-ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b found org a's member: %v", err)
	}
	if _, err := s.UserByID(ctx, a, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty id: %v", err)
	}
}

func TestAuditRecordsWhoseAccountACallRanAs(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := twoOrgs(t, s)
	call, service := ids.New(), ids.New()
	now := time.Now()
	if err := s.InsertAudit(ctx, a,
		AuditEvent{ID: ids.New(), CallID: call, Phase: "intent", Time: now, Actor: "laptop", ActorID: "t-bound", UserID: "u-ada", Server: "linear", Item: "whoami", Action: "tools/call", Decision: "allow"},
		AuditEvent{ID: ids.New(), CallID: service, Phase: "intent", Time: now.Add(time.Millisecond), Actor: "ci", ActorID: "t-label", Server: "echo", Item: "echo", Action: "tools/call", Decision: "allow"},
	); err != nil {
		t.Fatal(err)
	}
	calls, err := s.RecentCalls(ctx, a, 10)
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls %+v %v", calls, err)
	}
	got := map[string]string{}
	for _, c := range calls {
		got[c.Intent.ActorID] = c.Intent.UserID
	}
	if got["t-bound"] != "u-ada" || got["t-label"] != "" {
		t.Fatalf("users in the audit: %v", got)
	}
}
