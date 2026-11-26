package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pressly/goose/v3"
)

// ADR 0026: per server and team, all tools or a chosen list.

func toolsetsOrg(t *testing.T) (*Store, OrgID) {
	t.Helper()
	s, org := signinOrg(t)
	addUser(t, s, org, "u-dee", "dee") // designer
	addUser(t, s, org, "u-dev", "dev") // developer
	addUser(t, s, org, "u-both", "both")
	mkTeam(t, s, org, "designers")
	mkTeam(t, s, org, "developers")
	ctx := context.Background()
	for _, m := range [][2]string{{"designers", "u-dee"}, {"developers", "u-dev"}, {"designers", "u-both"}, {"developers", "u-both"}} {
		if err := s.AddTeamMember(ctx, org, m[0], m[1], t0, adminEv("add-"+m[0]+m[1])); err != nil {
			t.Fatal(err)
		}
	}
	addServer(t, s, org, "gh", "github", []string{"designers", "developers"})
	addServer(t, s, org, "nt", "notion", []string{TeamEveryone})
	return s, org
}

func seen(t *testing.T, s *Store, org OrgID, user string) Access {
	t.Helper()
	a, err := s.AccessFor(context.Background(), org, user)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTeamToolsNarrowAccess(t *testing.T) {
	ctx := context.Background()
	s, org := toolsetsOrg(t)
	// Nothing narrowed: everyone sees every tool of what they reach.
	if a := seen(t, s, org, "u-dee"); !a.SeesTool("gh", "delete_repo") || !a.SeesTool("nt", "search") {
		t.Fatalf("before narrowing: %+v", a)
	}
	if err := s.SetTeamTools(ctx, org, "gh", "designers", []string{"search_code", "get_file"}, adminEv("narrow")); err != nil {
		t.Fatal(err)
	}
	dee, dev, both := seen(t, s, org, "u-dee"), seen(t, s, org, "u-dev"), seen(t, s, org, "u-both")
	if !dee.SeesTool("gh", "search_code") || dee.SeesTool("gh", "delete_repo") || !dee.Reaches("gh") {
		t.Fatalf("designer: %+v", dee)
	}
	if !dev.SeesTool("gh", "delete_repo") {
		t.Fatalf("developer: %+v", dev)
	}
	if !both.SeesTool("gh", "delete_repo") { // a team that sees all opens all
		t.Fatalf("in both teams: %+v", both)
	}
	// Both narrowed: the union.
	if err := s.SetTeamTools(ctx, org, "gh", "developers", []string{"delete_repo"}, adminEv("narrow2")); err != nil {
		t.Fatal(err)
	}
	both = seen(t, s, org, "u-both")
	if !both.SeesTool("gh", "delete_repo") || !both.SeesTool("gh", "search_code") || both.SeesTool("gh", "merge") {
		t.Fatalf("union: %+v", both)
	}
	// An empty list hides every tool, but the server is still reached.
	if err := s.SetTeamTools(ctx, org, "gh", "designers", []string{}, adminEv("none")); err != nil {
		t.Fatal(err)
	}
	if a := seen(t, s, org, "u-dee"); !a.Reaches("gh") || a.SeesTool("gh", "search_code") {
		t.Fatalf("empty list: %+v", a)
	}
	// everyone can be narrowed too; a token of no member follows it.
	if err := s.SetTeamTools(ctx, org, "nt", TeamEveryone, []string{"search"}, adminEv("ev")); err != nil {
		t.Fatal(err)
	}
	if a := seen(t, s, org, ""); !a.SeesTool("nt", "search") || a.SeesTool("nt", "create_page") {
		t.Fatalf("everyone narrowed: %+v", a)
	}
	// Back to all.
	if err := s.SetTeamTools(ctx, org, "gh", "designers", nil, adminEv("all")); err != nil {
		t.Fatal(err)
	}
	if a := seen(t, s, org, "u-dee"); !a.SeesTool("gh", "anything") {
		t.Fatalf("back to all: %+v", a)
	}
	tt, err := s.TeamTools(ctx, org)
	if err != nil || !slices.Equal(tt["gh"]["developers"], []string{"delete_repo"}) || tt["gh"]["designers"] != nil || !slices.Equal(tt["nt"][TeamEveryone], []string{"search"}) {
		t.Fatalf("TeamTools = %v, %v", tt, err)
	}
	// Only a team on the server can be narrowed.
	if err := s.SetTeamTools(ctx, org, "nt", "designers", []string{"x"}, adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a team not on the server: %v", err)
	}
}

// Keeping a team on a server keeps its list; removing the team from the
// server, deleting the team or the server drops it.
func TestTeamToolsFollowTeamChanges(t *testing.T) {
	ctx := context.Background()
	s, org := toolsetsOrg(t)
	if err := s.SetTeamTools(ctx, org, "gh", "designers", []string{"search_code"}, adminEv("n1")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerTeams(ctx, org, "gh", []string{"designers", "developers", TeamEveryone}, adminEv("teams1")); err != nil {
		t.Fatal(err)
	}
	if tt, _ := s.TeamTools(ctx, org); !slices.Equal(tt["gh"]["designers"], []string{"search_code"}) {
		t.Fatalf("saving the server's teams lost the list: %v", tt)
	}
	if err := s.SetServerTeams(ctx, org, "gh", []string{"developers"}, adminEv("teams2")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerTeams(ctx, org, "gh", []string{"developers", "designers"}, adminEv("teams3")); err != nil {
		t.Fatal(err)
	}
	if tt, _ := s.TeamTools(ctx, org); tt["gh"]["designers"] != nil {
		t.Fatalf("a team taken off and put back kept its old list: %v", tt)
	}
	if err := s.SetTeamTools(ctx, org, "gh", "designers", []string{"search_code"}, adminEv("n2")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTeam(ctx, org, "designers", adminEv("del")); err != nil {
		t.Fatal(err)
	}
	if tt, _ := s.TeamTools(ctx, org); len(tt["gh"]) != 0 {
		t.Fatalf("a deleted team's list stayed: %v", tt)
	}
	if err := s.SetTeamTools(ctx, org, "gh", "developers", []string{"merge"}, adminEv("n3")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServer(ctx, org, "gh"); err != nil {
		t.Fatal(err)
	}
	if tt, _ := s.TeamTools(ctx, org); len(tt) != 0 {
		t.Fatalf("a deleted server's lists stayed: %v", tt)
	}
}

func TestTeamToolsAreIsolatedPerOrg(t *testing.T) {
	ctx := context.Background()
	s, org := toolsetsOrg(t)
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if err := s.SetTeamTools(ctx, other.ID, "gh", "designers", []string{"x"}, adminEv("o")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org narrowed this org's server: %v", err)
	}
	if err := s.SetTeamTools(ctx, org, "gh", "designers", []string{"search_code"}, adminEv("n")); err != nil {
		t.Fatal(err)
	}
	if tt, _ := s.TeamTools(ctx, other.ID); len(tt) != 0 {
		t.Fatalf("another org sees %v", tt)
	}
}

func TestToolsetsPerKind(t *testing.T) {
	ctx := context.Background()
	s, org := toolsetsOrg(t)

	// Narrow prompts for designers, leaving tools/resources/templates open.
	if err := s.SetTeamItemTools(ctx, org, "gh", "designers", ItemKindPrompt, []string{"summarize"}, adminEv("p1")); err != nil {
		t.Fatal(err)
	}
	dee := seen(t, s, org, "u-dee")
	if !dee.SeesItem("gh", ItemKindPrompt, "summarize") || dee.SeesItem("gh", ItemKindPrompt, "deploy_note") {
		t.Fatalf("designers prompt access: %+v", dee)
	}
	// Other kinds remain open.
	if !dee.SeesTool("gh", "anything") || !dee.SeesItem("gh", ItemKindResource, "repo://readme") || !dee.SeesItem("gh", ItemKindResourceTemplate, "repo://file/{+path}") {
		t.Fatalf("other kinds narrowed unexpectedly: %+v", dee)
	}

	// User in both teams sees all prompts because developers is not narrowed on prompts.
	both := seen(t, s, org, "u-both")
	if !both.SeesItem("gh", ItemKindPrompt, "deploy_note") {
		t.Fatalf("both teams should see all prompts: %+v", both)
	}

	// Narrow developers on prompts too -> union for u-both.
	if err := s.SetTeamItemTools(ctx, org, "gh", "developers", ItemKindPrompt, []string{"deploy_note"}, adminEv("p2")); err != nil {
		t.Fatal(err)
	}
	both = seen(t, s, org, "u-both")
	if !both.SeesItem("gh", ItemKindPrompt, "summarize") || !both.SeesItem("gh", ItemKindPrompt, "deploy_note") || both.SeesItem("gh", ItemKindPrompt, "secret") {
		t.Fatalf("union prompts: %+v", both)
	}

	// Resources and templates narrowed atomically via SetTeamToolsets.
	if err := s.SetTeamToolsets(ctx, org, "gh", "designers", map[string][]string{
		ItemKindResource:         {"gh://readme"},
		ItemKindResourceTemplate: {},
	}, adminEv("rt1")); err != nil {
		t.Fatal(err)
	}
	dee = seen(t, s, org, "u-dee")
	if !dee.SeesItem("gh", ItemKindResource, "gh://readme") || dee.SeesItem("gh", ItemKindResource, "gh://secret") {
		t.Fatalf("designers resources: %+v", dee)
	}
	if dee.SeesItem("gh", ItemKindResourceTemplate, "gh://file/{+path}") {
		t.Fatalf("empty template list should hide all templates: %+v", dee)
	}

	// TeamToolsets returns narrowed lists per kind and nil for un-narrowed kinds.
	all, err := s.TeamToolsets(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	des := all["gh"]["designers"]
	if des.Tools != nil || !slices.Equal(des.Prompts, []string{"summarize"}) ||
		!slices.Equal(des.Resources, []string{"gh://readme"}) || des.Templates == nil || len(des.Templates) != 0 {
		t.Fatalf("TeamToolsets[gh][designers] = %+v", des)
	}

	// Reset templates back to all.
	if err := s.SetTeamItemTools(ctx, org, "gh", "designers", ItemKindResourceTemplate, nil, adminEv("rt2")); err != nil {
		t.Fatal(err)
	}
	dee = seen(t, s, org, "u-dee")
	if !dee.SeesItem("gh", ItemKindResourceTemplate, "gh://file/{+path}") {
		t.Fatalf("template reset to all failed: %+v", dee)
	}

	// Invalid kind is rejected.
	if err := s.SetTeamItemTools(ctx, org, "gh", "designers", "bogus", []string{"x"}, adminEv("bad")); err == nil {
		t.Fatal("invalid kind accepted")
	}

	// Removing a team from the server cascades all kind rows.
	if err := s.SetServerTeams(ctx, org, "gh", []string{"developers"}, adminEv("drop-des")); err != nil {
		t.Fatal(err)
	}
	all, err = s.TeamToolsets(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["gh"]["designers"]; ok {
		t.Fatalf("removed team kept toolsets: %+v", all["gh"])
	}
}

func TestExistingListsKeepMeaningAll(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "privo.db")
	db, err := sql.Open("sqlite", fileURI(dbPath, "_pragma=foreign_keys(ON)"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Migrate only up to 00017 (before 00018_toolsets_per_kind.sql).
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationFiles())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 17); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO orgs (id, slug, name, created_at) VALUES ('o1', 'acme', 'Acme', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (org_id, id, person, display_name, created_at) VALUES ('o1', 'u1', 'ada', 'Ada', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_roles (org_id, user_id, role, granted_at) VALUES ('o1', 'u1', 'member', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO teams (org_id, slug, name, created_at) VALUES ('o1', 'des', 'Designers', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO team_members (org_id, team, user_id, added_at) VALUES ('o1', 'des', 'u1', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO servers (org_id, id, alias, transport, config, enabled, created_at, updated_at) VALUES ('o1', 's1', 'gh', 'http', '{}', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO server_teams (org_id, server_id, team, narrowed) VALUES ('o1', 's1', 'des', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO server_team_tools (org_id, server_id, team, name) VALUES ('o1', 's1', 'des', 'search_code')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Re-open with Store.Open so 00018_toolsets_per_kind.sql runs on existing data.
	st, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	acc, err := st.AccessFor(ctx, "o1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !acc.SeesTool("s1", "search_code") || acc.SeesTool("s1", "delete_repo") {
		t.Fatalf("existing tool list lost after migration: %+v", acc)
	}
	// Existing lists leave prompts, resources and templates un-narrowed (all visible).
	if !acc.SeesItem("s1", ItemKindPrompt, "greet") ||
		!acc.SeesItem("s1", ItemKindResource, "gh://readme") ||
		!acc.SeesItem("s1", ItemKindResourceTemplate, "gh://repos/{owner}/{repo}") {
		t.Fatalf("existing tool list narrowed non-tool kinds: %+v", acc)
	}
	ts, err := st.TeamToolsets(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	got := ts["s1"]["des"]
	if !slices.Equal(got.Tools, []string{"search_code"}) || got.Prompts != nil || got.Resources != nil || got.Templates != nil {
		t.Fatalf("migrated TeamToolsets = %+v", got)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify 00018 down migration + re-up migration both succeed cleanly.
	db2, err := sql.Open("sqlite", fileURI(dbPath, "_pragma=foreign_keys(ON)"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	p2, err := goose.NewProvider(goose.DialectSQLite3, db2, migrationFiles())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p2.Down(ctx); err != nil {
		t.Fatalf("goose down 00018: %v", err)
	}
	if _, err := p2.Up(ctx); err != nil {
		t.Fatalf("goose re-up 00018: %v", err)
	}
}

func TestToolsetsPerKindOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s, org := toolsetsOrg(t)
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if err := s.SetTeamItemTools(ctx, other.ID, "gh", "designers", ItemKindPrompt, []string{"x"}, adminEv("o")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org narrowed this org's prompts: %v", err)
	}
	if err := s.SetTeamToolsets(ctx, org, "gh", "designers", map[string][]string{
		ItemKindPrompt:           {"greet"},
		ItemKindResource:         {"gh://readme"},
		ItemKindResourceTemplate: {"gh://repos/{owner}/{repo}"},
	}, adminEv("n")); err != nil {
		t.Fatal(err)
	}
	if tt, _ := s.TeamToolsets(ctx, other.ID); len(tt) != 0 {
		t.Fatalf("another org sees %v", tt)
	}
	if acc, err := s.AccessFor(ctx, other.ID, "u-dee"); err != nil || acc.Reaches("gh") || acc.SeesItem("gh", ItemKindPrompt, "greet") {
		t.Fatalf("cross-org access leaked: %+v, %v", acc, err)
	}
}
