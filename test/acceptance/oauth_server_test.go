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
	"regexp"
	"strings"
	"testing"
	"time"
)

// F06 (ADR 0022): an AI client signs in to the gateway with OAuth. The test
// is the client: it starts from nothing but the gateway's address, finds
// everything else the way a client does, and the person answers in a browser.

var requestField = regexp.MustCompile(`name="request" value="([^"]+)"`)

func getJSON(t *testing.T, target string) (int, map[string]any) {
	t.Helper()
	res, err := http.Get(target) //nolint:gosec // a test against a local server
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
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

// The whole way, on the real binary: the client knows only the gateway's
// address; the member allows it; it calls a server as the member's own
// upstream account; a rotated refresh token used again ends its access; and
// the member takes the access of a second sign-in away in the app.
//
// The client registers itself. A client with a metadata document would need
// an https address the binary trusts, which this suite does not have; that
// path is covered with real fetches in internal/oauthserver.
func TestClientSignsInWithOAuthAndCallsAsTheMember(t *testing.T) {
	w := newOAuthWorld(t, true, "PRIVO_OAUTH_SERVER=on", "PRIVO_OAUTH_DYNAMIC_REGISTRATION=on")
	g, up, boss := w.g, w.up, w.boss
	up.signInAs("boss@linear")
	if back := connectAccount(t, boss, g, "linear"); back.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("connecting the boss's upstream account: %q", back.Header.Get("Location"))
	}
	g.eventually("linear comes up", 10*time.Second, func() bool { return g.serverStatus("linear") == "up" })
	g.acceptPins("linear")

	// 1. Discovery from nothing but the gateway's address.
	status, challenge := mcpStatus(t, g, "")
	prmURL := g.base + "/.well-known/oauth-protected-resource/mcp"
	if status != http.StatusUnauthorized || !strings.Contains(challenge, `resource_metadata="`+prmURL+`"`) || !strings.Contains(challenge, `scope="mcp"`) {
		t.Fatalf("no token: %d, WWW-Authenticate %q", status, challenge)
	}
	_, prm := getJSON(t, prmURL)
	servers, _ := prm["authorization_servers"].([]any)
	if prm["resource"] != g.base+"/mcp" || len(servers) != 1 || servers[0] != g.base {
		t.Fatalf("protected resource metadata: %v", prm)
	}
	status, meta := getJSON(t, g.base+"/.well-known/oauth-authorization-server")
	if status != http.StatusOK || meta["issuer"] != g.base || meta["client_id_metadata_document_supported"] != true {
		t.Fatalf("authorization server metadata: %d %v", status, meta)
	}
	authorizeEP, tokenEP, registerEP := meta["authorization_endpoint"].(string), meta["token_endpoint"].(string), meta["registration_endpoint"].(string)

	// 2. The client registers itself.
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

	// signIn is what the client and the member do together: the browser goes
	// to the authorization endpoint, the member allows, the code becomes
	// tokens. The boss is signed in to Privo already.
	var codes []string
	signIn := func() (access, refresh string) {
		t.Helper()
		verifier := strings.Repeat("a", 43) + randomHex(8)
		sum := sha256.Sum256([]byte(verifier))
		q := url.Values{"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {redirect}, "scope": {"mcp"}, "state": {"state-from-the-client"},
			"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {g.base + "/mcp"}}
		page, body := boss.do(http.MethodGet, authorizeEP+"?"+q.Encode(), nil, nil)
		if page.StatusCode != http.StatusOK || !strings.Contains(body, "Acceptance Client") || !strings.Contains(body, "registered itself") || !strings.Contains(body, "127.0.0.1:45871") {
			t.Fatalf("consent page: %d\n%s", page.StatusCode, body)
		}
		allowed, _ := boss.do(http.MethodPost, authorizeEP, url.Values{"csrf": {csrfField.FindStringSubmatch(body)[1]},
			"request": {requestField.FindStringSubmatch(body)[1]}, "decision": {"allow"}}, nil)
		back, err := url.Parse(allowed.Header.Get("Location"))
		if err != nil || back.Host != "127.0.0.1:45871" || back.Query().Get("state") != "state-from-the-client" || back.Query().Get("iss") != g.base || back.Query().Get("code") == "" {
			t.Fatalf("after Allow: %d %q", allowed.StatusCode, allowed.Header.Get("Location"))
		}
		codes = append(codes, back.Query().Get("code"))
		status, tok := postForm(t, tokenEP, url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {redirect},
			"client_id": {registered.ClientID}, "code_verifier": {verifier}, "resource": {g.base + "/mcp"}})
		access, _ = tok["access_token"].(string)
		refresh, _ = tok["refresh_token"].(string)
		if status != http.StatusOK || access == "" || refresh == "" {
			t.Fatalf("code exchange: %d %v", status, tok)
		}
		return access, refresh
	}

	// Someone who is not signed in is sent to sign in first.
	anon := newPerson(t, g)
	if res, _ := anon.do(http.MethodGet, authorizeEP+"?"+url.Values{"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {redirect},
		"state": {"state-from-the-client"}, "code_challenge": {strings.Repeat("c", 43)}, "code_challenge_method": {"S256"}, "resource": {g.base + "/mcp"}}.Encode(), nil, nil); res.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(res.Header.Get("Location"), "/admin/signin") {
		t.Errorf("the authorization endpoint without a session: %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	// 3. Signed in: the client calls the upstream as the boss's own account.
	access, refresh := signIn()
	session := g.connect(access)
	if text, isErr := call(t, session, "linear__whoami", nil); isErr || text != "you are boss@linear" {
		t.Fatalf("a call over OAuth: %q (error=%v), want the boss's own upstream account", text, isErr)
	}
	// The admin API does not take the access token, although the boss is the owner.
	if code, _ := g.raw(http.MethodGet, "/api/v1/tokens", access, nil); code != http.StatusUnauthorized {
		t.Errorf("the admin API with an OAuth access token: %d, want 401", code)
	}
	code, mine := boss.app(http.MethodGet, "/v1/me/clients", nil, nil)
	if list, _ := mine["clients"].([]any); code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["client"] != "Acceptance Client" {
		t.Fatalf("connected clients: %d %v", code, mine)
	}

	// 4. Refresh: new tokens; the old access token stops within the cache time.
	status, tok2 := postForm(t, tokenEP, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {registered.ClientID}})
	access2, _ := tok2["access_token"].(string)
	refresh2, _ := tok2["refresh_token"].(string)
	if status != http.StatusOK || access2 == "" || access2 == access || refresh2 == refresh {
		t.Fatalf("refresh: %d %v", status, tok2)
	}
	g.eventually("the old access token stops working", 5*time.Second, func() bool { s, _ := mcpStatus(t, g, access); return s == http.StatusUnauthorized })
	if s, _ := mcpStatus(t, g, access2); s == http.StatusUnauthorized {
		t.Fatal("the refreshed access token does not work")
	}

	// 5. No grace (ADR 0022 point 9): the rotated refresh token used again is
	// refused, and the tokens issued after it stop on the gateway.
	if status, out := postForm(t, tokenEP, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {registered.ClientID}}); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("a rotated refresh token used again: %d %v", status, out)
	}
	g.eventually("the access token issued after the reused one stops", 5*time.Second, func() bool { s, _ := mcpStatus(t, g, access2); return s == http.StatusUnauthorized })
	if status, out := postForm(t, tokenEP, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh2}, "client_id": {registered.ClientID}}); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("the successor refresh token: %d %v", status, out)
	}
	g.eventually("the reuse is in the audit, naming the client and the member", 5*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=100&kind=admin", nil, nil)
		events, _ := out["events"].([]any)
		for _, e := range events {
			ev, _ := e.(map[string]any)
			if ev["action"] == "oauth.grant.reuse" && ev["client"] == "Acceptance Client" && ev["user"] == "boss" {
				return true
			}
		}
		return false
	})

	// 6. Signed in again; the member takes the access away in the app.
	access3, refresh3 := signIn()
	if s, _ := mcpStatus(t, g, access3); s == http.StatusUnauthorized {
		t.Fatal("the second sign-in's access token does not work")
	}
	_, mine = boss.app(http.MethodGet, "/v1/me/clients", nil, nil)
	list, _ := mine["clients"].([]any)
	if len(list) != 1 {
		t.Fatalf("connected clients after signing in again: %v", mine)
	}
	if code, out := boss.app(http.MethodDelete, "/v1/me/clients/"+list[0].(map[string]any)["id"].(string), nil, nil); code != http.StatusOK {
		t.Fatalf("taking the access away: %d %v", code, out)
	}
	g.eventually("the revoked client gets 401 with the metadata address", 5*time.Second, func() bool {
		s, challenge := mcpStatus(t, g, access3)
		return s == http.StatusUnauthorized && strings.Contains(challenge, "resource_metadata=")
	})
	if status, out := postForm(t, tokenEP, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh3}, "client_id": {registered.ClientID}}); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("refresh after the access was taken away: %d %v", status, out)
	}

	// Nothing a client holds is on disk or in the log.
	for _, secret := range append([]string{access, access2, access3, refresh, refresh2, refresh3}, codes...) {
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
	_, prm := getJSON(t, g.base+"/.well-known/oauth-protected-resource/mcp")
	if servers, _ := prm["authorization_servers"].([]any); len(servers) != 0 {
		t.Errorf("authorization servers with the server off: %v", servers)
	}
	if status, challenge := mcpStatus(t, g, ""); status != http.StatusUnauthorized || strings.Contains(challenge, "scope=") {
		t.Errorf("no token with the server off: %d %q", status, challenge)
	}
	if status, _ := postForm(t, g.base+"/oauth/token", url.Values{"grant_type": {"refresh_token"}}); status != http.StatusServiceUnavailable {
		t.Errorf("/oauth/token with the server off: %d, want 503", status)
	}
	if status, _ := getJSON(t, g.base+"/.well-known/oauth-authorization-server"); status != http.StatusNotFound {
		t.Errorf("authorization server metadata with the server off: %d, want 404", status)
	}
	if s, _ := mcpStatus(t, g, "privo_at_anything.anything"); s != http.StatusUnauthorized {
		t.Errorf("an access-token-shaped value with the server off: %d", s)
	}
}
