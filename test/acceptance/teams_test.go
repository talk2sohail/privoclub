//go:build acceptance

package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// ADR 0020 through the real binary, as the admin app calls it: teams decide
// which servers each person's AI client sees and can call.

// ownerOf claims the setup link as "boss" and returns the signed-in owner.
func ownerOf(t *testing.T, g *gateway, gh *acceptanceGitHub) *person {
	t.Helper()
	out, err := g.command("admin", "setup-link", "--person", "boss").Output()
	if err != nil {
		t.Fatal(err)
	}
	boss := newPerson(t, g)
	_, body := boss.do(http.MethodGet, strings.TrimSpace(string(out)), nil, nil)
	gh.as = fakeGitHubUser{ID: 1, Login: "boss", Name: "Boss"}
	start, _ := boss.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"setup_token": {setupTokenField.FindStringSubmatch(body)[1]}}, nil)
	if cb, _ := boss.signInThroughGitHub(start); cb.Header.Get("Location") != "/admin/servers" {
		t.Fatalf("owner lands on %q", cb.Header.Get("Location"))
	}
	return boss
}

// join invites person as a member; they sign in, land on Start and make a
// token for their AI client.
func join(t *testing.T, g *gateway, gh *acceptanceGitHub, boss *person, name string, githubID int) (*person, string) {
	t.Helper()
	code, inv := boss.app(http.MethodPost, "/v1/invites", map[string]any{"person": name, "role": "member"}, nil)
	invite, _ := inv["invite"].(map[string]any)
	link, _ := invite["url"].(string)
	if code != http.StatusCreated {
		t.Fatalf("invite %s: %d %v", name, code, inv)
	}
	p := newPerson(t, g)
	_, body := p.do(http.MethodGet, link, nil, nil)
	gh.as = fakeGitHubUser{ID: githubID, Login: name + "-gh", Name: strings.ToUpper(name[:1]) + name[1:]}
	start, _ := p.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"invite_token": {inviteTokenField.FindStringSubmatch(body)[1]}}, nil)
	if cb, _ := p.signInThroughGitHub(start); cb.Header.Get("Location") != "/admin/start" {
		t.Fatalf("%s lands on %q, want /admin/start", name, cb.Header.Get("Location"))
	}
	// Token names are unique in the org, not per person (see docs/todo.md F26).
	code, tok := p.app(http.MethodPost, "/v1/me/tokens", map[string]any{"name": "claude-code-" + name}, nil)
	token, _ := tok["token"].(string)
	if code != http.StatusCreated || token == "" {
		t.Fatalf("%s's token: %d %v", name, code, tok)
	}
	return p, token
}

// addReviewed adds a server and accepts all of its tools.
func addReviewed(t *testing.T, g *gateway, boss *person, body map[string]any) {
	t.Helper()
	alias := fmt.Sprint(body["alias"])
	if code, out := boss.app(http.MethodPost, "/v1/servers", body, nil); code != http.StatusCreated {
		t.Fatalf("add %s: %d %v", alias, code, out)
	}
	var pins []map[string]any
	g.eventually(alias+" lists its tools", 10*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/servers/"+alias+"/pins", nil, nil)
		pins = list(out, "pins")
		return len(pins) > 0
	})
	accept := []map[string]any{}
	for _, p := range pins {
		accept = append(accept, map[string]any{"kind": p["kind"], "hash": p["hash"], "name": p["name"]})
	}
	if code, out := boss.app(http.MethodPost, "/v1/servers/"+alias+"/pins:accept", map[string]any{"pins": accept}, nil); code != http.StatusOK {
		t.Fatalf("accept %s: %d %v", alias, code, out)
	}
}

func TestTeamsLimitWhatEachPersonSees(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)

	if code, out := boss.app(http.MethodPost, "/v1/teams", map[string]any{"name": "Designers", "profile": "designers"}, nil); code != http.StatusCreated {
		t.Fatalf("create team: %d %v", code, out)
	}
	addReviewed(t, g, boss, map[string]any{"alias": "shared", "transport": "http", "url": plainMCPServer(t).URL + "/mcp"})
	addReviewed(t, g, boss, map[string]any{"alias": "design", "transport": "http", "url": plainMCPServer(t).URL + "/mcp", "teams": []string{"designers"}})

	ada, adaToken := join(t, g, gh, boss, "ada", 2)
	_, bobToken := join(t, g, gh, boss, "bob", 3)
	adaClient, bobClient := g.connect(adaToken), g.connect(bobToken)

	// 1. Before Ada is in the team, both see only the shared server, and a
	// call on the other is answered like a name that does not exist.
	if got := toolList(t, adaClient); !slices.Equal(got, []string{"shared__echo"}) {
		t.Fatalf("ada sees %v, want [shared__echo]", got)
	}
	unknown, _ := call(t, adaClient, "design__nope", map[string]any{"text": "x"})
	hidden, isErr := call(t, adaClient, "design__echo", map[string]any{"text": "x"})
	if !isErr || !strings.Contains(hidden, "is not available") || strings.ReplaceAll(hidden, "design__echo", "X") != strings.ReplaceAll(unknown, "design__nope", "X") {
		t.Fatalf("a call on a server outside her teams: %q, an unknown name: %q", hidden, unknown)
	}
	if text, isErr := call(t, adaClient, "shared__echo", map[string]any{"text": "hi"}); isErr || text != "hi" {
		t.Fatalf("shared call: %q %v", text, isErr)
	}
	_, mine := ada.app(http.MethodGet, "/v1/me/tools", nil, nil)
	if fmt.Sprint(aliasesOf(list(mine, "servers"))) != "[shared]" {
		t.Fatalf("ada's start page: %v", mine)
	}
	_, srvs := ada.app(http.MethodGet, "/v1/servers", nil, nil)
	if fmt.Sprint(aliasesOf(list(srvs, "servers"))) != "[shared]" {
		t.Fatalf("ada's server list: %v", srvs)
	}

	// 2. Joining the team opens the server on the next call; lists follow
	// once the client's cached list expires (5 seconds, ADR 0003).
	if code, out := boss.app(http.MethodPost, "/v1/teams/designers/members", map[string]any{"person": "ada"}, nil); code != http.StatusOK {
		t.Fatalf("add ada: %d %v", code, out)
	}
	if text, isErr := call(t, adaClient, "design__echo", map[string]any{"text": "made it"}); isErr || text != "made it" {
		t.Fatalf("design call: %q %v", text, isErr)
	}
	g.eventually("ada's list shows the designers' server", 8*time.Second, func() bool {
		return slices.Equal(toolList(t, adaClient), []string{"design__echo", "shared__echo"})
	})
	if got := toolList(t, bobClient); !slices.Equal(got, []string{"shared__echo"}) {
		t.Fatalf("bob sees %v", got)
	}

	// 3. A rule for the team applies to its members only.
	if code, out := boss.app(http.MethodPost, "/v1/policies", map[string]any{"server": "shared", "tool": "*", "subject": "team:designers", "action": "deny", "note": ""}, nil); code != http.StatusCreated {
		t.Fatalf("team rule: %d %v", code, out)
	}
	if text, isErr := call(t, adaClient, "shared__echo", map[string]any{"text": "x"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("ada under the team rule: %q %v", text, isErr)
	}
	if text, isErr := call(t, bobClient, "shared__echo", map[string]any{"text": "ok"}); isErr || text != "ok" {
		t.Fatalf("bob is not in the team: %q %v", text, isErr)
	}

	// 4. Taking the server's teams away hides it from everyone.
	if code, out := boss.app(http.MethodPatch, "/v1/servers/design", map[string]any{"teams": []string{}}, nil); code != http.StatusOK {
		t.Fatalf("clear teams: %d %v", code, out)
	}
	if text, isErr := call(t, adaClient, "design__echo", map[string]any{"text": "x"}); !isErr || !strings.Contains(text, "is not available") {
		t.Fatalf("a server without teams still answers: %q %v", text, isErr)
	}
	g.eventually("a server without teams leaves the list", 8*time.Second, func() bool {
		return !slices.Contains(toolList(t, adaClient), "design__echo")
	})

	// 5. Deleting the team deletes its rule.
	if code, out := boss.app(http.MethodDelete, "/v1/teams/designers", nil, nil); code != http.StatusOK {
		t.Fatalf("delete team: %d %v", code, out)
	}
	if text, isErr := call(t, adaClient, "shared__echo", map[string]any{"text": "back"}); isErr || text != "back" {
		t.Fatalf("the team's rule outlived the team: %q %v", text, isErr)
	}

	// 6. Members cannot see or change teams or the catalog.
	for _, path := range []string{"/v1/teams", "/v1/catalog"} {
		if code, _ := ada.app(http.MethodGet, path, nil, nil); code != http.StatusForbidden {
			t.Errorf("member GET %s: %d, want 403", path, code)
		}
	}
	if code, _ := ada.app(http.MethodPost, "/v1/teams", map[string]any{"name": "Mine"}, nil); code != http.StatusForbidden {
		t.Errorf("member creating a team: %d, want 403", code)
	}

	// 7. The changes are in the audit log with the owner as actor.
	g.eventually("team changes are audited", 5*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=100&kind=admin", nil, nil)
		s := fmt.Sprint(out)
		return strings.Contains(s, "team.create:designers") && strings.Contains(s, "team.member.add:designers:ada") &&
			strings.Contains(s, "server.teams=designers") && strings.Contains(s, "team.delete:designers")
	})
}

func aliasesOf(items []map[string]any) []string {
	out := []string{}
	for _, m := range items {
		out = append(out, fmt.Sprint(m["alias"]))
	}
	return out
}
