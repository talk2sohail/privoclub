//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
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

type scopeToken struct {
	subject string
	scopes  []string
}

type scopeCode struct {
	challenge string
	redirect  string
	resource  string
	subject   string
	scope     string
}

type scopeStepUpUpstream struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	clientID        string
	as              string
	codes           map[string]scopeCode
	access          map[string]scopeToken
	refresh         map[string]scopeToken
	refreshes       int
	authorizeScopes []string
	malformedScope  string
	issued          []string
}

func newScopeStepUpUpstream(t *testing.T) *scopeStepUpUpstream {
	t.Helper()
	u := &scopeStepUpUpstream{
		t:       t,
		codes:   map[string]scopeCode{},
		access:  map[string]scopeToken{},
		refresh: map[string]scopeToken{},
	}
	servers := map[string]*mcp.Server{}
	serverFor := func(subject string) *mcp.Server {
		u.mu.Lock()
		defer u.mu.Unlock()
		if s, ok := servers[subject]; ok {
			return s
		}
		s := mcp.NewServer(&mcp.Implementation{Name: "linear-scope-fixture", Version: "0"}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: "read_info", Description: "Read data (needs read scope)."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "read ok for " + subject}}}, nil, nil
			})
		mcp.AddTool(s, &mcp.Tool{Name: "write_item", Description: "Write data (needs write scope)."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "write ok for " + subject}}}, nil, nil
			})
		servers[subject] = s
		return s
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		tok, _ := u.tokenOf(r)
		return serverFor(tok.subject)
	}, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		tok, ok := u.tokenOf(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+u.srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if bytes.Contains(body, []byte(`"name":"write_item"`)) && !slices.Contains(tok.scopes, "write") {
			u.mu.Lock()
			mal := u.malformedScope
			u.mu.Unlock()
			if mal != "" {
				w.Header().Set("WWW-Authenticate", mal)
			} else {
				w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="write"`)
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "RAW_UPSTREAM_FORBIDDEN_BODY")
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              u.srv.URL + "/mcp",
			"authorization_servers": []string{u.srv.URL},
			"scopes_supported":      []string{"read", "write"},
		})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           u.srv.URL,
			"authorization_endpoint":           u.srv.URL + "/authorize",
			"token_endpoint":                   u.srv.URL + "/token",
			"registration_endpoint":            u.srv.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
			"authorization_response_iss_parameter_supported": true,
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.clientID = "dyn-scope-1"
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
		sc := q.Get("scope")
		u.authorizeScopes = append(u.authorizeScopes, sc)
		code := "code-" + randomHex(10)
		u.issued = append(u.issued, code)
		u.codes[code] = scopeCode{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), resource: q.Get("resource"), subject: u.as, scope: sc}
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
		if r.PostForm.Get("client_id") != u.clientID || r.PostForm.Get("resource") != u.srv.URL+"/mcp" {
			fail()
			return
		}
		var tok scopeToken
		var scopeStr string
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			c, ok := u.codes[r.PostForm.Get("code")]
			delete(u.codes, r.PostForm.Get("code"))
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if !ok || c.redirect != r.PostForm.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
				fail()
				return
			}
			scopeStr = c.scope
			tok = scopeToken{subject: c.subject, scopes: strings.Fields(c.scope)}
		case "refresh_token":
			prev, ok := u.refresh[r.PostForm.Get("refresh_token")]
			delete(u.refresh, r.PostForm.Get("refresh_token"))
			if !ok {
				fail()
				return
			}
			u.refreshes++
			tok = prev
			scopeStr = strings.Join(prev.scopes, " ")
		default:
			fail()
			return
		}
		at, rt := "at-"+randomHex(12), "rt-"+randomHex(12)
		u.access[at], u.refresh[rt] = tok, tok
		u.issued = append(u.issued, at, rt)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": at, "token_type": "Bearer", "refresh_token": rt, "expires_in": 3600, "scope": scopeStr})
	})

	u.srv = httptest.NewServer(mux)
	t.Cleanup(u.srv.Close)
	return u
}

func (u *scopeStepUpUpstream) tokenOf(r *http.Request) (scopeToken, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	tok, ok := u.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	return tok, ok
}

func (u *scopeStepUpUpstream) signInAs(subject string) {
	u.mu.Lock()
	u.as = subject
	u.mu.Unlock()
}

var scopeFieldRe = regexp.MustCompile(`name="scope" value="([^"]+)"`)

func connectWithPath(t *testing.T, p *person, g *gateway, path string) *http.Response {
	t.Helper()
	page, body := p.do(http.MethodGet, g.base+path, nil, nil)
	m := csrfField.FindStringSubmatch(body)
	if page.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("connect page %s: %d\n%s", path, page.StatusCode, body)
	}
	form := url.Values{"csrf": {m[1]}}
	if sm := scopeFieldRe.FindStringSubmatch(body); sm != nil {
		form.Set("scope", sm[1])
	}
	alias := strings.TrimPrefix(strings.SplitN(path, "?", 2)[0], "/connect/")
	start, body := p.do(http.MethodPost, g.base+"/connect/"+alias+"/start", form, nil)
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

func TestScopeStepUpEndToEnd(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	up := newScopeStepUpUpstream(t)

	g.api(http.MethodPost, "/api/v1/servers", g.owner, map[string]any{
		"alias":     "linear",
		"transport": "http",
		"url":       up.srv.URL + "/mcp",
		"oauth":     map[string]any{"scopes": []string{"read", "write"}},
	}, http.StatusCreated)

	up.signInAs("boss@linear")
	if back := connectWithPath(t, boss, g, "/connect/linear"); back.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("boss connect: %q", back.Header.Get("Location"))
	}
	g.eventually("linear comes up", 10*time.Second, func() bool { return g.serverStatus("linear") == "up" })
	g.acceptPins("linear")

	adaWeb, adaTok := join(t, g, gh, boss, "ada", 2)
	// Ada initially connects with only "read" scope.
	up.signInAs("ada@linear")
	if back := connectWithPath(t, adaWeb, g, "/connect/linear?scope=read"); back.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("ada initial read connect: %q", back.Header.Get("Location"))
	}

	adaAgent := g.connect(adaTok)
	g.eventually("ada sees tools", 5*time.Second, func() bool {
		tools := toolList(t, adaAgent)
		return slices.Contains(tools, "linear__read_info") && slices.Contains(tools, "linear__write_item")
	})

	if text, isErr := call(t, adaAgent, "linear__read_info", nil); isErr || text != "read ok for ada@linear" {
		t.Fatalf("read_info before step-up: %q (error=%v)", text, isErr)
	}

	// write_item fails with 403 insufficient_scope scope="write": error names server, scope, and step-up link.
	text, isErr := call(t, adaAgent, "linear__write_item", nil)
	wantLink := g.base + "/connect/linear?scope=write"
	if !isErr || !strings.Contains(text, "linear") || !strings.Contains(text, "write") || !strings.Contains(text, wantLink) {
		t.Fatalf("write_item insufficient_scope = %q (error=%v), want link %q", text, isErr, wantLink)
	}
	if strings.Contains(text, "RAW_UPSTREAM_FORBIDDEN_BODY") {
		t.Fatalf("raw 403 body leaked: %q", text)
	}

	// Existing grant is kept (read_info still succeeds and no refresh happened).
	if text, isErr := call(t, adaAgent, "linear__read_info", nil); isErr || text != "read ok for ada@linear" {
		t.Fatalf("read_info after insufficient_scope: %q (error=%v)", text, isErr)
	}
	up.mu.Lock()
	refreshes := up.refreshes
	up.mu.Unlock()
	if refreshes != 0 {
		t.Fatalf("refreshes = %d, want 0", refreshes)
	}

	// Audited as upstream.scope_needed:linear:write and recorded in call telemetry.
	g.eventually("scope_needed audited", 5*time.Second, func() bool {
		events := list(g.api(http.MethodGet, "/api/v1/audit/events?kind=admin&limit=50", g.owner, nil, http.StatusOK), "events")
		for _, ev := range events {
			if ev["action"] == "upstream.scope_needed:linear:write" && ev["server"] == "linear" && ev["user"] == "ada" {
				return true
			}
		}
		return false
	})
	g.eventually("insufficient_scope call telemetry", 10*time.Second, func() bool {
		calls := list(g.api(http.MethodGet, "/api/v1/telemetry/calls?limit=20", g.owner, nil, http.StatusOK), "calls")
		for _, c := range calls {
			if c["server"] == "linear" && c["item"] == "write_item" && c["outcome"] == "error" && c["error_class"] == "insufficient_scope" {
				return true
			}
		}
		return false
	})

	// Out-of-policy scope is refused with 400.
	if badResp, _ := adaWeb.do(http.MethodGet, g.base+"/connect/linear?scope=admin", nil, nil); badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("scope outside server list: %d, want 400", badResp.StatusCode)
	}

	// Unauthenticated click of the returned ?scope=write link signs in and lands back on /connect/linear?scope=write.
	fresh := newPerson(t, g)
	redir, _ := fresh.do(http.MethodGet, wantLink, nil, nil)
	if redir.StatusCode != http.StatusSeeOther || !strings.HasPrefix(redir.Header.Get("Location"), "/admin/signin?next=") {
		t.Fatalf("anonymous step-up redirect: %d %q", redir.StatusCode, redir.Header.Get("Location"))
	}
	signinResp, signinBody := fresh.follow("step-up signin", redir)
	if signinResp.StatusCode != http.StatusOK || !strings.Contains(signinBody, `value="/connect/linear?scope=write"`) {
		t.Fatalf("signin page: %d\n%s", signinResp.StatusCode, signinBody)
	}
	gh.as = fakeGitHubUser{ID: 2, Login: "ada-gh", Name: "Ada"}
	ghStart, _ := fresh.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"return_to": {"/connect/linear?scope=write"}}, nil)
	ghCB, _ := fresh.signInThroughGitHub(ghStart)
	if ghCB.StatusCode != http.StatusSeeOther || ghCB.Header.Get("Location") != "/connect/linear?scope=write" {
		t.Fatalf("post-signin redirect = %d %q", ghCB.StatusCode, ghCB.Header.Get("Location"))
	}

	// Connect flow asks for the union of granted ("read") + requested ("write") = "read write".
	page, body := fresh.do(http.MethodGet, wantLink, nil, nil)
	if page.StatusCode != http.StatusOK || !strings.Contains(body, "<code>read write</code>") {
		t.Fatalf("step-up connect page: %d\n%s", page.StatusCode, body)
	}
	up.signInAs("ada@linear")
	if back := connectWithPath(t, fresh, g, "/connect/linear?scope=write"); back.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("step-up connect callback: %q", back.Header.Get("Location"))
	}
	up.mu.Lock()
	lastAuthScope := up.authorizeScopes[len(up.authorizeScopes)-1]
	up.mu.Unlock()
	if lastAuthScope != "read write" {
		t.Fatalf("AS /authorize received scope %q, want %q", lastAuthScope, "read write")
	}

	// write_item and read_info now both succeed.
	if text, isErr := call(t, adaAgent, "linear__write_item", nil); isErr || text != "write ok for ada@linear" {
		t.Fatalf("write_item after step-up: %q (error=%v)", text, isErr)
	}
	if text, isErr := call(t, adaAgent, "linear__read_info", nil); isErr || text != "read ok for ada@linear" {
		t.Fatalf("read_info after step-up: %q (error=%v)", text, isErr)
	}

	// Malformed 403 challenge for a user without write scope never reflects header text into MCP reply, audit, or logs.
	cyWeb, cyTok := join(t, g, gh, boss, "cy", 3)
	up.signInAs("cy@linear")
	if back := connectWithPath(t, cyWeb, g, "/connect/linear?scope=read"); back.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("cy connect for malformed check: %q", back.Header.Get("Location"))
	}
	up.mu.Lock()
	up.malformedScope = "Bearer error=\"insufficient_scope\", scope=\"bad\tMALFORMED_LEAK\""
	up.mu.Unlock()
	cyAgent := g.connect(cyTok)
	malText, malErr := call(t, cyAgent, "linear__write_item", nil)
	if !malErr || strings.Contains(malText, "MALFORMED_LEAK") {
		t.Fatalf("malformed 403 reply leaked or did not fail: %q (err=%v)", malText, malErr)
	}
	logs := g.logs.String()
	if strings.Contains(logs, "MALFORMED_LEAK") {
		t.Fatalf("malformed scope leaked to logs")
	}
	up.mu.Lock()
	secrets := append([]string(nil), up.issued...)
	up.mu.Unlock()
	for _, s := range append(secrets, gh.secrets...) {
		if strings.Contains(logs, s) {
			t.Errorf("gateway log contains code or token (%d chars)", len(s))
		}
	}
}
