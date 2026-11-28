//go:build acceptance

package acceptance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// twoToolServer offers echo and shout.
func twoToolServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "two", Version: "0"}, nil)
	type in struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Repeat the text."},
		func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "shout", Description: "Repeat the text in capitals."},
		func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.ToUpper(a.Text)}}}, nil, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts
}

// ADR 0026 through the real binary: a team sees only the tools chosen for it.
func TestToolsetsLimitWhatATeamSees(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	for _, team := range []string{"Designers", "Developers"} {
		if code, out := boss.app(http.MethodPost, "/v1/teams", map[string]any{"name": team}, nil); code != http.StatusCreated {
			t.Fatalf("team %s: %d %v", team, code, out)
		}
	}
	addReviewed(t, g, boss, map[string]any{"alias": "kit", "transport": "http", "url": twoToolServer(t).URL + "/mcp", "teams": []string{"designers", "developers"}})
	ada, adaToken := join(t, g, gh, boss, "ada", 2)
	_, bobToken := join(t, g, gh, boss, "bob", 3)
	for _, m := range [][2]string{{"designers", "ada"}, {"developers", "bob"}} {
		if code, out := boss.app(http.MethodPost, "/v1/teams/"+m[0]+"/members", map[string]any{"person": m[1]}, nil); code != http.StatusOK {
			t.Fatalf("member %v: %d %v", m, code, out)
		}
	}
	adaClient, bobClient := g.connect(adaToken), g.connect(bobToken)
	if got := toolList(t, adaClient); !slices.Equal(got, []string{"kit__echo", "kit__shout"}) {
		t.Fatalf("before narrowing ada sees %v", got)
	}

	// The designers get echo only.
	if code, out := boss.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{"tools": []string{"echo"}}, nil); code != http.StatusOK {
		t.Fatalf("narrow: %d %v", code, out)
	}
	// Lists may be reused for a few seconds (ADR 0003); the next fresh one follows.
	g.eventually("ada (designers) sees echo only", 6*time.Second, func() bool { return slices.Equal(toolList(t, adaClient), []string{"kit__echo"}) })
	if got := toolList(t, bobClient); !slices.Equal(got, []string{"kit__echo", "kit__shout"}) {
		t.Fatalf("bob (developers) sees %v", got)
	}
	unknown, _ := call(t, adaClient, "kit__nope", map[string]any{"text": "x"})
	hidden, isErr := call(t, adaClient, "kit__shout", map[string]any{"text": "x"})
	if !isErr || strings.ReplaceAll(hidden, "kit__shout", "X") != strings.ReplaceAll(unknown, "kit__nope", "X") {
		t.Fatalf("a hidden tool: %q, an unknown one: %q", hidden, unknown)
	}
	if text, isErr := call(t, adaClient, "kit__echo", map[string]any{"text": "hi"}); isErr || text != "hi" {
		t.Fatalf("a chosen tool: %q %v", text, isErr)
	}
	// Start counts only what ada sees.
	_, mine := ada.app(http.MethodGet, "/v1/me/tools", nil, nil)
	for _, s := range list(mine, "servers") {
		if s["alias"] == "kit" && s["tools"] != float64(1) {
			t.Fatalf("start counts %v tools of kit for ada", s["tools"])
		}
	}
	// The server list counts only what ada sees, so she does not learn how
	// many tools are hidden (security review 2026-11-20).
	_, servers := ada.app(http.MethodGet, "/v1/servers", nil, nil)
	for _, s := range list(servers, "servers") {
		if items, _ := s["items"].(map[string]any); s["alias"] == "kit" && items["tool"] != float64(1) {
			t.Fatalf("the server list counts %v tools of kit for ada", items["tool"])
		}
	}
	// A new choice applies to the next request.
	if code, _ := boss.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{"tools": []string{"shout"}}, nil); code != http.StatusOK {
		t.Fatal("change")
	}
	g.eventually("ada sees shout only", 5*time.Second, func() bool { return slices.Equal(toolList(t, adaClient), []string{"kit__shout"}) })
	if text, isErr := call(t, adaClient, "kit__shout", map[string]any{"text": "hi"}); isErr || text != "HI" {
		t.Fatalf("shout: %q %v", text, isErr)
	}
	// Members cannot change toolsets.
	if code, _ := ada.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{"tools": nil}, nil); code != http.StatusForbidden {
		t.Fatalf("a member changed a toolset: %d", code)
	}
}

// F26 R-03: a call on a server outside the caller's teams, or on a tool
// their toolset hides, is answered like an unknown name, and the audit
// records it once a minute per person, server and reason.
func TestCallsOutsideTheTeamsAreAudited(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	for _, team := range []string{"Designers", "Developers"} {
		if code, out := boss.app(http.MethodPost, "/v1/teams", map[string]any{"name": team}, nil); code != http.StatusCreated {
			t.Fatalf("team %s: %d %v", team, code, out)
		}
	}
	addReviewed(t, g, boss, map[string]any{"alias": "kit", "transport": "http", "url": twoToolServer(t).URL + "/mcp", "teams": []string{"designers"}})
	_, adaToken := join(t, g, gh, boss, "ada", 2)
	_, bobToken := join(t, g, gh, boss, "bob", 3)
	for _, m := range [][2]string{{"designers", "ada"}, {"developers", "bob"}} {
		if code, out := boss.app(http.MethodPost, "/v1/teams/"+m[0]+"/members", map[string]any{"person": m[1]}, nil); code != http.StatusOK {
			t.Fatalf("member %v: %d %v", m, code, out)
		}
	}
	if code, out := boss.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{"tools": []string{"echo"}}, nil); code != http.StatusOK {
		t.Fatalf("narrow: %d %v", code, out)
	}
	ada, bob := g.connect(adaToken), g.connect(bobToken)
	unknown, _ := call(t, bob, "kit__nope", map[string]any{"text": "x"})
	for range 3 {
		outside, isErr := call(t, bob, "kit__echo", map[string]any{"text": "x"})
		if !isErr || strings.ReplaceAll(outside, "kit__echo", "X") != strings.ReplaceAll(unknown, "kit__nope", "X") {
			t.Fatalf("outside the teams: %q, unknown: %q", outside, unknown)
		}
	}
	g.eventually("ada's toolset hides shout", 6*time.Second, func() bool { return !slices.Contains(toolList(t, ada), "kit__shout") })
	if _, isErr := call(t, ada, "kit__shout", map[string]any{"text": "x"}); !isErr {
		t.Fatal("a hidden tool answered")
	}
	count := func(decision string) int {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=200&decision="+decision, nil, nil)
		return len(list(out, "events"))
	}
	g.eventually("the refusals are in the audit", 6*time.Second, func() bool {
		return count("denied:not_in_team") == 1 && count("denied:not_in_toolset") == 1
	})
	// An unknown name stays unrecorded.
	if n := count("denied:unknown"); n != 0 {
		t.Fatalf("%d records of an unknown name", n)
	}
}

// ADR 0026 amendment: a member sees their granted tools on Start, can turn
// individual tools off for themselves, gets an explicit reminder on call (with
// no approval created), and admin restrictions still override silently.
func TestMemberCanToggleOwnToolsAndGetsClearRefusalWhenMuted(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	if code, out := boss.app(http.MethodPost, "/v1/teams", map[string]any{"name": "Designers"}, nil); code != http.StatusCreated {
		t.Fatalf("team: %d %v", code, out)
	}
	addReviewed(t, g, boss, map[string]any{"alias": "kit", "transport": "http", "url": twoToolServer(t).URL + "/mcp", "teams": []string{"designers"}})
	ada, adaToken := join(t, g, gh, boss, "ada", 2)
	_, bobToken := join(t, g, gh, boss, "bob", 3)
	for _, person := range []string{"ada", "bob"} {
		if code, out := boss.app(http.MethodPost, "/v1/teams/designers/members", map[string]any{"person": person}, nil); code != http.StatusOK {
			t.Fatalf("add %s: %d %v", person, code, out)
		}
	}
	adaClient, bobClient := g.connect(adaToken), g.connect(bobToken)
	g.eventually("ada sees both tools", 6*time.Second, func() bool {
		return slices.Equal(toolList(t, adaClient), []string{"kit__echo", "kit__shout"})
	})

	// Start lists both tools with enabled=true for ada.
	_, mine := ada.app(http.MethodGet, "/v1/me/tools", nil, nil)
	var sawToolList int
	for _, s := range list(mine, "servers") {
		if s["alias"] == "kit" {
			if s["tools"] != float64(2) || s["total_tools"] != float64(2) {
				t.Fatalf("before toggle: %v", s)
			}
			sawToolList = len(list(s, "tool_list"))
		}
	}
	if sawToolList != 2 {
		t.Fatalf("expected 2 tools in tool_list, got %d", sawToolList)
	}

	// Ada turns off shout for herself.
	if code, out := ada.app(http.MethodPatch, "/v1/me/servers/kit/tools", map[string]any{"disabled": []string{"shout"}}, nil); code != http.StatusOK {
		t.Fatalf("disable shout: %d %v", code, out)
	}
	g.eventually("ada's tools/list omits shout", 6*time.Second, func() bool {
		return slices.Equal(toolList(t, adaClient), []string{"kit__echo"})
	})
	// Bob is unaffected.
	if got := toolList(t, bobClient); !slices.Equal(got, []string{"kit__echo", "kit__shout"}) {
		t.Fatalf("bob affected by ada's toggle: %v", got)
	}
	// Start shows 1 of 2 for ada, with shout marked enabled=false.
	_, mine = ada.app(http.MethodGet, "/v1/me/tools", nil, nil)
	for _, s := range list(mine, "servers") {
		if s["alias"] == "kit" && (s["tools"] != float64(1) || s["total_tools"] != float64(2)) {
			t.Fatalf("after toggle: %v", s)
		}
	}

	// Put an ask rule on kit/*: calling the self-disabled tool must refuse
	// explicitly BEFORE creating an approval request.
	if code, out := boss.app(http.MethodPost, "/v1/policies", map[string]any{"server": "kit", "tool": "*", "action": "ask"}, nil); code != http.StatusCreated {
		t.Fatalf("ask rule: %d %v", code, out)
	}
	muted, isErr := call(t, adaClient, "kit__shout", map[string]any{"text": "hi"})
	if !isErr || !strings.Contains(muted, "You turned off") || !strings.Contains(muted, `"shout"`) || !strings.Contains(muted, "/admin/start") {
		t.Fatalf("self-disabled call = %q (err=%v)", muted, isErr)
	}
	if _, pending := boss.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil); len(list(pending, "approvals")) != 0 {
		t.Fatalf("self-disabled call created an approval: %v", pending)
	}
	g.eventually("self-disabled refusal audited", 6*time.Second, func() bool {
		_, evs := boss.app(http.MethodGet, "/v1/audit/events?limit=50&decision=refused:disabled_by_user", nil, nil)
		return len(list(evs, "events")) == 1
	})

	// Admin hides shout from designers: now ada gets "not available" (same as
	// an unknown name) — never revealing that the tool still exists.
	if code, out := boss.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{"tools": []string{"echo"}}, nil); code != http.StatusOK {
		t.Fatalf("narrow team: %d %v", code, out)
	}
	unknown, _ := call(t, adaClient, "kit__nope", map[string]any{"text": "x"})
	adminHidden, isErr := call(t, adaClient, "kit__shout", map[string]any{"text": "x"})
	if !isErr || strings.ReplaceAll(adminHidden, "kit__shout", "X") != strings.ReplaceAll(unknown, "kit__nope", "X") {
		t.Fatalf("admin-hidden after self-disable = %q, unknown = %q", adminHidden, unknown)
	}

	// Admin restores all tools and ada re-enables all: shout returns.
	if code, out := boss.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{"tools": nil}, nil); code != http.StatusOK {
		t.Fatalf("restore team: %d %v", code, out)
	}
	if code, out := ada.app(http.MethodPatch, "/v1/me/servers/kit/tools", map[string]any{"disabled": []string{}}, nil); code != http.StatusOK {
		t.Fatalf("re-enable all: %d %v", code, out)
	}
	g.eventually("ada sees both tools again", 6*time.Second, func() bool {
		return slices.Equal(toolList(t, adaClient), []string{"kit__echo", "kit__shout"})
	})
}
