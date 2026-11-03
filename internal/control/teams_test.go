package control

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/store"
)

// ADR 0020: teams, server access, the catalog and each person's own tools.

func (e *sessionEnv) seedServer(t *testing.T, alias string, teams []string) store.Server {
	t.Helper()
	srv := store.Server{OrgID: e.org, ID: "srv-" + alias, Alias: alias, Transport: "http", Config: store.ServerConfig{URL: "https://" + alias + ".example/mcp"},
		Enabled: true, CreatedAt: time.Now(), Teams: teams}
	if err := e.st.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
	return srv
}

// aliases are the server aliases in an answer's "servers".
func aliases(out map[string]any) []string {
	var got []string
	raw, _ := out["servers"].([]any)
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			got = append(got, fmt.Sprint(m["alias"]))
		}
	}
	return got
}

func adminActions(t *testing.T, e *sessionEnv) string {
	t.Helper()
	evs, err := e.st.RecentAdminEvents(context.Background(), e.org, 200)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		fmt.Fprintf(&b, "%s by %s\n", ev.Action, ev.ActorID)
	}
	return b.String()
}

func TestTeamsAPIRoleRules(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "adam", store.RoleAdmin)
	_, member := e.user(t, "mia", store.RoleMember)
	_, auditor := e.user(t, "otto", store.RoleAuditor)

	created := e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"name": "Product managers", "profile": "product"}, http.StatusCreated)
	if team, _ := created["team"].(map[string]any); team["slug"] != "product-managers" {
		t.Fatalf("slug from the name: %v", created)
	}
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers", "profile": "designers"}, http.StatusCreated)
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Again"}, http.StatusConflict)
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"name": "Everyone"}, http.StatusConflict)
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"name": "x\u202ey"}, http.StatusUnprocessableEntity)
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"name": "Ops", "profile": "everyone"}, http.StatusUnprocessableEntity)
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"name": "Ops", "slug": "Bad Slug"}, http.StatusUnprocessableEntity)

	for _, p := range []auth.Principal{member, auditor} {
		e.call(t, p, http.MethodGet, "/api/v1/teams", nil, http.StatusForbidden)
		e.call(t, p, http.MethodPost, "/api/v1/teams", map[string]any{"name": "Mine"}, http.StatusForbidden)
		e.call(t, p, http.MethodPost, "/api/v1/teams/designers/members", map[string]any{"person": p.Person}, http.StatusForbidden)
		e.call(t, p, http.MethodGet, "/api/v1/catalog", nil, http.StatusForbidden)
	}

	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/members", map[string]any{"person": "mia"}, http.StatusOK)
	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/members", map[string]any{"person": "ghost"}, http.StatusNotFound)
	e.call(t, admin, http.MethodPost, "/api/v1/teams/nope/members", map[string]any{"person": "mia"}, http.StatusNotFound)
	e.call(t, admin, http.MethodPost, "/api/v1/teams/everyone/members", map[string]any{"person": "mia"}, http.StatusConflict)
	e.call(t, admin, http.MethodPatch, "/api/v1/teams/everyone", map[string]any{"name": "All"}, http.StatusConflict)
	e.call(t, admin, http.MethodDelete, "/api/v1/teams/everyone", nil, http.StatusConflict)
	e.call(t, admin, http.MethodPatch, "/api/v1/teams/designers", map[string]any{"name": "Design"}, http.StatusOK)
	e.call(t, admin, http.MethodPatch, "/api/v1/teams/designers", map[string]any{}, http.StatusUnprocessableEntity)

	list := e.call(t, admin, http.MethodGet, "/api/v1/teams", nil, http.StatusOK)
	teams, _ := list["teams"].([]any)
	if len(teams) != 3 {
		t.Fatalf("teams: %v", list)
	}
	first, _ := teams[0].(map[string]any)
	if first["slug"] != "everyone" || first["built_in"] != true {
		t.Fatalf("everyone first: %v", first)
	}
	found := false
	for _, v := range teams {
		m, _ := v.(map[string]any)
		if m["slug"] == "designers" {
			found = m["name"] == "Design" && strings.Contains(fmt.Sprint(m["members"]), "mia")
		}
	}
	if !found {
		t.Fatalf("designers with mia: %v", teams)
	}

	e.call(t, admin, http.MethodDelete, "/api/v1/teams/designers/members/mia", nil, http.StatusOK)
	e.call(t, admin, http.MethodDelete, "/api/v1/teams/designers/members/mia", nil, http.StatusNotFound)
	e.call(t, admin, http.MethodDelete, "/api/v1/teams/designers", nil, http.StatusOK)
	e.call(t, admin, http.MethodDelete, "/api/v1/teams/designers", nil, http.StatusNotFound)
}

func TestTeamsAPIAudited(t *testing.T) {
	e := newSessionEnv(t)
	u, owner := e.user(t, "boss", store.RoleOwner)
	e.user(t, "mia", store.RoleMember)
	e.seedServer(t, "figma", nil)
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)
	e.call(t, owner, http.MethodPost, "/api/v1/teams/designers/members", map[string]any{"person": "mia"}, http.StatusOK)
	e.call(t, owner, http.MethodPost, "/api/v1/teams/designers/servers", map[string]any{"alias": "figma"}, http.StatusOK)
	e.call(t, owner, http.MethodDelete, "/api/v1/teams/everyone/servers/figma", nil, http.StatusOK)
	e.call(t, owner, http.MethodPatch, "/api/v1/servers/figma", map[string]any{"teams": []string{"designers", "everyone"}}, http.StatusOK)
	e.call(t, owner, http.MethodDelete, "/api/v1/teams/designers/members/mia", nil, http.StatusOK)
	e.call(t, owner, http.MethodDelete, "/api/v1/teams/designers", nil, http.StatusOK)
	got := adminActions(t, e)
	for _, want := range []string{"team.create:designers", "team.member.add:designers:mia", "server.team.add:designers", "server.team.remove:everyone",
		"server.teams=designers,everyone", "team.member.remove:designers:mia", "team.delete:designers"} {
		if !strings.Contains(got, want+" by user:"+u.ID) {
			t.Errorf("audit lacks %q by the owner:\n%s", want, got)
		}
	}
}

func TestPatchServerTeams(t *testing.T) {
	e := newSessionEnv(t)
	_, admin := e.user(t, "adam", store.RoleAdmin)
	_, mia := e.user(t, "mia", store.RoleMember)
	e.seedServer(t, "figma", nil)
	e.seedServer(t, "notion", nil)
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)

	out := e.call(t, admin, http.MethodPatch, "/api/v1/servers/figma", map[string]any{"teams": []string{"designers"}}, http.StatusOK)
	srv, _ := out["server"].(map[string]any)
	if fmt.Sprint(srv["teams"]) != "[designers]" {
		t.Fatalf("teams after patch: %v", srv)
	}
	e.call(t, admin, http.MethodPatch, "/api/v1/servers/figma", map[string]any{"teams": []string{"nope"}}, http.StatusUnprocessableEntity)
	e.call(t, admin, http.MethodPatch, "/api/v1/servers/figma", map[string]any{"teams": []string{"Not A Slug"}}, http.StatusUnprocessableEntity)
	e.call(t, mia, http.MethodPatch, "/api/v1/servers/figma", map[string]any{"teams": []string{"everyone"}}, http.StatusForbidden)

	// A member sees only the servers she reaches, and no team names.
	if got := aliases(e.call(t, mia, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)); !slices.Equal(got, []string{"notion"}) {
		t.Fatalf("mia lists %v, want [notion]", got)
	}
	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/members", map[string]any{"person": "mia"}, http.StatusOK)
	list := e.call(t, mia, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)
	if got := aliases(list); !slices.Equal(got, []string{"figma", "notion"}) {
		t.Fatalf("mia in designers lists %v", got)
	}
	if strings.Contains(fmt.Sprint(list), "designers") {
		t.Fatalf("a member sees team names: %v", list)
	}
	// Admins see every server with its teams, even one nobody reaches.
	e.call(t, admin, http.MethodPatch, "/api/v1/servers/notion", map[string]any{"teams": []string{}}, http.StatusOK)
	all := e.call(t, admin, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)
	if got := aliases(all); !slices.Equal(got, []string{"figma", "notion"}) {
		t.Fatalf("admin lists %v", got)
	}
	if got := aliases(e.call(t, mia, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)); !slices.Equal(got, []string{"figma"}) {
		t.Fatalf("a server without teams is listed to mia: %v", got)
	}
}

func plainUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "plain", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo."}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts
}

func TestCreateServerWithTeams(t *testing.T) {
	e := newSessionEnv(t)
	allow, _ := egress.ParseAllowList("127.0.0.0/8,::1/128")
	e.api.Egress = egress.Policy{Allow: allow}
	_, admin := e.user(t, "adam", store.RoleAdmin)
	up := plainUpstream(t)
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)

	out := e.call(t, admin, http.MethodPost, "/api/v1/servers", map[string]any{"alias": "figma", "transport": "http", "url": up.URL + "/mcp", "teams": []string{"designers", "designers"}}, http.StatusCreated)
	if srv, _ := out["server"].(map[string]any); fmt.Sprint(srv["teams"]) != "[designers]" {
		t.Fatalf("created with teams: %v", out)
	}
	out = e.call(t, admin, http.MethodPost, "/api/v1/servers", map[string]any{"alias": "plain", "transport": "http", "url": up.URL + "/mcp"}, http.StatusCreated)
	if srv, _ := out["server"].(map[string]any); fmt.Sprint(srv["teams"]) != "[everyone]" {
		t.Fatalf("created without teams: %v", out)
	}
	e.call(t, admin, http.MethodPost, "/api/v1/servers", map[string]any{"alias": "ghost", "transport": "http", "url": up.URL + "/mcp", "teams": []string{"nope"}}, http.StatusUnprocessableEntity)
	if _, err := e.st.ServerByAlias(context.Background(), e.org, "ghost"); err == nil {
		t.Fatal("a server with an unknown team was stored")
	}
	if got := adminActions(t, e); !strings.Contains(got, "server.teams=designers") || !strings.Contains(got, "server.teams=everyone") {
		t.Fatalf("audit:\n%s", got)
	}
}

func TestCatalogAPI(t *testing.T) {
	e := newSessionEnv(t)
	_, admin := e.user(t, "adam", store.RoleAdmin)
	srv := store.Server{OrgID: e.org, ID: "srv-n", Alias: "my-notion", Transport: "http", Config: store.ServerConfig{URL: "https://mcp.notion.com/mcp"}, Enabled: true, CreatedAt: time.Now()}
	if err := e.st.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
	out := e.call(t, admin, http.MethodGet, "/api/v1/catalog", nil, http.StatusOK)
	entries, _ := out["entries"].([]any)
	profiles, _ := out["profiles"].([]any)
	if len(entries) < 10 || len(profiles) < 4 {
		t.Fatalf("catalog: %d entries, %d profiles", len(entries), len(profiles))
	}
	var notion, github map[string]any
	for _, v := range entries {
		m, _ := v.(map[string]any)
		switch m["id"] {
		case "notion":
			notion = m
		case "github":
			github = m
		}
	}
	if notion["installed"] != "my-notion" {
		t.Fatalf("notion should show as installed: %v", notion)
	}
	if github["registration"] != "own_app" || github["installed"] != nil {
		t.Fatalf("github: %v", github)
	}
	if out["oauth_available"] != false {
		t.Fatalf("oauth_available without the broker: %v", out["oauth_available"])
	}
}

func TestMyTools(t *testing.T) {
	e := newSessionEnv(t)
	_, admin := e.user(t, "adam", store.RoleAdmin)
	_, mia := e.user(t, "mia", store.RoleMember)
	_, otto := e.user(t, "otto", store.RoleAuditor)
	e.seedServer(t, "figma", []string{})
	e.seedServer(t, "notion", nil)
	n := store.Server{OrgID: e.org, ID: "srv-lin", Alias: "linear", Transport: "http", Enabled: true, CreatedAt: time.Now(),
		Config: store.ServerConfig{URL: "https://mcp.linear.app/mcp", Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{Issuer: "https://mcp.linear.app"}}}
	if err := e.st.CreateServer(context.Background(), n, nil); err != nil {
		t.Fatal(err)
	}
	off := e.seedServer(t, "off", nil)
	if err := e.st.SetServerEnabled(context.Background(), e.org, off.ID, false); err != nil {
		t.Fatal(err)
	}
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)
	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/servers", map[string]any{"alias": "figma"}, http.StatusOK)

	out := e.call(t, mia, http.MethodGet, "/api/v1/me/tools", nil, http.StatusOK)
	if got := aliases(out); !slices.Equal(got, []string{"linear", "notion"}) {
		t.Fatalf("mia's servers %v, want [linear notion] (figma is the designers', off is turned off)", got)
	}
	if fmt.Sprint(out["teams"]) != "[map[name:Everyone slug:everyone]]" {
		t.Fatalf("mia's teams: %v", out["teams"])
	}
	raw, _ := out["servers"].([]any)
	lin, _ := raw[0].(map[string]any)
	if lin["name"] != "Linear" || lin["auth"] != "oauth" || lin["connected"] != nil {
		t.Fatalf("linear for mia: %v", lin)
	}

	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/members", map[string]any{"person": "mia"}, http.StatusOK)
	out = e.call(t, mia, http.MethodGet, "/api/v1/me/tools", nil, http.StatusOK)
	if got := aliases(out); !slices.Equal(got, []string{"figma", "linear", "notion"}) {
		t.Fatalf("mia in designers: %v", got)
	}
	if !strings.Contains(fmt.Sprint(out["teams"]), "Designers") {
		t.Fatalf("teams: %v", out["teams"])
	}
	// Auditors do not use the gateway.
	e.call(t, otto, http.MethodGet, "/api/v1/me/tools", nil, http.StatusForbidden)
}

// F26 review R-02, R-04, R-06, R-07.
func TestTeamsReviewFixes(t *testing.T) {
	e := newSessionEnv(t)
	_, admin := e.user(t, "adam", store.RoleAdmin)
	e.seedServer(t, "figma", []string{})
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)

	// R-02: a rule for a team that does not exist is refused.
	e.call(t, admin, http.MethodPost, "/api/v1/policies", map[string]any{"server": "*", "tool": "*", "subject": "team:finance", "action": "allow"}, http.StatusUnprocessableEntity)
	e.call(t, admin, http.MethodPost, "/api/v1/policies", map[string]any{"server": "*", "tool": "*", "subject": "team:designers", "action": "ask"}, http.StatusCreated)

	// R-04: admins see an empty list for a server nobody reaches.
	list := e.call(t, admin, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)
	raw, _ := list["servers"].([]any)
	srv, _ := raw[0].(map[string]any)
	if teams, ok := srv["teams"].([]any); !ok || len(teams) != 0 {
		t.Fatalf("a server without teams shows %v to admins, want []", srv["teams"])
	}

	// R-06: unknown teams are 404 or 422, never 500, and are caught before
	// any outbound work.
	e.call(t, admin, http.MethodPost, "/api/v1/teams/nope/servers", map[string]any{"alias": "figma"}, http.StatusNotFound)
	e.call(t, admin, http.MethodPost, "/api/v1/teams/designers/servers", map[string]any{"alias": "nope"}, http.StatusNotFound)
	out := e.call(t, admin, http.MethodPost, "/api/v1/servers", map[string]any{"alias": "ghost", "transport": "http", "url": "https://203.0.113.9/mcp", "teams": []string{"nope"}}, http.StatusUnprocessableEntity)
	if !strings.Contains(fmt.Sprint(out["error"]), `no team "nope"`) {
		t.Fatalf("unknown team on create: %v", out)
	}

	// R-07: names a person cannot read, or that read like another team's.
	for _, bad := range []string{"x y", "ㅤㅤ", "", "á́́", "designers", "EVERYONE", "   "} {
		e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"name": bad, "slug": "other"}, map[bool]int{true: http.StatusConflict, false: http.StatusUnprocessableEntity}[bad == "designers" || bad == "EVERYONE"])
	}
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"name": "Ünïcode Désign 2", "slug": "other"}, http.StatusCreated)
	e.call(t, admin, http.MethodPatch, "/api/v1/teams/other", map[string]any{"name": "DESIGNERS"}, http.StatusConflict)
	e.call(t, admin, http.MethodPatch, "/api/v1/teams/designers", map[string]any{"name": "designers"}, http.StatusOK) // its own name, other case
	long := strings.Repeat("é", 64)
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"name": long, "slug": "long"}, http.StatusCreated) // 64 characters, 128 bytes
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"name": long + "é", "slug": "longer"}, http.StatusUnprocessableEntity)
}
