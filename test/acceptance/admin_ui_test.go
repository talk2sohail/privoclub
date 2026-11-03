//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The whole first-day flow as the admin app does it (ADR 0019), no curl: claim
// the setup link, add a server, review its tools, add an ask rule, invite a
// member, the member makes their AI-client token, their call waits, the owner
// approves, the activity shows it, and removing the member ends their token.
// Each step is the request the app sends: JSON through the session, with the
// CSRF header.

var assetRef = regexp.MustCompile(`/admin/app/assets/[A-Za-z0-9._-]+\.js`)

func plainMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "plain", Version: "0"}, nil)
	type in struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Repeat the text. <b>bold</b> stays text."},
		func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts
}

func list(out map[string]any, key string) []map[string]any {
	raw, _ := out[key].([]any)
	res := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			res = append(res, m)
		}
	}
	return res
}

func TestAdminUIFromScratch(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	upstreamSrv := plainMCPServer(t)

	// 1. The owner claims the setup link and lands on Servers: the app's shell,
	// with its script served from the binary and cached for good.
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
	res, shell := boss.do(http.MethodGet, g.base+"/admin/servers", nil, nil)
	script := assetRef.FindString(shell)
	if res.StatusCode != http.StatusOK || script == "" {
		t.Fatalf("app shell: %d\n%s", res.StatusCode, shell)
	}
	if res, js := newPerson(t, g).do(http.MethodGet, g.base+script, nil, map[string]string{"Accept-Encoding": "identity"}); res.StatusCode != http.StatusOK ||
		!strings.Contains(res.Header.Get("Cache-Control"), "immutable") || !strings.Contains(js, "/admin/api/session") {
		t.Fatalf("app script: %d %v", res.StatusCode, res.Header)
	}

	// 2. Add a server.
	if code, out := boss.app(http.MethodPost, "/v1/servers", map[string]any{"alias": "plain", "url": upstreamSrv.URL + "/mcp", "transport": "http"}, nil); code != http.StatusCreated {
		t.Fatalf("add server: %d %v", code, out)
	}

	// 3. Review its tool: the description is the upstream's text, as data.
	var pins []map[string]any
	g.eventually("the tool waits for review", 10*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/servers/plain/pins", nil, nil)
		pins = list(out, "pins")
		return len(pins) > 0
	})
	if !strings.Contains(fmt.Sprint(pins[0]["definition"]), "Repeat the text. <b>bold</b>") {
		t.Fatalf("pin %v", pins[0])
	}
	accept := []map[string]any{}
	for _, p := range pins {
		accept = append(accept, map[string]any{"kind": p["kind"], "hash": p["hash"], "name": p["name"]})
	}
	if code, out := boss.app(http.MethodPost, "/v1/servers/plain/pins:accept", map[string]any{"pins": accept}, nil); code != http.StatusOK {
		t.Fatalf("accept: %d %v", code, out)
	}

	// 4. Every call to it needs approval.
	if code, out := boss.app(http.MethodPost, "/v1/policies", map[string]any{"server": "plain", "tool": "*", "subject": "*", "action": "ask", "note": ""}, nil); code != http.StatusCreated {
		t.Fatalf("add rule: %d %v", code, out)
	}

	// 5. Invite Ada; she joins and makes her AI-client token herself.
	code, inv := boss.app(http.MethodPost, "/v1/invites", map[string]any{"person": "ada", "role": "member"}, nil)
	invite, _ := inv["invite"].(map[string]any)
	link, _ := invite["url"].(string)
	if code != http.StatusCreated || !strings.Contains(link, "/admin/invite/privo_invite_") {
		t.Fatalf("invite: %d %v", code, inv)
	}
	ada := newPerson(t, g)
	_, body = ada.do(http.MethodGet, link, nil, nil)
	gh.as = fakeGitHubUser{ID: 2, Login: "ada-gh", Name: "Ada"}
	start, _ = ada.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"invite_token": {inviteTokenField.FindStringSubmatch(body)[1]}}, nil)
	if cb, _ := ada.signInThroughGitHub(start); cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/admin/tokens" {
		t.Fatalf("ada joins: %d %q", cb.StatusCode, cb.Header.Get("Location"))
	}
	if code, _ := ada.app(http.MethodGet, "/v1/servers/plain/pins", nil, nil); code != http.StatusForbidden {
		t.Errorf("a member reviewing tools: %d, want 403", code)
	}
	code, tok := ada.app(http.MethodPost, "/v1/me/tokens", map[string]any{"name": "claude-code"}, nil)
	adaToken, _ := tok["token"].(string)
	if code != http.StatusCreated || !strings.HasPrefix(adaToken, "privo_pat_") || tok["person"] != "ada" || tok["kind"] != "gateway" {
		t.Fatalf("ada's token: %d %v", code, tok)
	}

	// 6. Her call waits; the owner approves it; the retry runs.
	client := g.connect(adaToken)
	if text, isErr := call(t, client, "plain__echo", map[string]any{"text": "hello"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("first call: %q", text)
	}
	id := boss.pendingID("hello")
	if code, out := boss.app(http.MethodPost, "/v1/approvals/"+id+"/approve", nil, nil); code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, out)
	}
	if text, isErr := call(t, client, "plain__echo", map[string]any{"text": "hello"}); isErr || text != "hello" {
		t.Fatalf("approved retry: %q (error=%v)", text, isErr)
	}

	// 7. Activity shows the call, who approved it, and the owner's changes.
	g.eventually("activity shows the approved call", 5*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=100", nil, nil)
		for _, e := range list(out, "events") {
			if e["item"] == "echo" && strings.HasPrefix(fmt.Sprint(e["approved_by"]), "boss (user ") {
				return true
			}
		}
		return false
	})
	g.eventually("admin changes name the owner as actor", 5*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=100&kind=admin", nil, nil)
		s := fmt.Sprint(out)
		return strings.Contains(s, "server.create") && strings.Contains(s, "policy.create") && strings.Contains(s, "invite.create") && strings.Contains(s, "actor_id:user:")
	})

	// 8. Removing Ada ends her token and her session.
	if code, out := boss.app(http.MethodPost, "/v1/members/ada/remove", nil, nil); code != http.StatusOK {
		t.Fatalf("remove: %d %v", code, out)
	}
	g.eventually("ada's token stops working", 5*time.Second, func() bool {
		code, _ := g.raw(http.MethodPost, "/mcp", adaToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
		return code == http.StatusUnauthorized
	})
	if res, _ := ada.do(http.MethodGet, g.base+"/admin/tokens", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("ada's session survived: %d", res.StatusCode)
	}
	if res, _ := ada.do(http.MethodGet, g.base+"/admin/api/session", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ada's session answers the app: %d", res.StatusCode)
	}
}
