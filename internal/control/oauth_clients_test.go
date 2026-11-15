package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/oauthserver"
	"privo.club/privo/internal/store"
)

// The admin API of Privo's own authorization server (F06, ADR 0022).

// withAuthServer switches the authorization server on for an environment and
// returns the address of a site that serves client metadata documents.
func withAuthServer(t *testing.T, e *sessionEnv) (docs *httptest.Server) {
	t.Helper()
	docs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := "http://" + r.Host + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tool.json":
			_, _ = io.WriteString(w, `{"client_id":"`+id+`","client_name":"Tool","redirect_uris":["https://tool.example/cb"]}`)
		case "/secret.json":
			_, _ = io.WriteString(w, `{"client_id":"`+id+`","redirect_uris":["https://tool.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(docs.Close)
	clients := &oauthserver.Clients{Org: e.org, Store: e.st, Policy: oauthserver.PolicyKnown, HTTP: docs.Client(), AllowHTTP: true,
		Known: []oauthserver.KnownClient{{URL: "https://known.example/client.json", Name: "Known"}}}
	as, err := oauthserver.New(context.Background(), oauthserver.Options{Org: e.org, Store: e.st, Keys: e.api.Keys, Audit: e.aw, Clients: clients, Issuer: "https://privo.example"})
	if err != nil {
		t.Fatal(err)
	}
	e.api.AuthServer, e.api.Clients = as, clients
	return docs
}

// connected gives a member a live client grant.
func connected(t *testing.T, e *sessionEnv, id, userID, clientID, name string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := e.st.CreateClientGrant(ctx, e.org, store.ClientGrant{ID: id, UserID: userID, ClientID: clientID, ClientName: name, ClientKind: store.ClientKnown,
		Scope: "mcp", CreatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for kind, ttl := range map[string]time.Duration{store.OAuthAccess: time.Hour, store.OAuthRefresh: 30 * 24 * time.Hour} {
		if err := e.st.PutOAuthToken(ctx, e.org, store.OAuthToken{Kind: kind, Signature: kind + "-" + id, GrantID: id, Request: []byte(`{}`), ExpiresAt: now.Add(ttl)}, now); err != nil {
			t.Fatal(err)
		}
	}
}

func clientIDs(res map[string]any) string {
	var ids []string
	list, _ := res["clients"].([]any)
	for _, c := range list {
		m, _ := c.(map[string]any)
		id, _ := m["id"].(string)
		if id == "" {
			id, _ = m["client_id"].(string)
		}
		ids = append(ids, id)
	}
	return strings.Join(ids, ",")
}

func TestAMemberSeesAndRevokesOnlyTheirOwnClients(t *testing.T) {
	e := newSessionEnv(t)
	withAuthServer(t, e)
	ada, adaP := e.user(t, "ada", store.RoleMember)
	bob, bobP := e.user(t, "bob", store.RoleMember)
	connected(t, e, "ga", ada.ID, "https://known.example/client.json", "Known")
	connected(t, e, "gb", bob.ID, "https://known.example/client.json", "Known")

	res := e.call(t, adaP, http.MethodGet, "/api/v1/me/clients", nil, http.StatusOK)
	if clientIDs(res) != "ga" || res["enabled"] != true {
		t.Fatalf("ada's clients: %v", res)
	}
	first := res["clients"].([]any)[0].(map[string]any)
	if first["client"] != "Known" || first["person"] != nil {
		t.Errorf("a member's own client: %v", first)
	}
	// Another member's client is not found, and stays.
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/clients/gb", nil, http.StatusNotFound)
	if got := clientIDs(e.call(t, bobP, http.MethodGet, "/api/v1/me/clients", nil, http.StatusOK)); got != "gb" {
		t.Errorf("bob's clients after ada's attempt: %q", got)
	}
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/clients/ga", nil, http.StatusOK)
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/clients/ga", nil, http.StatusNotFound)
	if _, _, err := e.st.OAuthTokenBySignature(context.Background(), e.org, store.OAuthAccess, "access-ga"); err == nil {
		t.Error("the revoked client's access token is still there")
	}
	// A member has no business with everyone's clients or with the client rules.
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/api/v1/oauth/grants"}, {http.MethodDelete, "/api/v1/oauth/grants/gb"},
		{http.MethodGet, "/api/v1/oauth/clients"}, {http.MethodPost, "/api/v1/oauth/clients"}, {http.MethodPost, "/api/v1/oauth/clients:remove"}} {
		if code, raw := e.api.Serve(context.Background(), adaP, tc.method, tc.path, map[string]any{"client_id": "x"}); code != http.StatusForbidden {
			t.Errorf("a member on %s %s: %d %s", tc.method, tc.path, code, raw)
		}
	}
	// A token is not a person: it has no clients of its own, even a token of
	// the member.
	tokenP := auth.Principal{OrgID: e.org, TokenID: "tok", Name: "script", Role: store.RoleMember, Kind: store.KindControl}
	rec := httptest.NewRecorder()
	if err := e.api.listMyClients(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me/clients", nil), tokenP); err != nil || strings.Contains(rec.Body.String(), "gb") {
		t.Errorf("a token's clients: %v %s", err, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/clients/gb", nil)
	req.SetPathValue("id", "gb")
	if err := e.api.revokeMyClient(httptest.NewRecorder(), req, tokenP); err == nil {
		t.Error("a token revoked a member's client")
	}
	if got := clientIDs(e.call(t, bobP, http.MethodGet, "/api/v1/me/clients", nil, http.StatusOK)); got != "gb" {
		t.Errorf("bob's clients at the end: %q", got)
	}
}

func TestAnAdministratorSeesAndRevokesEveryonesClients(t *testing.T) {
	e := newSessionEnv(t)
	withAuthServer(t, e)
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, _ := e.user(t, "ada", store.RoleMember)
	connected(t, e, "ga", ada.ID, "https://known.example/client.json", "Known")
	res := e.call(t, owner, http.MethodGet, "/api/v1/oauth/grants", nil, http.StatusOK)
	first, _ := res["clients"].([]any)[0].(map[string]any)
	if clientIDs(res) != "ga" || first["person"] != "ada" {
		t.Fatalf("everyone's clients: %v", res)
	}
	e.call(t, owner, http.MethodDelete, "/api/v1/oauth/grants/ga", nil, http.StatusOK)
	e.call(t, owner, http.MethodDelete, "/api/v1/oauth/grants/ga", nil, http.StatusNotFound)
	if got := clientIDs(e.call(t, owner, http.MethodGet, "/api/v1/oauth/grants", nil, http.StatusOK)); got != "" {
		t.Errorf("after the revocation: %q", got)
	}
	e.aw.Close()
	events, err := e.st.RecentAdminEvents(context.Background(), e.org, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		// That the row names the client and the member is the store's test.
		if ev.Action == "oauth.grant.revoke:ga" {
			found = true
		}
	}
	if !found {
		t.Error("the revocation is not in the audit")
	}
}

func TestApprovingAndRemovingAClient(t *testing.T) {
	e := newSessionEnv(t)
	docs := withAuthServer(t, e)
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, _ := e.user(t, "ada", store.RoleMember)
	tool := docs.URL + "/tool.json"

	res := e.call(t, owner, http.MethodGet, "/api/v1/oauth/clients", nil, http.StatusOK)
	if res["enabled"] != true || res["policy"] != "known" || res["registration"] != false || clientIDs(res) != "https://known.example/client.json" {
		t.Fatalf("the client rules at the start: %v", res)
	}
	// A document that cannot be used is not approved, and the answer says why.
	for name, id := range map[string]string{"a secret client": docs.URL + "/secret.json", "no document": docs.URL + "/missing.json", "not a URL": "tool", "empty": ""} {
		code, raw := e.api.Serve(context.Background(), owner, http.MethodPost, "/api/v1/oauth/clients", map[string]any{"client_id": id})
		if code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	got := e.call(t, owner, http.MethodPost, "/api/v1/oauth/clients", map[string]any{"client_id": tool}, http.StatusCreated)
	if got["name"] != "Tool" || got["kind"] != "approved" {
		t.Fatalf("approved: %v", got)
	}
	if uris, _ := json.Marshal(got["redirect_uris"]); string(uris) != `["https://tool.example/cb"]` {
		t.Errorf("the answer does not show where the client sends people: %s", uris)
	}
	e.call(t, owner, http.MethodPost, "/api/v1/oauth/clients", map[string]any{"client_id": tool}, http.StatusConflict)
	if c, err := e.api.Clients.Accepted(context.Background(), tool); err != nil || c.Kind != store.ClientApproved {
		t.Errorf("the approved client is not accepted: %+v, %v", c, err)
	}

	// Removing it ends its grants; a known client only loses its grants.
	connected(t, e, "g-tool", ada.ID, tool, "Tool")
	connected(t, e, "g-known", ada.ID, "https://known.example/client.json", "Known")
	out := e.call(t, owner, http.MethodPost, "/api/v1/oauth/clients:remove", map[string]any{"client_id": tool}, http.StatusOK)
	if out["removed"] != true || out["ended"] != float64(1) {
		t.Errorf("removing the approved client: %v", out)
	}
	if _, err := e.api.Clients.Accepted(context.Background(), tool); err == nil {
		t.Error("the removed client is still accepted")
	}
	out = e.call(t, owner, http.MethodPost, "/api/v1/oauth/clients:remove", map[string]any{"client_id": "https://known.example/client.json"}, http.StatusOK)
	if out["removed"] != false || out["ended"] != float64(1) {
		t.Errorf("ending a known client's connections: %v", out)
	}
	e.call(t, owner, http.MethodPost, "/api/v1/oauth/clients:remove", map[string]any{"client_id": tool}, http.StatusNotFound)
	e.call(t, owner, http.MethodPost, "/api/v1/oauth/clients:remove", map[string]any{}, http.StatusBadRequest)
}

func TestClientAPIWhenTheAuthorizationServerIsOff(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, adaP := e.user(t, "ada", store.RoleMember)
	// A grant left from when it was on is not shown and cannot act anyway.
	connected(t, e, "ga", ada.ID, "https://known.example/client.json", "Known")
	if res := e.call(t, adaP, http.MethodGet, "/api/v1/me/clients", nil, http.StatusOK); res["enabled"] != false || clientIDs(res) != "" {
		t.Errorf("my clients with the server off: %v", res)
	}
	if res := e.call(t, owner, http.MethodGet, "/api/v1/oauth/clients", nil, http.StatusOK); res["enabled"] != false || clientIDs(res) != "" {
		t.Errorf("client rules with the server off: %v", res)
	}
	e.call(t, owner, http.MethodPost, "/api/v1/oauth/clients", map[string]any{"client_id": "https://x.example/c.json"}, http.StatusConflict)
	// It can still be taken away.
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/clients/ga", nil, http.StatusOK)
}
