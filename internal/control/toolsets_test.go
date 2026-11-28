package control

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"privo.club/privo/internal/store"
)

// ADR 0026: an admin narrows what a team sees of a server.
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

// ADR 0026 amendment: a signed-in member can toggle tools of a server they
// reach off or back on, never probing servers outside their teams.
func TestPatchMyServerToolsValidationAndTeamIsolation(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, admin := e.user(t, "adam", store.RoleAdmin)
	_, mia := e.user(t, "mia", store.RoleMember)
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)
	e.seedServer(t, "github", nil)
	e.seedServer(t, "secret", []string{"designers"})

	path := "/api/v1/me/servers/github/tools"
	// Deduplicates and sorts disabled tool names.
	res := e.call(t, mia, http.MethodPatch, path, map[string]any{"disabled": []string{"write", "read", "write"}}, http.StatusOK)
	if got := res["disabled"].([]any); len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Fatalf("patch = %v", res)
	}
	// Empty list re-enables all tools.
	if res := e.call(t, mia, http.MethodPatch, path, map[string]any{"disabled": []string{}}, http.StatusOK); len(res["disabled"].([]any)) != 0 {
		t.Fatalf("re-enable all = %v", res)
	}
	// Server outside member's teams answers 404, identical to an unknown alias.
	e.call(t, mia, http.MethodPatch, "/api/v1/me/servers/secret/tools", map[string]any{"disabled": []string{"x"}}, http.StatusNotFound)
	e.call(t, mia, http.MethodPatch, "/api/v1/me/servers/nope/tools", map[string]any{"disabled": []string{"x"}}, http.StatusNotFound)
	// Validation errors.
	for _, bad := range []map[string]any{{}, {"disabled": nil}, {"disabled": []string{""}}, {"disabled": []string{"bad\nname"}}} {
		e.call(t, mia, http.MethodPatch, path, bad, http.StatusUnprocessableEntity)
	}
	e.aw.Close()
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 30)
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.Action)
	}
	for _, want := range []string{"me.tools:github:disabled=2", "me.tools:github:all"} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit lacks %s: %q", want, actions)
		}
	}
}
