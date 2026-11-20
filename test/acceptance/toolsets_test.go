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
