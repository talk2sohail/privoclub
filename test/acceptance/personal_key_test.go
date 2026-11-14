//go:build acceptance

package acceptance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ADR 0023: a server that takes each person's own API key. Each person enters
// their key once on the connect page; every call runs with the caller's own
// key, in the header the admin named, and never anyone else's.

type keyUpstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys map[string]string // key -> whose it is
	seen []string          // the key of every request, "" for none
}

func newKeyUpstream(t *testing.T, keys map[string]string) *keyUpstream {
	t.Helper()
	u := &keyUpstream{keys: keys}
	servers := map[string]*mcp.Server{}
	serverFor := func(owner string) *mcp.Server {
		u.mu.Lock()
		defer u.mu.Unlock()
		if s, ok := servers[owner]; ok {
			return s
		}
		s := mcp.NewServer(&mcp.Implementation{Name: "acme-fixture", Version: "0"}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "Whose key is calling."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "you are " + owner}}}, nil, nil
			})
		servers[owner] = s
		return s
	}
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return serverFor(u.keys[r.Header.Get("X-Api-Key")]) }, nil)
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Api-Key")
		u.mu.Lock()
		u.seen = append(u.seen, key)
		_, ok := u.keys[key]
		u.mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected Authorization header", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "unknown key", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

var keyField = regexp.MustCompile(`<input type="password" name="key"`)

// saveKey plays the browser: the connect page, then the form.
func saveKey(t *testing.T, p *person, g *gateway, alias, key string) *http.Response {
	t.Helper()
	page, body := p.do(http.MethodGet, g.base+"/connect/"+alias, nil, nil)
	m := csrfField.FindStringSubmatch(body)
	if page.StatusCode != http.StatusOK || m == nil || !keyField.MatchString(body) {
		t.Fatalf("key page: %d\n%s", page.StatusCode, body)
	}
	res, body := p.do(http.MethodPost, g.base+"/connect/"+alias+"/key", url.Values{"csrf": {m[1]}, "key": {key}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("saving the key: %d\n%s", res.StatusCode, body)
	}
	return res
}

func TestPersonalKeysEachPersonUsesTheirOwn(t *testing.T) {
	bossKey, adaKey := "acme-boss-"+randomHex(16), "acme-ada-"+randomHex(16)
	up := newKeyUpstream(t, map[string]string{bossKey: "boss@acme", adaKey: "ada@acme"})
	w := newOAuthWorld(t, true)
	g := w.g

	res := g.api(http.MethodPost, "/api/v1/servers", g.owner, map[string]any{"alias": "acme", "transport": "http", "url": up.srv.URL + "/mcp",
		"personal_key": map[string]any{"header": "X-Api-Key", "help": "Find it under Settings, API keys.", "help_url": "https://acme.example/settings/api"}}, http.StatusCreated)
	srv, _ := res["server"].(map[string]any)
	if srv["auth"] != "key" || srv["key_header"] != "X-Api-Key" || srv["connect_url"] != g.base+"/connect/acme" || srv["status"] == "up" {
		t.Fatalf("server after adding: %v", srv)
	}
	// The page names where the key goes and where to get it.
	_, page := w.boss.do(http.MethodGet, g.base+"/connect/acme", nil, nil)
	for _, want := range []string{"Your acme API key", "Find it under Settings, API keys.", "https://acme.example/settings/api", `type="password"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the key page does not show %q", want)
		}
	}

	// The boss's key lists the tools; then the server is up.
	if back := saveKey(t, w.boss, g, "acme", bossKey); back.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("after saving: %q", back.Header.Get("Location"))
	}
	g.eventually("acme comes up with the boss's key", 10*time.Second, func() bool { return g.serverStatus("acme") == "up" })
	g.acceptPins("acme")

	// Ada joins. Before she enters a key her client is told where to do it.
	adaWeb, adaTok := join(t, g, w.gh, w.boss, "ada", 2)
	ada := g.connect(adaTok)
	g.eventually("ada sees the tool", 5*time.Second, func() bool { return slices.Contains(toolList(t, ada), "acme__whoami") })
	if text, isErr := call(t, ada, "acme__whoami", nil); !isErr || !strings.Contains(text, g.base+"/connect/acme") {
		t.Fatalf("ada without a key: %q (error=%v), want the connect link", text, isErr)
	}
	// A wrong key: the server refuses it, and she is told so, with the link.
	saveKey(t, adaWeb, g, "acme", "acme-wrong-"+randomHex(8))
	if text, isErr := call(t, ada, "acme__whoami", nil); !isErr || !strings.Contains(text, "refused your API key") || !strings.Contains(text, g.base+"/connect/acme") {
		t.Fatalf("ada with a wrong key: %q (error=%v)", text, isErr)
	}
	// Her own key: her own account.
	saveKey(t, adaWeb, g, "acme", adaKey)
	if text, isErr := call(t, ada, "acme__whoami", nil); isErr || text != "you are ada@acme" {
		t.Fatalf("ada with her key: %q (error=%v)", text, isErr)
	}
	boss := g.connect(w.bossTok)
	if text, isErr := call(t, boss, "acme__whoami", nil); isErr || text != "you are boss@acme" {
		t.Fatalf("the boss: %q (error=%v)", text, isErr)
	}
	// Her connections list it as a key, connected.
	_, mine := adaWeb.app(http.MethodGet, "/me/connections", nil, nil)
	found := false
	for _, c := range mine["connections"].([]any) {
		m := c.(map[string]any)
		if m["alias"] == "acme" && m["kind"] == "key" && m["connected"] == true {
			found = true
		}
	}
	if !found {
		t.Errorf("ada's connections: %v", mine)
	}

	// The upstream never saw a request without a key or with a key that was
	// not the caller's: the boss's key only before ada had one or for the
	// boss's own calls is not something it can tell, so count what it can.
	up.mu.Lock()
	for _, k := range up.seen {
		if k == "" {
			t.Error("a request reached the upstream without a key")
		}
	}
	up.mu.Unlock()

	// The upstream revokes ada's key while her connection is open: the next
	// call says so, with the link, and the boss is not affected.
	up.mu.Lock()
	delete(up.keys, adaKey)
	up.mu.Unlock()
	if text, isErr := call(t, ada, "acme__whoami", nil); !isErr || !strings.Contains(text, "refused your API key") || !strings.Contains(text, g.base+"/connect/acme") {
		t.Fatalf("ada after the upstream revoked her key: %q (error=%v)", text, isErr)
	}
	if text, isErr := call(t, boss, "acme__whoami", nil); isErr || text != "you are boss@acme" {
		t.Fatalf("the boss after ada's key was revoked: %q (error=%v)", text, isErr)
	}
	up.mu.Lock()
	up.keys[adaKey] = "ada@acme"
	up.mu.Unlock()

	// Removed: the next call asks for a key again.
	if code, out := adaWeb.app(http.MethodDelete, "/me/connections/acme", nil, nil); code != http.StatusOK {
		t.Fatalf("removing the key: %d %v", code, out)
	}
	if text, isErr := call(t, ada, "acme__whoami", nil); !isErr || !strings.Contains(text, g.base+"/connect/acme") {
		t.Errorf("ada after removing her key: %q (error=%v)", text, isErr)
	}
	// No key is on disk in the clear or in the log.
	for _, k := range []string{bossKey, adaKey} {
		g.assertNotOnDisk(k)
		if strings.Contains(g.logs.String(), k) {
			t.Error("the log holds an API key")
		}
	}
}
