package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"privo.club/privo/internal/ids"
)

// addUser inserts an active member directly, as sign-in would.
func addUser(t *testing.T, s *Store, org OrgID, id, person string) {
	t.Helper()
	_, err := s.w.ExecContext(context.Background(), `INSERT INTO users (org_id, id, person, display_name, role, created_at) VALUES (?, ?, ?, ?, 'member', ?)`,
		org, id, person, "Name "+person, ts(t0))
	if err != nil {
		t.Fatal(err)
	}
}

func addServer(t *testing.T, s *Store, org OrgID, id, alias string, teams []string) {
	t.Helper()
	srv := Server{OrgID: org, ID: id, Alias: alias, Transport: "http", Config: ServerConfig{URL: "https://" + alias + ".example/mcp"}, Enabled: true, CreatedAt: t0, Teams: teams}
	if err := s.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatalf("CreateServer %s: %v", alias, err)
	}
}

func mkTeam(t *testing.T, s *Store, org OrgID, slug string) {
	t.Helper()
	ev := adminEv("team.create:" + slug)
	ev.ID = ids.New()
	if err := s.CreateTeam(context.Background(), org, Team{Slug: slug, Name: "Team " + slug, CreatedAt: t0}, ev); err != nil {
		t.Fatalf("CreateTeam %s: %v", slug, err)
	}
}

func reached(a Access) []string {
	var out []string
	for id := range a.Servers {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func TestTeamsCreateListRenameDelete(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	mkTeam(t, s, org, "designers")
	mkTeam(t, s, org, "developers")
	if err := s.CreateTeam(ctx, org, Team{Slug: "designers", Name: "Again", CreatedAt: t0}, adminEv("dup")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate slug: %v, want ErrConflict", err)
	}
	if err := s.UpdateTeam(ctx, org, "designers", "Design", "Figma people", adminEv("team.update:designers")); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTeam(ctx, org, "nope", "x", "", adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}
	got, err := s.TeamBySlug(ctx, org, "designers")
	if err != nil || got.Name != "Design" || got.Description != "Figma people" {
		t.Fatalf("TeamBySlug: %+v %v", got, err)
	}
	if err := s.DeleteTeam(ctx, org, "developers", adminEv("team.delete:developers")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTeam(ctx, org, "developers", adminEv("again")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	teams, err := s.ListTeams(ctx, org)
	if err != nil || len(teams) != 1 || teams[0].Slug != "designers" {
		t.Fatalf("ListTeams: %+v %v", teams, err)
	}
	acts := auditActions(t, s, org)
	for _, want := range []string{"team.create:designers", "team.update:designers", "team.delete:developers"} {
		if !slices.Contains(acts, want) {
			t.Fatalf("audit %v lacks %q", acts, want)
		}
	}
}

func TestTeamSlugRules(t *testing.T) {
	for _, ok := range []string{"designers", "pm", "dev-ops", "team2"} {
		if !ValidTeamSlug(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "a", "Designers", "2team", "-x", "with space", "everyone", "a234567890123456789012345678901234"} {
		if ValidTeamSlug(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestEveryoneIsReserved(t *testing.T) {
	s, org := signinOrg(t)
	if err := s.CreateTeam(context.Background(), org, Team{Slug: TeamEveryone, Name: "Everyone", CreatedAt: t0}, adminEv("x")); err == nil {
		t.Fatal("created a team named everyone")
	}
}

func TestTeamMembers(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u1", "ada")
	addUser(t, s, org, "u2", "bob")
	mkTeam(t, s, org, "designers")
	if err := s.AddTeamMember(ctx, org, "designers", "u1", t0, adminEv("team.member.add:designers:ada")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTeamMember(ctx, org, "designers", "u1", t0, adminEv("again")); err != nil {
		t.Fatalf("adding twice should be a no-op: %v", err)
	}
	if err := s.AddTeamMember(ctx, org, "nope", "u1", t0, adminEv("x")); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("unknown team: %v", err)
	}
	if err := s.AddTeamMember(ctx, org, "designers", "ghost", t0, adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	teams, _ := s.ListTeams(ctx, org)
	if len(teams[0].Members) != 1 || teams[0].Members[0].Person != "ada" {
		t.Fatalf("members: %+v", teams[0].Members)
	}
	if err := s.RemoveTeamMember(ctx, org, "designers", "u2", adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing a non-member: %v", err)
	}
	if err := s.RemoveTeamMember(ctx, org, "designers", "u1", adminEv("team.member.remove:designers:ada")); err != nil {
		t.Fatal(err)
	}
	teams, _ = s.ListTeams(ctx, org)
	if len(teams[0].Members) != 0 {
		t.Fatalf("members after removal: %+v", teams[0].Members)
	}
}

func TestServerTeamsDefaultToEveryone(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addServer(t, s, org, "s1", "notion", nil)
	st, err := s.ServerTeams(ctx, org)
	if err != nil || !slices.Equal(st["s1"], []string{TeamEveryone}) {
		t.Fatalf("ServerTeams: %v %v", st, err)
	}
}

func TestSetServerTeamsNeedsKnownTeams(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	if err := s.CreateServer(ctx, Server{OrgID: org, ID: "s0", Alias: "figma", Transport: "http", CreatedAt: t0, Teams: []string{"designers"}}, nil); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("create with unknown team: %v", err)
	}
	if _, err := s.ServerByID(ctx, org, "s0"); !errors.Is(err, ErrNotFound) {
		t.Fatal("the server was stored although its team is unknown")
	}
	mkTeam(t, s, org, "designers")
	addServer(t, s, org, "s1", "figma", []string{"designers"})
	if err := s.SetServerTeams(ctx, org, "s1", []string{"designers", "nope"}, adminEv("x")); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("set unknown team: %v", err)
	}
	if err := s.SetServerTeams(ctx, org, "missing", []string{TeamEveryone}, adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set on unknown server: %v", err)
	}
	if err := s.SetServerTeams(ctx, org, "s1", []string{TeamEveryone, "designers"}, adminEv("server.teams:figma")); err != nil {
		t.Fatal(err)
	}
	st, _ := s.ServerTeams(ctx, org)
	if !slices.Equal(st["s1"], []string{"designers", TeamEveryone}) {
		t.Fatalf("teams: %v", st["s1"])
	}
	if err := s.SetServerTeams(ctx, org, "s1", nil, adminEv("server.teams:figma:none")); err != nil {
		t.Fatal(err)
	}
	st, _ = s.ServerTeams(ctx, org)
	if len(st["s1"]) != 0 {
		t.Fatalf("teams after clearing: %v", st["s1"])
	}
}

func TestAccessFollowsTeams(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u1", "ada")
	addUser(t, s, org, "u2", "bob")
	mkTeam(t, s, org, "designers")
	addServer(t, s, org, "s-all", "notion", nil)
	addServer(t, s, org, "s-des", "figma", []string{"designers"})
	addServer(t, s, org, "s-none", "orphan", []string{})
	if err := s.AddTeamMember(ctx, org, "designers", "u1", t0, adminEv("x")); err != nil {
		t.Fatal(err)
	}
	ada, err := s.AccessFor(ctx, org, "u1")
	if err != nil || !slices.Equal(reached(ada), []string{"s-all", "s-des"}) || !slices.Equal(ada.Teams, []string{"designers", TeamEveryone}) {
		t.Fatalf("ada: %v %v %v", reached(ada), ada.Teams, err)
	}
	bob, _ := s.AccessFor(ctx, org, "u2")
	if !slices.Equal(reached(bob), []string{"s-all"}) || !slices.Equal(bob.Teams, []string{TeamEveryone}) {
		t.Fatalf("bob: %v %v", reached(bob), bob.Teams)
	}
	bot, _ := s.AccessFor(ctx, org, "")
	if !slices.Equal(reached(bot), []string{"s-all"}) || !bot.Reaches("s-all") || bot.Reaches("s-des") {
		t.Fatalf("no member: %v", reached(bot))
	}
	ghost, _ := s.AccessFor(ctx, org, "not-a-user")
	if len(ghost.Servers) != 0 || len(ghost.Teams) != 0 {
		t.Fatalf("unknown user id reaches %v %v", reached(ghost), ghost.Teams)
	}
}

func TestAccessOfRemovedMemberIsEmpty(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u1", "ada")
	addServer(t, s, org, "s-all", "notion", nil)
	if err := s.DisableUser(ctx, org, "u1", t0.Add(time.Hour), adminEv("member.remove:ada")); err != nil {
		t.Fatal(err)
	}
	a, err := s.AccessFor(ctx, org, "u1")
	if err != nil || len(a.Servers) != 0 || len(a.Teams) != 0 {
		t.Fatalf("removed member reaches %v %v %v", reached(a), a.Teams, err)
	}
}

func TestDeleteTeamDropsItsRulesAndAccess(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addUser(t, s, org, "u1", "ada")
	mkTeam(t, s, org, "designers")
	addServer(t, s, org, "s-des", "figma", []string{"designers"})
	_ = s.AddTeamMember(ctx, org, "designers", "u1", t0, adminEv("x"))
	for i, subj := range []string{"team:designers", "role:member"} {
		if err := s.CreatePolicies(ctx, org, Policy{ID: "p" + string(rune('0'+i)), Server: "*", Tool: "*", Subject: subj, Action: ActionDeny, CreatedBy: "t", CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteTeam(ctx, org, "designers", adminEv("team.delete:designers")); err != nil {
		t.Fatal(err)
	}
	rules, _ := s.ListPolicies(ctx, org)
	if len(rules) != 1 || rules[0].Subject != "role:member" {
		t.Fatalf("rules after delete: %+v", rules)
	}
	a, _ := s.AccessFor(ctx, org, "u1")
	if a.Reaches("s-des") || slices.Contains(a.Teams, "designers") {
		t.Fatalf("access after delete: %v %v", reached(a), a.Teams)
	}
	// Re-creating the slug does not bring old members or access back.
	mkTeam(t, s, org, "designers")
	if a, _ := s.AccessFor(ctx, org, "u1"); a.Reaches("s-des") || slices.Contains(a.Teams, "designers") {
		t.Fatalf("a re-created team inherited old access: %v %v", reached(a), a.Teams)
	}
}

func TestDeleteServerDropsItsTeams(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	mkTeam(t, s, org, "designers")
	addServer(t, s, org, "s1", "figma", []string{"designers", TeamEveryone})
	if err := s.DeleteServer(ctx, org, "s1"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.ServerTeams(ctx, org)
	if len(st) != 0 {
		t.Fatalf("server teams left: %v", st)
	}
	teams, _ := s.ListTeams(ctx, org)
	if len(teams[0].Servers) != 0 {
		t.Fatalf("team still lists %v", teams[0].Servers)
	}
}

func TestTeamsAreIsolatedPerOrg(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	a, b := twoOrgs(t, s)
	addUser(t, s, a, "u1", "ada")
	addUser(t, s, b, "u2", "ada")
	mkTeam(t, s, a, "designers")
	addServer(t, s, a, "sa", "figma", []string{"designers"})
	if err := s.AddTeamMember(ctx, b, "designers", "u2", t0, adminEv("x")); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("org b used org a's team: %v", err)
	}
	if err := s.AddTeamMember(ctx, a, "designers", "u2", t0, adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org a took org b's user: %v", err)
	}
	if err := s.SetServerTeams(ctx, b, "sa", []string{TeamEveryone}, adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b changed org a's server: %v", err)
	}
	if err := s.CreateServer(ctx, Server{OrgID: b, ID: "sb", Alias: "figma", Transport: "http", CreatedAt: t0, Teams: []string{"designers"}}, nil); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("org b's server used org a's team: %v", err)
	}
	if teams, _ := s.ListTeams(ctx, b); len(teams) != 0 {
		t.Fatalf("org b sees teams %+v", teams)
	}
	if acc, _ := s.AccessFor(ctx, b, "u2"); len(acc.Servers) != 0 {
		t.Fatalf("org b's user reaches %v", reached(acc))
	}
}

func TestMigrationGivesExistingServersEveryone(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.w, mustSub(migrations, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 5); err != nil {
		t.Fatalf("down to 5: %v", err)
	}
	// A server as it was stored before teams existed.
	if _, err := s.w.ExecContext(ctx, `INSERT INTO servers (org_id, id, alias, transport, config, enabled, created_at, updated_at) VALUES (?, 'old', 'old', 'http', '{}', 1, ?, ?)`,
		org, ts(t0), ts(t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if acc, _ := s.AccessFor(ctx, org, ""); !acc.Reaches("old") {
		t.Fatal("an existing server is not reachable by everyone after the migration")
	}
}
