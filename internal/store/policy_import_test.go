package store

import (
	"context"
	"errors"
	"testing"

	"github.com/pressly/goose/v3"
)

// ADR 0024: rules keep their conditions and grant marks; an import is one
// transaction and a replace keeps grants.

func TestPolicyConditionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	p := Policy{ID: "r1", Server: "github", Tool: "delete_repo", Subject: "*", Action: ActionDeny, Conditions: `[{"arg":"repo","op":"equals","value":"prod"}]`,
		CreatedBy: "t", CreatedAt: t0}
	g := Policy{ID: "g1", Server: "github", Tool: "merge", Subject: "*", Action: ActionAllow, GrantOf: "appr-1", CreatedBy: "t", CreatedAt: t0}
	if err := s.CreatePolicies(ctx, org, p, g); err != nil {
		t.Fatal(err)
	}
	rules, err := s.ListPolicies(ctx, org)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules %+v %v", rules, err)
	}
	for _, r := range rules {
		if (r.ID == "r1" && r.Conditions != p.Conditions) || (r.ID == "g1" && r.GrantOf != "appr-1") {
			t.Fatalf("round trip: %+v", r)
		}
	}
}

func TestImportPoliciesKeepsGrants(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	mkTeam(t, s, org, "designers")
	old := Policy{ID: "old", Server: "*", Tool: "*", Subject: "*", Action: ActionAsk, CreatedBy: "t", CreatedAt: t0}
	grant := Policy{ID: "g1", Server: "github", Tool: "merge", Subject: "*", Action: ActionAllow, GrantOf: "appr-1", CreatedBy: "t", CreatedAt: t0}
	if err := s.CreatePolicies(ctx, org, old, grant); err != nil {
		t.Fatal(err)
	}
	ids := func() map[string]bool {
		rules, _ := s.ListPolicies(ctx, org)
		out := map[string]bool{}
		for _, r := range rules {
			out[r.ID] = true
		}
		return out
	}
	// A refused import changes nothing: one rule names an unknown team.
	bad := []Policy{
		{ID: "n1", Server: "*", Tool: "*", Subject: "team:designers", Action: ActionAllow, CreatedBy: "t", CreatedAt: t0},
		{ID: "n2", Server: "*", Tool: "*", Subject: "team:finance", Action: ActionAllow, CreatedBy: "t", CreatedAt: t0},
	}
	if err := s.ImportPolicies(ctx, org, true, bad, "policy.default", "deny", adminEv("import-bad")); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("import with an unknown team: %v", err)
	}
	if got := ids(); !got["old"] || !got["g1"] || len(got) != 2 {
		t.Fatalf("a refused import changed the rules: %v", got)
	}
	if d, _ := s.Setting(ctx, org, "policy.default", "allow"); d != "allow" {
		t.Fatalf("a refused import changed the default: %s", d)
	}
	// Merge adds; replace drops every rule but grants.
	if err := s.ImportPolicies(ctx, org, false, bad[:1], "policy.default", "", adminEv("merge")); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !got["old"] || !got["n1"] || !got["g1"] {
		t.Fatalf("merge: %v", got)
	}
	repl := []Policy{{ID: "n3", Server: "*", Tool: "*", Subject: "*", Action: ActionDeny, CreatedBy: "t", CreatedAt: t0}}
	if err := s.ImportPolicies(ctx, org, true, repl, "policy.default", "ask", adminEv("replace")); err != nil {
		t.Fatal(err)
	}
	if got := ids(); len(got) != 2 || !got["n3"] || !got["g1"] {
		t.Fatalf("replace: %v", got)
	}
	if d, _ := s.Setting(ctx, org, "policy.default", "allow"); d != "ask" {
		t.Fatalf("default after import: %s", d)
	}
}

// Grants made before migration 11 are marked from their note and cover the
// rank of the ask they were made over.
func TestMigrationMarksOldGrants(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.w, mustSub(migrations, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][4]string{
		{"g", "Allowed for ada by carol from approval appr-9", "6", "2099-01-01T00:00:00Z"},
		{"plain", "a note", "3", ""},
	} {
		var exp any
		if r[3] != "" {
			exp = r[3]
		}
		if _, err := s.w.ExecContext(ctx, `INSERT INTO policies (org_id, id, server, tool, subject, action, priority, note, created_by, created_at, expires_at)
			VALUES (?, ?, 'github', 'merge', '*', 'allow', ?, ?, 't', ?, ?)`, org, r[0], r[2], r[1], ts(t0), exp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	rules, _ := s.ListPolicies(ctx, org)
	for _, r := range rules {
		switch {
		case r.ID == "g" && (r.GrantOf != "appr-9" || r.Priority != 5):
			t.Errorf("old grant %+v", r)
		case r.ID == "plain" && (r.GrantOf != "" || r.Priority != 3):
			t.Errorf("plain rule %+v", r)
		}
	}
}
