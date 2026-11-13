//go:build acceptance

package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// F06 (ADR 0022): an AI client signs in to the gateway with OAuth. The test
// is the client: it starts from nothing but the gateway's address, finds
// everything else the way a client does, and the person answers in a browser.

var requestField = regexp.MustCompile(`name="request" value="([^"]+)"`)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func getJSON(t *testing.T, target string) (int, map[string]any, http.Header) {
	t.Helper()
	res, err := http.Get(target) //nolint:gosec // a test against a local server
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out, res.Header
}

func postForm(t *testing.T, target string, form url.Values) (int, map[string]any) {
	t.Helper()
	res, err := http.PostForm(target, form) //nolint:gosec // a test against a local server
	if err != nil {
		t.Fatalf("POST %s: %v", target, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// mcpStatus is the status and the WWW-Authenticate header of a bare request
// to /mcp with a bearer value.
func mcpStatus(t *testing.T, g *gateway, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, g.base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode, res.Header.Get("WWW-Authenticate")
}

func TestOAuthSignInEndToEnd(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	secretFile := writeTemp(t, webClientSecret+"\n")
	g := startGateway(t,
		"PRIVO_GITHUB_CLIENT_ID="+webClientID, "PRIVO_GITHUB_CLIENT_SECRET_FILE="+secretFile,
		"PRIVO_GITHUB_BASE_URL="+gh.srv.URL, "PRIVO_GITHUB_API_URL="+gh.srv.URL+"/api",
		"PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.0/8",
		"PRIVO_OAUTH_SERVER=on", "PRIVO_OAUTH_DYNAMIC_REGISTRATION=on")
	readyEcho(t, g)

	// The owner exists: setup link, GitHub.
	out, err := g.command("admin", "setup-link", "--person", "boss").Output()
	if err != nil {
		t.Fatalf("admin setup-link: %v", err)
	}
	boss := newPerson(t, g)
	_, body := boss.do(http.MethodGet, strings.TrimSpace(string(out)), nil, nil)
	gh.as = fakeGitHubUser{ID: 424242, Login: "BossHuman", Name: "The Boss"}
	start, _ := boss.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"setup_token": {setupTokenField.FindStringSubmatch(body)[1]}}, nil)
	if res, _ := boss.signInThroughGitHub(start); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: %d", res.StatusCode)
	}

	// 1. The client knows only the gateway. A call without a token tells it
	// where the metadata is and which scope to ask for.
	status, challenge := mcpStatus(t, g, "")
	prmURL := g.base + "/.well-known/oauth-protected-resource/mcp"
	if status != http.StatusUnauthorized || !strings.Contains(challenge, `resource_metadata="`+prmURL+`"`) || !strings.Contains(challenge, `scope="mcp"`) {
		t.Fatalf("no token: %d, WWW-Authenticate %q", status, challenge)
	}
	_, prm, _ := getJSON(t, prmURL)
	servers, _ := prm["authorization_servers"].([]any)
	if prm["resource"] != g.base+"/mcp" || len(servers) != 1 || servers[0] != g.base {
		t.Fatalf("protected resource metadata: %v", prm)
	}
	status, meta, _ := getJSON(t, g.base+"/.well-known/oauth-authorization-server")
	if status != http.StatusOK || meta["issuer"] != g.base || meta["client_id_metadata_document_supported"] != true {
		t.Fatalf("authorization server metadata: %d %v", status, meta)
	}
	authorizeEP, tokenEP, registerEP := meta["authorization_endpoint"].(string), meta["token_endpoint"].(string), meta["registration_endpoint"].(string)

	// 2. It registers itself (this test's client has no https address to
	// publish a metadata document at).
	const redirect = "http://127.0.0.1:45871/callback"
	reg, _ := json.Marshal(map[string]any{"client_name": "Acceptance Client", "redirect_uris": []string{"http://127.0.0.1/callback"}, "token_endpoint_auth_method": "none"})
	res, err := http.Post(registerEP, "application/json", bytes.NewReader(reg)) //nolint:gosec // a test against a local server
	if err != nil {
		t.Fatal(err)
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&registered)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated || registered.ClientID == "" {
		t.Fatalf("registration: %d", res.StatusCode)
	}

	// 3. It sends the person's browser to the authorization endpoint. The
	// person is signed in already and sees the consent page.
	verifier := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {redirect}, "scope": {"mcp"}, "state": {"state-from-the-client"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {g.base + "/mcp"}}
	page, body := boss.do(http.MethodGet, authorizeEP+"?"+q.Encode(), nil, nil)
	if page.StatusCode != http.StatusOK || !strings.Contains(body, "Acceptance Client") || !strings.Contains(body, "registered itself") || !strings.Contains(body, "127.0.0.1:45871") {
		t.Fatalf("consent page: %d\n%s", page.StatusCode, body)
	}
	// Someone who is not signed in is sent to sign in, and nothing is stored.
	if res, _ := newPerson(t, g).do(http.MethodGet, authorizeEP+"?"+q.Encode(), nil, nil); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/admin/signin") {
		t.Errorf("the authorization endpoint without a session: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	allowed, _ := boss.do(http.MethodPost, authorizeEP, url.Values{"csrf": {csrfField.FindStringSubmatch(body)[1]},
		"request": {requestField.FindStringSubmatch(body)[1]}, "decision": {"allow"}}, nil)
	back, err := url.Parse(allowed.Header.Get("Location"))
	if err != nil || back.Host != "127.0.0.1:45871" || back.Query().Get("state") != "state-from-the-client" || back.Query().Get("iss") != g.base || back.Query().Get("code") == "" {
		t.Fatalf("after Allow: %d %q", allowed.StatusCode, allowed.Header.Get("Location"))
	}

	// 4. The code becomes tokens.
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {redirect},
		"client_id": {registered.ClientID}, "code_verifier": {verifier}, "resource": {g.base + "/mcp"}}
	status, tok := postForm(t, tokenEP, exchange)
	access, _ := tok["access_token"].(string)
	refresh, _ := tok["refresh_token"].(string)
	if status != http.StatusOK || access == "" || refresh == "" {
		t.Fatalf("code exchange: %d %v", status, tok)
	}

	// 5. The gateway takes the access token: the person's tools, as the person.
	session := g.connect(access)
	if names := toolNames(t, session); len(names) == 0 {
		t.Fatal("no tools for a client that signed in with OAuth")
	}
	if got := callEcho(t, session, "over oauth"); !strings.Contains(got, "over oauth") {
		t.Errorf("echo over OAuth: %q", got)
	}
	// The admin API does not take it, although the person is the owner.
	if code, _ := g.raw(http.MethodGet, "/api/v1/tokens", access, nil); code != http.StatusUnauthorized {
		t.Errorf("the admin API with an OAuth access token: %d, want 401", code)
	}
	// The app shows the client to the person.
	code, mine := boss.app(http.MethodGet, "/v1/me/clients", nil, nil)
	list, _ := mine["clients"].([]any)
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("connected clients: %d %v", code, mine)
	}
	grantID, _ := list[0].(map[string]any)["id"].(string)
	if list[0].(map[string]any)["client"] != "Acceptance Client" {
		t.Errorf("the connected client: %v", list[0])
	}

	// 6. Refresh: new tokens; the old access token stops within the cache time.
	status, tok2 := postForm(t, tokenEP, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {registered.ClientID}})
	access2, _ := tok2["access_token"].(string)
	if status != http.StatusOK || access2 == "" || access2 == access {
		t.Fatalf("refresh: %d %v", status, tok2)
	}
	g.eventually("the old access token stops working", 5*time.Second, func() bool { s, _ := mcpStatus(t, g, access); return s == http.StatusUnauthorized })
	if s, _ := mcpStatus(t, g, access2); s == http.StatusUnauthorized {
		t.Fatal("the refreshed access token does not work")
	}
	// 7. The person takes the access away in the app.
	if code, out := boss.app(http.MethodDelete, "/v1/me/clients/"+grantID, nil, nil); code != http.StatusOK {
		t.Fatalf("revoking the client: %d %v", code, out)
	}
	g.eventually("the revoked client gets 401", 5*time.Second, func() bool {
		s, challenge := mcpStatus(t, g, access2)
		return s == http.StatusUnauthorized && strings.Contains(challenge, "resource_metadata=")
	})
	refresh2, _ := tok2["refresh_token"].(string)
	if status, out := postForm(t, tokenEP, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh2}, "client_id": {registered.ClientID}}); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("refresh after the revocation: %d %v", status, out)
	}

	// Nothing a client holds is on disk or in the log.
	for _, secret := range []string{access, access2, refresh, refresh2, back.Query().Get("code")} {
		g.assertNotOnDisk(secret)
		if strings.Contains(g.logs.String(), secret) {
			t.Errorf("the log holds a token or code: %.14s…", secret)
		}
	}
}

// Off unless it is switched on: the metadata names no authorization server,
// the endpoints answer 503, and a client learns nothing about OAuth.
func TestOAuthServerIsOffByDefault(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	_, prm, _ := getJSON(t, g.base+"/.well-known/oauth-protected-resource/mcp")
	if servers, _ := prm["authorization_servers"].([]any); len(servers) != 0 {
		t.Errorf("authorization servers with the server off: %v", servers)
	}
	if status, challenge := mcpStatus(t, g, ""); status != http.StatusUnauthorized || strings.Contains(challenge, "scope=") {
		t.Errorf("no token with the server off: %d %q", status, challenge)
	}
	if status, _ := postForm(t, g.base+"/oauth/token", url.Values{"grant_type": {"refresh_token"}}); status != http.StatusServiceUnavailable {
		t.Errorf("/oauth/token with the server off: %d, want 503", status)
	}
	if status, _, _ := getJSON(t, g.base+"/.well-known/oauth-authorization-server"); status != http.StatusNotFound {
		t.Errorf("authorization server metadata with the server off: %d, want 404", status)
	}
	if s, _ := mcpStatus(t, g, "privo_at_anything.anything"); s != http.StatusUnauthorized {
		t.Errorf("an access-token-shaped value with the server off: %d", s)
	}
}
