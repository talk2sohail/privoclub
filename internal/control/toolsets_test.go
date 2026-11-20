package control

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"privo.club/privo/internal/store"
)

// ADR 0023: an admin narrows what a team sees of a server.
func TestTeamToolsAPI(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, admin := e.user(t, "adam", store.RoleAdmin)
	_, mia := e.user(t, "mia", store.RoleMember)
	e.seedServer(t, "github", nil)
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)
	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/servers", map[string]any{"alias": "github"}, http.StatusOK)

	path := "/api/v1/servers/github/teams/designers"
	res := e.call(t, admin, http.MethodPatch, path, map[string]any{"tools": []string{"search_code", "get_file", "search_code"}}, http.StatusOK)
	if got := res["tools"].([]any); len(got) != 2 || got[0] != "get_file" || got[1] != "search_code" {
		t.Fatalf("set %v", res)
	}
	list := e.call(t, admin, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)["servers"].([]any)
	gh := list[0].(map[string]any)
	tt, _ := gh["team_tools"].(map[string]any)
	if tt == nil || len(tt["designers"].([]any)) != 2 || tt["everyone"] != nil {
		t.Fatalf("admins see %v", gh["team_tools"])
	}
	// Members never see toolsets.
	if v := e.call(t, mia, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)["servers"].([]any); len(v) != 1 || v[0].(map[string]any)["team_tools"] != nil {
		t.Fatalf("a member sees %v", v)
	}
	e.call(t, mia, http.MethodPatch, path, map[string]any{"tools": nil}, http.StatusForbidden)
	// Validation.
	for _, bad := range []map[string]any{{}, {"tools": "all"}, {"tools": []string{""}}, {"tools": []string{"bad\nname"}}} {
		e.call(t, admin, http.MethodPatch, path, bad, http.StatusUnprocessableEntity)
	}
	e.call(t, admin, http.MethodPatch, "/api/v1/servers/github/teams/nobody", map[string]any{"tools": nil}, http.StatusNotFound)
	e.call(t, admin, http.MethodPatch, "/api/v1/servers/nope/teams/designers", map[string]any{"tools": nil}, http.StatusNotFound)
	// None, then back to all.
	if res := e.call(t, admin, http.MethodPatch, path, map[string]any{"tools": []string{}}, http.StatusOK); len(res["tools"].([]any)) != 0 {
		t.Fatalf("none: %v", res)
	}
	if res := e.call(t, admin, http.MethodPatch, path, map[string]any{"tools": nil}, http.StatusOK); res["tools"] != nil {
		t.Fatalf("all: %v", res)
	}
	e.aw.Close()
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 30)
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.Action)
	}
	for _, want := range []string{"server.team_tools:github:designers:2", "server.team_tools:github:designers:0", "server.team_tools:github:designers:all"} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit lacks %s: %q", want, actions)
		}
	}
}
