package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// F07 review R-01: a token finds its member by id, never by the person name
// it carries.

func TestTokenCarriesUserID(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u-ada", "ada")
	own := APIToken{OrgID: org, ID: "tok-own", Name: "laptop", Role: RoleMember, Kind: KindGateway, Person: "ada", UserID: "u-ada",
		SecretHash: "h1", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	ci := APIToken{OrgID: org, ID: "tok-ci", Name: "ci", Role: RoleMember, Kind: KindGateway, Person: "ci", SecretHash: "h2",
		CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	for _, tok := range []APIToken{own, ci} {
		if err := s.CreateToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.TokenByID(ctx, "tok-own"); err != nil || got.UserID != "u-ada" {
		t.Fatalf("own token: %+v %v", got, err)
	}
	if got, err := s.TokenByID(ctx, "tok-ci"); err != nil || got.UserID != "" {
		t.Fatalf("automation token: %+v %v", got, err)
	}
	list, err := s.ListTokens(ctx, org)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, tok := range list {
		if want := map[string]string{"tok-own": "u-ada", "tok-ci": ""}[tok.ID]; tok.UserID != want {
			t.Errorf("listed %s with user %q, want %q", tok.ID, tok.UserID, want)
		}
	}
}

func TestUserByID(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u-ada", "ada")
	if u, err := s.UserByID(ctx, org, "u-ada"); err != nil || u.Person != "ada" {
		t.Fatalf("UserByID: %+v %v", u, err)
	}
	if _, err := s.UserByID(ctx, org, "u-nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	other, err := s.EnsureOrg(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByID(ctx, other.ID, "u-ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a user of another org: %v", err)
	}
}

// The migration binds the tokens a person made for themselves while signed in,
// and nothing else: not a token an admin made with their person, not one the
// CLI made, not an automation token.
func TestMigrationBindsOnlyAPersonsOwnTokens(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.w, mustSub(migrations, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 6); err != nil {
		t.Fatalf("down to 6: %v", err)
	}
	for _, u := range [][2]string{{"u-ada", "ada"}, {"u-boss", "boss"}} { // users as stored at version 6
		if _, err := s.w.ExecContext(ctx, `INSERT INTO users (org_id, id, person, display_name, role, created_at) VALUES (?, ?, ?, ?, 'member', ?)`,
			org, u[0], u[1], u[1], ts(t0)); err != nil {
			t.Fatal(err)
		}
	}
	token := func(id, name, person string, created time.Time) {
		t.Helper()
		if _, err := s.w.ExecContext(ctx, `INSERT INTO api_tokens (org_id, id, name, role, kind, person, secret_hash, created_at, expires_at)
			VALUES (?, ?, ?, 'member', 'gateway', ?, ?, ?, ?)`, org, id, name, person, "h-"+id, ts(created), ts(created.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	event := func(actorID, name string, at time.Time) {
		t.Helper()
		if _, err := s.w.ExecContext(ctx, `INSERT INTO audit_events (org_id, id, call_id, phase, ts, actor, actor_id, action, decision, outcome)
			VALUES (?, ?, ?, 'admin', ?, 'x', ?, ?, 'allow', 'ok')`, org, "ev-"+name, "call-"+name, ts(at), actorID, "token.create:gateway:member:"+name); err != nil {
			t.Fatal(err)
		}
	}
	at := t0.Add(123456789 * time.Nanosecond) // fractional seconds, as ts writes them
	token("own", "laptop", "ada", at)
	event("user:u-ada", "laptop", at.Add(40*time.Millisecond))
	token("by-admin", "ada", "ada", at)
	event("user:u-boss", "ada", at)
	token("cli", "ada-cli", "ada", at) // privo token create writes no event
	token("late", "old", "ada", at)
	event("user:u-ada", "old", at.Add(time.Hour)) // a later token of the same name
	token("ci", "ci", "ci", at)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	want := map[string]string{"own": "u-ada", "by-admin": "", "cli": "", "late": "", "ci": ""}
	for id, user := range want {
		got, err := s.TokenByID(ctx, id)
		if err != nil || got.UserID != user {
			t.Errorf("token %s: user %q (%v), want %q", id, got.UserID, err, user)
		}
	}
}

func TestAuditRecordsAccount(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ev := AuditEvent{ID: "i1", CallID: "c1", Phase: "intent", Time: t0, Actor: "laptop", ActorID: "tok", Server: "linear", Item: "whoami",
		Action: "tools/call", Decision: "allow", Account: "ada (user u-ada)"}
	if err := s.InsertAudit(ctx, org, ev); err != nil {
		t.Fatal(err)
	}
	calls, err := s.RecentCalls(ctx, org, 10)
	if err != nil || len(calls) != 1 || calls[0].Intent.Account != "ada (user u-ada)" {
		t.Fatalf("calls: %+v %v", calls, err)
	}
}
