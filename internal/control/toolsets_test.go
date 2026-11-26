package control

import (
	"context"
	"net/http"
	"slices"
	"strings"
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

func TestTeamToolsetsPerKindAPI(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, admin := e.user(t, "adam", store.RoleAdmin)
	_, mia := e.user(t, "mia", store.RoleMember)
	e.seedServer(t, "github", nil)
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)
	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/servers", map[string]any{"alias": "github"}, http.StatusOK)

	path := "/api/v1/servers/github/teams/designers"
	longRes := "https://api.github.com/" + strings.Repeat("segment/", 20) + "readme"
	longTpl := "https://api.github.com/" + strings.Repeat("segment/", 20) + "repos/{owner}/{repo}"

	// Multi-kind PATCH sets prompts, resources and resource_templates atomically, deduplicates & sorts.
	res := e.call(t, admin, http.MethodPatch, path, map[string]any{
		"prompts":            []string{"summarize", "greet", "greet"},
		"resources":          []string{longRes},
		"resource_templates": []string{longTpl},
	}, http.StatusOK)
	if got := res["prompts"].([]any); len(got) != 2 || got[0] != "greet" || got[1] != "summarize" {
		t.Fatalf("prompts = %v", res)
	}
	if got := res["resources"].([]any); len(got) != 1 || got[0] != longRes {
		t.Fatalf("resources = %v", res)
	}
	if got := res["resource_templates"].([]any); len(got) != 1 || got[0] != longTpl {
		t.Fatalf("resource_templates = %v", res)
	}

	// Managers see team_prompts, team_resources, team_templates; members see none of them.
	list := e.call(t, admin, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)["servers"].([]any)
	gh := list[0].(map[string]any)
	if tp, _ := gh["team_prompts"].(map[string]any); tp == nil || len(tp["designers"].([]any)) != 2 {
		t.Fatalf("admins see team_prompts = %v", gh["team_prompts"])
	}
	if tr, _ := gh["team_resources"].(map[string]any); tr == nil || len(tr["designers"].([]any)) != 1 {
		t.Fatalf("admins see team_resources = %v", gh["team_resources"])
	}
	if tt, _ := gh["team_templates"].(map[string]any); tt == nil || len(tt["designers"].([]any)) != 1 {
		t.Fatalf("admins see team_templates = %v", gh["team_templates"])
	}
	if gh["team_tools"] != nil {
		t.Fatalf("tools were not narrowed, got team_tools = %v", gh["team_tools"])
	}

	mView := e.call(t, mia, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)["servers"].([]any)[0].(map[string]any)
	for _, k := range []string{"team_tools", "team_prompts", "team_resources", "team_templates"} {
		if mView[k] != nil {
			t.Fatalf("member sees %s = %v", k, mView[k])
		}
	}
	e.call(t, mia, http.MethodPatch, path, map[string]any{"prompts": nil}, http.StatusForbidden)

	// Validation: bad shapes, overlong prompt name (>128), control chars.
	for _, bad := range []map[string]any{
		{},
		{"prompts": "all"},
		{"resources": []string{""}},
		{"resource_templates": []string{"bad\nuri"}},
		{"prompts": []string{strings.Repeat("a", 129)}},
	} {
		e.call(t, admin, http.MethodPatch, path, bad, http.StatusUnprocessableEntity)
	}

	// None ([]) and reset back to all (null).
	res = e.call(t, admin, http.MethodPatch, path, map[string]any{"prompts": []string{}, "resources": nil, "resource_templates": nil}, http.StatusOK)
	if len(res["prompts"].([]any)) != 0 || res["resources"] != nil || res["resource_templates"] != nil {
		t.Fatalf("none/reset response = %v", res)
	}

	list = e.call(t, admin, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)["servers"].([]any)
	gh = list[0].(map[string]any)
	if tp, _ := gh["team_prompts"].(map[string]any); tp == nil || len(tp["designers"].([]any)) != 0 {
		t.Fatalf("team_prompts after none = %v", gh["team_prompts"])
	}
	if gh["team_resources"] != nil || gh["team_templates"] != nil {
		t.Fatalf("reset fields still present: res=%v tpl=%v", gh["team_resources"], gh["team_templates"])
	}

	e.aw.Close()
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 30)
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.Action)
	}
	for _, want := range []string{
		"server.team_prompts:github:designers:2",
		"server.team_resources:github:designers:1",
		"server.team_templates:github:designers:1",
		"server.team_prompts:github:designers:0",
		"server.team_resources:github:designers:all",
		"server.team_templates:github:designers:all",
	} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit lacks %s: %q", want, actions)
		}
	}
}
