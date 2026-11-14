package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"privo.club/privo/internal/store"
)

// ADR 0023: the connect page of a server that takes each person's own key.

func addKeyServer(t *testing.T, e *env) store.Server {
	t.Helper()
	srv := store.Server{OrgID: e.org, ID: "srv-acme", Alias: "acme", Transport: "http", Enabled: true, CreatedAt: e.clock.now(),
		Config: store.ServerConfig{URL: "https://mcp.acme.example/mcp", Auth: store.AuthKey,
			Key: &store.ServerKey{Header: "X-Api-Key", Help: "Settings, then <b>API keys</b>.", HelpURL: "https://acme.example/keys"}}}
	if err := e.st.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestKeyPageSavesThePersonsOwnKey(t *testing.T) {
	e := newConnEnv(t)
	srv := addKeyServer(t, e)
	b, u := e.signedIn(ada, "ada", store.RoleMember)

	page := b.get("/connect/acme")
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Body, `type="password" name="key"`) || !strings.Contains(page.Body, "mcp.acme.example") ||
		!strings.Contains(page.Body, "Settings, then &lt;b&gt;API keys&lt;/b&gt;.") || !strings.Contains(page.Body, `href="https://acme.example/keys"`) {
		t.Fatalf("key page: %d\n%s", page.StatusCode, page.Body)
	}
	csrf := csrfOf(t, page.Body)
	if res := b.post("/connect/acme/key", url.Values{"key": {"k-0123456789"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("no CSRF token: %d", res.StatusCode)
	}
	if res := b.do(http.MethodPost, "/connect/acme/key", url.Values{"csrf": {csrf}, "key": {"k-0123456789"}}, map[string]string{"Origin": "https://evil.example"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("another site's form: %d", res.StatusCode)
	}
	for _, bad := range []string{"", "with space", "line\nbreak", strings.Repeat("k", 4097)} {
		if res := b.post("/connect/acme/key", url.Values{"csrf": {csrf}, "key": {bad}}); res.StatusCode != http.StatusBadRequest {
			t.Errorf("key %q: %d", bad, res.StatusCode)
		}
	}
	if _, err := e.st.Grant(context.Background(), e.org, srv.ID, u.ID); err == nil {
		t.Fatal("a refused key was saved")
	}
	res := b.post("/connect/acme/key", url.Values{"csrf": {csrf}, "key": {"  k-0123456789  "}})
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/connections?result=connected" {
		t.Fatalf("saving: %d %q %s", res.StatusCode, location(res), res.Body)
	}
	g, err := e.st.Grant(context.Background(), e.org, srv.ID, u.ID)
	if err != nil || g.Credential.Purpose != store.PurposeKeyGrant || strings.Contains(string(g.Credential.Ciphertext), "k-0123456789") {
		t.Fatalf("stored: %+v, %v", g.Credential.Purpose, err)
	}
	// The page then says a key is saved, and never shows it.
	again := b.get("/connect/acme")
	if !strings.Contains(again.Body, "A key of yours is saved") || strings.Contains(again.Body, "k-0123456789") {
		t.Errorf("the page after saving:\n%s", again.Body)
	}
	// Her connections list it as a key, connected.
	list := b.call(http.MethodGet, "/admin/api/me/connections", b.csrf(), "", nil)
	var out struct {
		Connections []myConnection `json:"connections"`
	}
	_ = json.Unmarshal([]byte(list.Body), &out)
	found := false
	for _, c := range out.Connections {
		if c.Alias == "acme" && c.Kind == store.AuthKey && c.Connected && c.Issuer == "mcp.acme.example" {
			found = true
		}
	}
	if !found {
		t.Errorf("connections: %s", list.Body)
	}
	if strings.Contains(e.logs.String(), "k-0123456789") {
		t.Error("the log holds the key")
	}
}

func TestKeyAndSignInPathsDoNotCross(t *testing.T) {
	e := newConnEnv(t)
	addKeyServer(t, e)
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	csrf := csrfOf(t, b.get("/connect/acme").Body)
	// A key for a server that uses sign-in, and a sign-in for one that takes a key.
	if res := b.post("/connect/linear/key", url.Values{"csrf": {csrf}, "key": {"k-1"}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("a key for an OAuth server: %d", res.StatusCode)
	}
	if res := b.post("/connect/acme/start", url.Values{"csrf": {csrf}}); res.StatusCode != http.StatusNotFound {
		t.Errorf("a sign-in for a key server: %d", res.StatusCode)
	}
	// A role that does not use tools has no key to enter.
	app, _ := e.signedIn(ghUser{ID: 7007, Login: "ann"}, "ann", store.RoleApprover)
	if res := app.get("/connect/acme"); res.StatusCode != http.StatusForbidden {
		t.Errorf("an approver on the key page: %d", res.StatusCode)
	}
	// Signed out: sent to sign in and back.
	if res := e.browser().get("/connect/acme"); res.StatusCode != http.StatusSeeOther || !strings.Contains(location(res), "next=%2Fconnect%2Facme") {
		t.Errorf("signed out: %d %q", res.StatusCode, location(res))
	}
}
