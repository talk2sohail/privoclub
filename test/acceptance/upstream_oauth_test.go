//go:build acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// F07 (ADR 0017): a remote MCP server that needs each person's own OAuth
// account, against the real binary. The fixture is the MCP server and its
// authorization server in one: it checks PKCE, the resource parameter and the
// redirect URI, hands out short-lived tokens so refresh is exercised, and its
// tool "whoami" answers with the subject of the token it received.

type oauthUpstream struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	dynamic     bool   // offer dynamic registration
	clientID    string // the registered or pre-registered client id
	secret      string // its secret ("" for a public client)
	as          string // subject of the next authorization
	codes       map[string]oauthCode
	access      map[string]string // access token -> subject
	refresh     map[string]string // refresh token -> subject
	revoked     []string
	refreshes   int
	issued      []string // every code and token handed out
	registered  int
	expiresIn   int
	subjectSeen []string
}

type oauthCode struct{ challenge, redirect, resource, subject, client string }

func newOAuthUpstream(t *testing.T, dynamic bool) *oauthUpstream {
	t.Helper()
	u := &oauthUpstream{t: t, dynamic: dynamic, codes: map[string]oauthCode{}, access: map[string]string{}, refresh: map[string]string{}, expiresIn: 3600}
	if !dynamic {
		u.clientID, u.secret = "preregistered-client", "preregistered-secret-NEVER-LEAK"
	}
	servers := map[string]*mcp.Server{}
	serverFor := func(subject string) *mcp.Server {
		u.mu.Lock()
		defer u.mu.Unlock()
		if s, ok := servers[subject]; ok {
			return s
		}
		s := mcp.NewServer(&mcp.Implementation{Name: "linear-fixture", Version: "0"}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "Who is calling."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				u.mu.Lock()
				u.subjectSeen = append(u.subjectSeen, subject)
				u.mu.Unlock()
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "you are " + subject}}}, nil, nil
			})
		servers[subject] = s
		return s
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return serverFor(u.subject(r)) }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if u.subject(r) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+u.srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": u.srv.URL + "/mcp", "authorization_servers": []string{u.srv.URL}, "scopes_supported": []string{"read"}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		m := map[string]any{"issuer": u.srv.URL, "authorization_endpoint": u.srv.URL + "/authorize", "token_endpoint": u.srv.URL + "/token",
			"revocation_endpoint": u.srv.URL + "/revoke", "code_challenge_methods_supported": []string{"S256"},
			"authorization_response_iss_parameter_supported": true}
		if u.dynamic {
			m["registration_endpoint"] = u.srv.URL + "/register"
		}
		_ = json.NewEncoder(w).Encode(m)
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		defer u.mu.Unlock()
		u.registered++
		u.clientID = fmt.Sprintf("dyn-%d", u.registered)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": u.clientID})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		u.mu.Lock()
		defer u.mu.Unlock()
		if q.Get("client_id") != u.clientID || q.Get("code_challenge_method") != "S256" || q.Get("resource") != u.srv.URL+"/mcp" || q.Get("state") == "" {
			http.Error(w, "bad authorization request", http.StatusBadRequest)
			return
		}
		code := "code-" + randomHex(10)
		u.issued = append(u.issued, code)
		u.codes[code] = oauthCode{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), resource: q.Get("resource"), subject: u.as, client: q.Get("client_id")}
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", code)
		bq.Set("state", q.Get("state"))
		bq.Set("iss", u.srv.URL)
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		u.mu.Lock()
		defer u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fail := func() { w.WriteHeader(http.StatusBadRequest); _, _ = w.Write([]byte(`{"error":"invalid_grant"}`)) }
		id, sec, basic := r.BasicAuth()
		if !basic {
			id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if id != u.clientID || sec != u.secret || r.PostForm.Get("resource") != u.srv.URL+"/mcp" {
			fail()
			return
		}
		var subject string
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			c, ok := u.codes[r.PostForm.Get("code")]
			delete(u.codes, r.PostForm.Get("code"))
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if !ok || c.redirect != r.PostForm.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
				fail()
				return
			}
			subject = c.subject
		case "refresh_token":
			s, ok := u.refresh[r.PostForm.Get("refresh_token")]
			delete(u.refresh, r.PostForm.Get("refresh_token")) // rotation
			if !ok {
				fail()
				return
			}
			u.refreshes++
			subject = s
		default:
			fail()
			return
		}
		at, rt := "at-"+randomHex(12), "rt-"+randomHex(12)
		u.access[at], u.refresh[rt] = subject, subject
		u.issued = append(u.issued, at, rt)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": at, "token_type": "Bearer", "refresh_token": rt, "expires_in": u.expiresIn, "scope": "read"})
	})
	mux.HandleFunc("POST /revoke", func(_ http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		u.mu.Lock()
		defer u.mu.Unlock()
		tok := r.PostForm.Get("token")
		u.revoked = append(u.revoked, tok)
		if s, ok := u.refresh[tok]; ok {
			delete(u.refresh, tok)
			for a, sub := range u.access {
				if sub == s {
					delete(u.access, a)
				}
			}
		}
	})
	u.srv = httptest.NewServer(mux)
	t.Cleanup(u.srv.Close)
	return u
}

func (u *oauthUpstream) subject(r *http.Request) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
}

func (u *oauthUpstream) signInAs(subject string) {
	u.mu.Lock()
	u.as = subject
	u.mu.Unlock()
}

// connectAccount plays the browser: the connect page, the start, the
// authorization server, back to Privo. It returns the final redirect.
func connectAccount(t *testing.T, p *person, g *gateway, alias string) *http.Response {
	t.Helper()
	page, body := p.do(http.MethodGet, g.base+"/connect/"+alias, nil, nil)
	m := csrfField.FindStringSubmatch(body)
	if page.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("connect page: %d\n%s", page.StatusCode, body)
	}
	start, body := p.do(http.MethodPost, g.base+"/connect/"+alias+"/start", url.Values{"csrf": {m[1]}}, nil)
	if start.StatusCode != http.StatusSeeOther {
		t.Fatalf("connect start: %d\n%s", start.StatusCode, body)
	}
	atAS, body := p.follow("start", start)
	if atAS.StatusCode != http.StatusFound {
		t.Fatalf("authorization server: %d %s", atAS.StatusCode, body)
	}
	back, body := p.follow("authorization server", atAS)
	if back.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback: %d\n%s", back.StatusCode, body)
	}
	return back
}

var inviteTokenField = regexp.MustCompile(`name="invite_token" value="([^"]+)"`)

// oauthWorld is a gateway with web sign-in, an owner "boss" signed in, and an
// oauth server "linear" added (not yet connected).
type oauthWorld struct {
	g        *gateway
	gh       *acceptanceGitHub
	up       *oauthUpstream
	boss     *person
	bossTok  string
	serverJS map[string]any
}

func newOAuthWorld(t *testing.T, dynamic bool) *oauthWorld {
	t.Helper()
	w := &oauthWorld{gh: newAcceptanceGitHub(t), up: newOAuthUpstream(t, dynamic)}
	w.g = webGateway(t, w.gh)
	g := w.g
	out, err := g.command("admin", "setup-link", "--person", "boss").Output()
	if err != nil {
		t.Fatal(err)
	}
	w.boss = newPerson(t, g)
	_, body := w.boss.do(http.MethodGet, strings.TrimSpace(string(out)), nil, nil)
	m := setupTokenField.FindStringSubmatch(body)
	w.gh.as = fakeGitHubUser{ID: 1, Login: "boss", Name: "Boss"}
	start, _ := w.boss.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"setup_token": {m[1]}}, nil)
	if cb, _ := w.boss.signInThroughGitHub(start); cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("boss setup: %d", cb.StatusCode)
	}
	oauth := map[string]any{}
	if !dynamic {
		oauth = map[string]any{"client_id": w.up.clientID, "client_secret": w.up.secret}
	}
	res := g.api(http.MethodPost, "/api/v1/servers", g.owner, map[string]any{"alias": "linear", "transport": "http", "url": w.up.srv.URL + "/mcp", "oauth": oauth}, http.StatusCreated)
	w.serverJS, _ = res["server"].(map[string]any)
	tok := g.api(http.MethodPost, "/api/v1/tokens", g.owner, map[string]any{"name": "boss-agent", "role": "owner", "kind": "gateway", "person": "boss"}, http.StatusCreated)
	w.bossTok, _ = tok["token"].(string)
	return w
}

func toolList(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	return out
}

func TestUpstreamOAuthTwoPeople(t *testing.T) {
	w := newOAuthWorld(t, true)
	g, up := w.g, w.up
	if w.serverJS["auth"] != "oauth" || w.serverJS["registration"] != "dynamic" || w.serverJS["connect_url"] != g.base+"/connect/linear" || w.serverJS["status"] == "up" {
		t.Fatalf("server after adding: %v", w.serverJS)
	}
	// The boss (owner) connects: that grant lists the catalog, and the server comes up.
	up.signInAs("boss@linear")
	if back := connectAccount(t, w.boss, g, "linear"); back.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("boss connect: %q", back.Header.Get("Location"))
	}
	g.eventually("linear comes up with the catalog grant", 10*time.Second, func() bool { return g.serverStatus("linear") == "up" })
	g.acceptPins("linear")

	// Ada is invited, joins, and her AI client calls before connecting.
	inv := g.api(http.MethodPost, "/api/v1/invites", g.owner, map[string]any{"person": "ada", "role": "member"}, http.StatusCreated)
	link, _ := inv["invite"].(map[string]any)["url"].(string)
	adaWeb := newPerson(t, g)
	_, body := adaWeb.do(http.MethodGet, link, nil, nil)
	m := inviteTokenField.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("invite page:\n%s", body)
	}
	w.gh.as = fakeGitHubUser{ID: 2, Login: "ada-gh", Name: "Ada"}
	start, _ := adaWeb.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"invite_token": {m[1]}}, nil)
	if cb, _ := adaWeb.signInThroughGitHub(start); cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/admin/start" { // members land on Start
		t.Fatalf("ada joins: %d %q", cb.StatusCode, cb.Header.Get("Location"))
	}
	adaAgent := g.connect(g.issue("ada")) // a gateway token whose person is "ada"
	g.eventually("ada sees the tool", 5*time.Second, func() bool { return slices.Contains(toolList(t, adaAgent), "linear__whoami") })
	text, isErr := call(t, adaAgent, "linear__whoami", nil)
	if !isErr || !strings.Contains(text, g.base+"/connect/linear") {
		t.Fatalf("ada before connecting: %q (error=%v), want the connect link", text, isErr)
	}
	up.mu.Lock()
	for _, s := range up.subjectSeen {
		if s != "boss@linear" {
			t.Fatalf("a call reached the upstream as %q", s)
		}
	}
	callsBefore := len(up.subjectSeen)
	up.mu.Unlock()
	if callsBefore != 0 {
		t.Fatal("ada's unconnected call reached the upstream")
	}

	// Ada connects her own account; each person's calls run as themselves.
	up.signInAs("ada@linear")
	connectAccount(t, adaWeb, g, "linear")
	if text, isErr := call(t, adaAgent, "linear__whoami", nil); isErr || text != "you are ada@linear" {
		t.Fatalf("ada: %q (error=%v)", text, isErr)
	}
	bossAgent := g.connect(w.bossTok)
	if text, isErr := call(t, bossAgent, "linear__whoami", nil); isErr || text != "you are boss@linear" {
		t.Fatalf("boss: %q (error=%v)", text, isErr)
	}
	// A token whose person has no account here gets told to ask for an invite.
	stranger := g.connect(g.issue("stranger"))
	if text, isErr := call(t, stranger, "linear__whoami", nil); !isErr || !strings.Contains(text, "invite") {
		t.Fatalf("stranger: %q", text)
	}

	// Expired access tokens are refreshed by Privo.
	up.mu.Lock()
	up.access = map[string]string{} // every access token stops working upstream
	up.mu.Unlock()
	if text, isErr := call(t, adaAgent, "linear__whoami", nil); isErr || text != "you are ada@linear" {
		t.Fatalf("ada after the upstream dropped her access token: %q (error=%v)", text, isErr)
	}
	up.mu.Lock()
	refreshed := up.refreshes
	up.mu.Unlock()
	if refreshed == 0 {
		t.Fatal("no refresh happened")
	}

	// The admin API lists connections without tokens.
	code, raw := g.raw(http.MethodGet, "/api/v1/servers/linear/connections", g.owner, nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"person":"ada"`) || !strings.Contains(string(raw), `"person":"boss"`) || strings.Contains(string(raw), "at-") || strings.Contains(string(raw), "rt-") {
		t.Fatalf("connections: %d %s", code, raw)
	}

	// Ada disconnects: the token is revoked and her next call gets the link again.
	if code, out := adaWeb.app(http.MethodGet, "/me/connections", nil, nil); code != http.StatusOK || !strings.Contains(fmt.Sprint(out), "connected:true") {
		t.Fatalf("ada's connections: %d %v", code, out)
	}
	if code, out := adaWeb.app(http.MethodDelete, "/me/connections/linear", nil, nil); code != http.StatusOK {
		t.Fatalf("disconnect: %d %v", code, out)
	}
	if text, isErr := call(t, adaAgent, "linear__whoami", nil); !isErr || !strings.Contains(text, "/connect/linear") {
		t.Fatalf("ada after disconnecting: %q", text)
	}
	up.mu.Lock()
	revoked := len(up.revoked)
	up.mu.Unlock()
	if revoked == 0 {
		t.Fatal("disconnect did not revoke at the authorization server")
	}
	// An admin disconnects the boss through the API.
	g.api(http.MethodDelete, "/api/v1/servers/linear/connections/boss", g.owner, nil, http.StatusOK)
	if text, isErr := call(t, bossAgent, "linear__whoami", nil); !isErr || !strings.Contains(text, "/connect/linear") {
		t.Fatalf("boss after an admin disconnected him: %q", text)
	}

	g.eventually("connections and disconnections audited", 5*time.Second, func() bool {
		code, raw := g.raw(http.MethodGet, "/api/v1/audit/events?kind=admin", g.owner, nil)
		s := string(raw)
		return code == http.StatusOK && strings.Contains(s, "upstream.connect:linear") && strings.Contains(s, "upstream.disconnect:linear") &&
			strings.Contains(s, "invite.create:member:ada") && strings.Contains(s, "invite.claim") && strings.Contains(s, "server.oauth_register:linear:dynamic")
	})
	// No code or token reached the logs.
	logs := g.logs.String()
	up.mu.Lock()
	secrets := append([]string(nil), up.issued...)
	up.mu.Unlock()
	for _, s := range append(secrets, w.gh.secrets...) {
		if strings.Contains(logs, s) {
			t.Errorf("the gateway's logs contain a code or token (%d chars, starts %q)", len(s), s[:5])
		}
	}
}

func TestUpstreamOAuthPreRegisteredClient(t *testing.T) {
	w := newOAuthWorld(t, false)
	g, up := w.g, w.up
	if w.serverJS["registration"] != "admin" {
		t.Fatalf("server %v", w.serverJS)
	}
	// The client secret is never shown.
	code, raw := g.raw(http.MethodGet, "/api/v1/servers", g.owner, nil)
	if code != http.StatusOK || strings.Contains(string(raw), up.secret) {
		t.Fatalf("server list: %d %s", code, raw)
	}
	up.signInAs("boss@linear")
	connectAccount(t, w.boss, g, "linear")
	g.eventually("linear comes up", 10*time.Second, func() bool { return g.serverStatus("linear") == "up" })
	g.acceptPins("linear")
	if text, isErr := call(t, g.connect(w.bossTok), "linear__whoami", nil); isErr || text != "you are boss@linear" {
		t.Fatalf("boss: %q (error=%v)", text, isErr)
	}
	if strings.Contains(g.logs.String(), up.secret) {
		t.Error("the client secret reached the logs")
	}
}

func TestUpstreamOAuthDynamicRegistration(t *testing.T) {
	w := newOAuthWorld(t, true)
	if w.up.registered != 1 || w.serverJS["registration"] != "dynamic" {
		t.Fatalf("registered %d times; server %v", w.up.registered, w.serverJS)
	}
	// Refused configurations: oauth on stdio, static headers with oauth, a
	// server that needs no authorization.
	g := w.g
	for name, body := range map[string]map[string]any{
		"stdio":   {"alias": "s1", "transport": "stdio", "command": map[string]any{"path": "/bin/true", "sha256": strings.Repeat("0", 64)}, "oauth": map[string]any{}},
		"headers": {"alias": "s2", "transport": "http", "url": w.up.srv.URL + "/mcp", "headers": map[string]any{"X-Key": "v"}, "oauth": map[string]any{}},
		"no auth": {"alias": "s3", "transport": "http", "url": w.gh.srv.URL + "/api/user", "oauth": map[string]any{}},
	} {
		if code, raw := g.raw(http.MethodPost, "/api/v1/servers", g.owner, body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	// Members cannot add servers or invite.
	auditor := g.issueControl("aud", "auditor", "aud")
	if code, _ := g.raw(http.MethodPost, "/api/v1/invites", auditor, map[string]any{"person": "x"}); code != http.StatusForbidden {
		t.Errorf("auditor invite: %d", code)
	}
	if code, _ := g.raw(http.MethodPost, "/api/v1/servers", auditor, map[string]any{"alias": "s4", "transport": "http", "url": w.up.srv.URL + "/mcp", "oauth": map[string]any{}}); code != http.StatusForbidden {
		t.Errorf("auditor adds a server: %d", code)
	}
	member := g.issue("mem")
	if code, _ := g.raw(http.MethodPost, "/api/v1/invites", member, map[string]any{"person": "x"}); code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Errorf("member invite: %d", code)
	}
}
