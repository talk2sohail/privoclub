//go:build acceptance

package acceptance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// F14 (ADR 0018): the whole first-day flow in the browser, no curl: claim the
// setup link, add a server, review its tools, add an ask rule, invite a
// member, the member makes their AI-client token, their call waits, the owner
// approves, the activity shows it, and removing the member ends their token.

var (
	pinField     = regexp.MustCompile(`name="pin" value="([^"]+)"`)
	shownToken   = regexp.MustCompile(`privo_pat_[A-Za-z0-9_]+`)
	inviteLinkRe = regexp.MustCompile(`https?://[^<\s]+/admin/invite/privo_invite_[A-Za-z0-9_-]+`)
)

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

func csrfOn(t *testing.T, p *person, page string) string {
	t.Helper()
	_, body := p.do(http.MethodGet, p.g.base+page, nil, nil)
	m := csrfField.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token on %s:\n%s", page, body)
	}
	return m[1]
}

func TestAdminUIFromScratch(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	upstreamSrv := plainMCPServer(t)

	// 1. The owner claims the setup link and lands on Servers.
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

	// 2. Add a server in the form.
	res, body := boss.do(http.MethodPost, g.base+"/admin/servers", url.Values{"csrf": {csrfOn(t, boss, "/admin/servers")}, "alias": {"plain"},
		"url": {upstreamSrv.URL + "/mcp"}, "transport": {"http"}, "auth": {"none"}}, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/servers/plain?result=added" {
		t.Fatalf("add server: %d %q\n%s", res.StatusCode, res.Header.Get("Location"), body)
	}

	// 3. Review its tool: the description is shown as text.
	var pins []string
	g.eventually("the tool waits for review", 10*time.Second, func() bool {
		_, body = boss.do(http.MethodGet, g.base+"/admin/servers/plain", nil, nil)
		pins = nil
		for _, m := range pinField.FindAllStringSubmatch(body, -1) {
			pins = append(pins, m[1])
		}
		return len(pins) > 0
	})
	if !strings.Contains(body, "Repeat the text. &lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("description not shown as text:\n%s", body)
	}
	form := url.Values{"csrf": {csrfField.FindStringSubmatch(body)[1]}}
	for _, p := range pins {
		form.Add("pin", strings.ReplaceAll(p, "&#43;", "+"))
	}
	if res, body := boss.do(http.MethodPost, g.base+"/admin/servers/plain/accept", form, nil); res.Header.Get("Location") != "/admin/servers/plain?result=accepted" {
		t.Fatalf("accept: %d %q\n%s", res.StatusCode, res.Header.Get("Location"), body)
	}

	// 4. Every call to it needs approval.
	if res, body := boss.do(http.MethodPost, g.base+"/admin/rules", url.Values{"csrf": {csrfOn(t, boss, "/admin/rules")}, "server": {"plain"}, "tool": {"*"},
		"subject": {"*"}, "action": {"ask"}}, nil); res.Header.Get("Location") != "/admin/rules?result=rule-added" {
		t.Fatalf("add rule: %d\n%s", res.StatusCode, body)
	}

	// 5. Invite Ada; she joins and makes her AI-client token herself.
	_, body = boss.do(http.MethodPost, g.base+"/admin/people/invite", url.Values{"csrf": {csrfOn(t, boss, "/admin/people")}, "person": {"ada"}, "role": {"member"}}, nil)
	link := inviteLinkRe.FindString(body)
	if link == "" {
		t.Fatalf("no invite link:\n%s", body)
	}
	ada := newPerson(t, g)
	_, body = ada.do(http.MethodGet, link, nil, nil)
	gh.as = fakeGitHubUser{ID: 2, Login: "ada-gh", Name: "Ada"}
	start, _ = ada.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"invite_token": {inviteTokenField.FindStringSubmatch(body)[1]}}, nil)
	if cb, _ := ada.signInThroughGitHub(start); cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("ada joins: %d", cb.StatusCode)
	}
	_, body = ada.do(http.MethodPost, g.base+"/admin/tokens/mine", url.Values{"csrf": {csrfOn(t, ada, "/admin/tokens")}, "name": {"claude-code"}}, nil)
	adaToken := shownToken.FindString(body)
	if adaToken == "" || !strings.Contains(body, "claude mcp add --transport http privo "+g.base+"/mcp") {
		t.Fatalf("token page:\n%s", body)
	}

	// 6. Her call waits; the owner approves it in the browser; the retry runs.
	client := g.connect(adaToken)
	if text, isErr := call(t, client, "plain__echo", map[string]any{"text": "hello"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("first call: %q", text)
	}
	_, page := boss.approvals()
	am := approveAction.FindStringSubmatch(page)
	if am == nil {
		t.Fatalf("approvals page:\n%s", page)
	}
	boss.do(http.MethodPost, g.base+am[0], url.Values{"csrf": {csrfField.FindStringSubmatch(page)[1]}}, nil)
	if text, isErr := call(t, client, "plain__echo", map[string]any{"text": "hello"}); isErr || text != "hello" {
		t.Fatalf("approved retry: %q (error=%v)", text, isErr)
	}

	// 7. Activity shows the call and who approved it.
	g.eventually("activity shows the approved call", 5*time.Second, func() bool {
		_, body := boss.do(http.MethodGet, g.base+"/admin/activity", nil, nil)
		return strings.Contains(body, "echo") && strings.Contains(body, "by boss")
	})
	g.eventually("admin changes show the page actions as the owner", 5*time.Second, func() bool {
		_, body := boss.do(http.MethodGet, g.base+"/admin/activity?view=admin", nil, nil)
		return strings.Contains(body, "server.create") && strings.Contains(body, "policy.create") && strings.Contains(body, "invite.create")
	})

	// 8. Removing Ada ends her token.
	if res, body := boss.do(http.MethodPost, g.base+"/admin/people/ada/remove", url.Values{"csrf": {csrfOn(t, boss, "/admin/people")}, "confirm": {"yes"}}, nil); res.Header.Get("Location") != "/admin/people?result=removed" {
		t.Fatalf("remove: %d\n%s", res.StatusCode, body)
	}
	g.eventually("ada's token stops working", 5*time.Second, func() bool {
		code, _ := g.raw(http.MethodPost, "/mcp", adaToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
		return code == http.StatusUnauthorized
	})
	if res, _ := ada.do(http.MethodGet, g.base+"/admin/tokens", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("ada's session survived: %d", res.StatusCode)
	}
}
