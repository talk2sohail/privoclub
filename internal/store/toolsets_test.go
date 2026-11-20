package store

import (
	"context"
	"errors"
	"slices"
	"testing"
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
