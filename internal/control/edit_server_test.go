package control

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/store"
)

// ADR 0029: editing a server.

type editT struct {
	*sessionEnv
	owner auth.Principal
}

func editEnv(t *testing.T) (*editT, func(alias string, body map[string]any, want int) map[string]any) {
	t.Helper()
	e := newSessionEnv(t)
	withPersonalServers(e)
	allow, _ := egress.ParseAllowList("127.0.0.0/8,::1/128")
	e.api.Egress = egress.Policy{Allow: allow}
	_, owner := e.user(t, "boss", store.RoleOwner)
	return &editT{e, owner}, func(alias string, body map[string]any, want int) map[string]any {
		t.Helper()
		return e.call(t, owner, http.MethodPatch, "/api/v1/servers/"+alias, body, want)
	}
}

func (e *editT) post(t *testing.T, path string, body any, want int) {
	t.Helper()
	e.call(t, e.owner, http.MethodPost, path, body, want)
}

// acceptEcho records the tool echo as seen on srv and accepts it, as the
// review would (the test manager does not reach loopback upstreams).
func acceptEcho(t *testing.T, e *editT, srv store.Server) {
	t.Helper()
	pin := store.Pin{ServerID: srv.ID, Kind: "tool", Name: "echo", Hash: "h-echo", Definition: `{"name":"echo"}`}
	if err := e.st.RecordSeen(context.Background(), e.org, []store.Pin{pin}); err != nil {
		t.Fatal(err)
	}
	e.post(t, "/api/v1/servers/"+srv.Alias+"/pins:accept", map[string]any{"pins": []map[string]string{{"kind": "tool", "name": "echo", "hash": "h-echo"}}}, http.StatusOK)
}

func storedServer(t *testing.T, e *sessionEnv, alias string) store.Server {
	t.Helper()
	s, err := e.st.ServerByAlias(context.Background(), e.org, alias)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func credPurposesOf(t *testing.T, e *sessionEnv, serverID string) []string {
	t.Helper()
	cs, err := e.st.ServerCredentials(context.Background(), e.org, serverID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, c.Purpose)
	}
	slices.Sort(out)
	return out
}

func TestEditServerAddress(t *testing.T) {
	e, edit := editEnv(t)
	up, up2 := plainUpstream(t), plainUpstream(t)
	e.post(t, "/api/v1/servers", map[string]any{"alias": "kit", "transport": "http", "url": up.URL + "/mcp",
		"headers": map[string]string{"Authorization": "Bearer secret-one"}}, http.StatusCreated)
	srv := storedServer(t, e.sessionEnv, "kit")
	acceptEcho(t, e, srv)

	// The same header again with the new address: kept, sealed anew.
	out := edit("kit", map[string]any{"url": up2.URL + "/mcp", "headers": map[string]string{"Authorization": "Bearer secret-two"}}, http.StatusOK)
	after := storedServer(t, e.sessionEnv, "kit")
	if after.Config.URL != up2.URL+"/mcp" || !slices.Equal(after.Config.HeaderNames, []string{"Authorization"}) {
		t.Fatalf("stored: %+v", after.Config)
	}
	// Everything accepted from the old address waits for review again.
	ps, _ := e.st.ListPins(context.Background(), e.org, srv.ID)
	for _, p := range ps {
		if p.Status == store.PinAccepted {
			t.Fatalf("a tool stayed accepted after the address changed: %+v", p)
		}
	}
	acts := adminActions(t, e.sessionEnv)
	for _, want := range []string{"server.url", "server.headers:Authorization", "server.review_again:1", "server.connections_ended:0"} {
		if !strings.Contains(acts, want) {
			t.Fatalf("audit %s misses %s", acts, want)
		}
	}
	if s, _ := out["server"].(map[string]any); s["target"] == nil {
		t.Fatalf("answer: %v", out)
	}

	// Refused: an address the egress policy refuses; a member; nothing to change.
	edit("kit", map[string]any{"url": "https://10.0.0.1/mcp"}, http.StatusUnprocessableEntity)
	_, member := e.user(t, "ada", store.RoleMember)
	e.call(t, member, http.MethodPatch, "/api/v1/servers/kit", map[string]any{"url": up.URL + "/mcp"}, http.StatusForbidden)
	edit("kit", map[string]any{}, http.StatusUnprocessableEntity)
}

// A new address drops header values not given again: they were entered for
// the old host.
func TestEditServerAddressNeedsHeadersAgain(t *testing.T) {
	e, edit := editEnv(t)
	up, up2 := plainUpstream(t), plainUpstream(t)
	e.post(t, "/api/v1/servers", map[string]any{"alias": "kit", "transport": "http", "url": up.URL + "/mcp",
		"headers": map[string]string{"Authorization": "Bearer secret-one", "X-Team": "t1"}}, http.StatusCreated)
	srv := storedServer(t, e.sessionEnv, "kit")
	out := edit("kit", map[string]any{"url": up2.URL + "/mcp", "headers": map[string]string{"X-Team": "t1"}}, http.StatusOK)
	after := storedServer(t, e.sessionEnv, "kit")
	if !slices.Equal(after.Config.HeaderNames, []string{"X-Team"}) || !slices.Equal(credPurposesOf(t, e.sessionEnv, srv.ID), []string{"header:X-Team"}) {
		t.Fatalf("after: %+v %q", after.Config.HeaderNames, credPurposesOf(t, e.sessionEnv, srv.ID))
	}
	if got, _ := out["headers_dropped"].([]any); len(got) != 1 || got[0] != "Authorization" {
		t.Fatalf("the answer does not say which headers were dropped: %v", out)
	}
	// The same address again is no change of address: headers stay.
	edit("kit", map[string]any{"url": up2.URL + "/mcp"}, http.StatusOK)
	if got := credPurposesOf(t, e.sessionEnv, srv.ID); !slices.Equal(got, []string{"header:X-Team"}) {
		t.Fatalf("the same address dropped headers: %q", got)
	}
}

func TestEditServerHeaders(t *testing.T) {
	e, edit := editEnv(t)
	up := plainUpstream(t)
	e.post(t, "/api/v1/servers", map[string]any{"alias": "kit", "transport": "http", "url": up.URL + "/mcp",
		"headers": map[string]string{"Authorization": "Bearer a"}}, http.StatusCreated)
	srv := storedServer(t, e.sessionEnv, "kit")
	acceptEcho(t, e, srv)

	edit("kit", map[string]any{"headers": map[string]string{"x-team": "t1", "Authorization": ""}}, http.StatusOK)
	after := storedServer(t, e.sessionEnv, "kit")
	if !slices.Equal(after.Config.HeaderNames, []string{"X-Team"}) || !slices.Equal(credPurposesOf(t, e.sessionEnv, srv.ID), []string{"header:X-Team"}) {
		t.Fatalf("after: %+v %q", after.Config.HeaderNames, credPurposesOf(t, e.sessionEnv, srv.ID))
	}
	// Headers alone keep the accepted tools: the address is the same.
	ps, _ := e.st.ListPins(context.Background(), e.org, srv.ID)
	if ps[0].Status != store.PinAccepted {
		t.Fatalf("a header change sent tools back to review: %+v", ps[0])
	}
	if acts := adminActions(t, e.sessionEnv); !strings.Contains(acts, "server.headers:+X-Team,-Authorization") {
		t.Fatalf("audit: %s", acts)
	}
	for _, bad := range []map[string]string{{"Host": "x"}, {"X-A": "a\r\nb"}, {"bad name": "x"}, {"X-Gone": ""}} {
		edit("kit", map[string]any{"headers": bad}, http.StatusUnprocessableEntity)
	}
	// A server that takes each person's own key takes no static headers.
	e.post(t, "/api/v1/servers", map[string]any{"alias": "acme", "transport": "http", "url": up.URL + "/mcp", "personal_key": map[string]any{}}, http.StatusCreated)
	edit("acme", map[string]any{"headers": map[string]string{"X-Team": "t"}}, http.StatusUnprocessableEntity)
}

// A server that takes each person's own key: a new address ends every
// person's connection, so no key goes to the new host.
func TestEditServerAddressEndsPeoplesKeys(t *testing.T) {
	e, edit := editEnv(t)
	up, up2 := plainUpstream(t), plainUpstream(t)
	e.post(t, "/api/v1/servers", map[string]any{"alias": "acme", "transport": "http", "url": up.URL + "/mcp", "personal_key": map[string]any{}}, http.StatusCreated)
	srv := storedServer(t, e.sessionEnv, "acme")
	ada, _ := e.user(t, "ada", store.RoleMember)
	g := store.Grant{ServerID: srv.ID, UserID: ada.ID, ConnectedAt: time.Now(), Credential: store.Credential{ID: "k-ada", ServerID: srv.ID, OwnerID: ada.ID,
		Purpose: store.PurposeKeyGrant, KeyVersion: 1, RowVersion: 1, Ciphertext: []byte("sealed")}}
	ev := store.AuditEvent{ID: "c1", CallID: "c1", Time: time.Now(), Actor: "ada", Action: "upstream.key:acme", Decision: "allow", Outcome: "ok"}
	if _, err := e.st.SaveGrant(context.Background(), e.org, g, false, ev); err != nil {
		t.Fatal(err)
	}
	edit("acme", map[string]any{"url": up2.URL + "/mcp"}, http.StatusOK)
	if gs, _ := e.st.ListServerGrants(context.Background(), e.org, srv.ID); len(gs) != 0 {
		t.Fatalf("a person's key survived the new address: %+v", gs)
	}
	if acts := adminActions(t, e.sessionEnv); !strings.Contains(acts, "server.connections_ended:1") {
		t.Fatalf("audit: %s", acts)
	}
}

func oauthServerFor(t *testing.T, e *sessionEnv, alias, registration string) store.Server {
	t.Helper()
	srv := store.Server{OrgID: e.org, ID: "srv-" + alias, Alias: alias, Transport: "http", Enabled: true, CreatedAt: time.Now(),
		Config: store.ServerConfig{URL: "https://127.0.0.1:1/" + alias + "/mcp", Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{
			Issuer: "https://as.example", AuthorizationEndpoint: "https://as.example/authorize", TokenEndpoint: "https://as.example/token",
			Resource: "https://127.0.0.1:1/" + alias + "/mcp", ClientID: "client-1", Registration: registration, TokenAuthMethod: "none", IssuerNamed: true}}}
	if err := e.st.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
	return srv
}

func connectGrant(t *testing.T, e *sessionEnv, srv store.Server, u store.User) {
	t.Helper()
	g := store.Grant{ServerID: srv.ID, UserID: u.ID, ConnectedAt: time.Now(), Credential: store.Credential{ID: "g-" + u.ID + srv.ID, ServerID: srv.ID, OwnerID: u.ID,
		Purpose: store.PurposeOAuthGrant, KeyVersion: 1, RowVersion: 1, Ciphertext: []byte("sealed")}}
	ev := store.AuditEvent{ID: "c-" + u.ID + srv.ID, CallID: "c", Time: time.Now(), Actor: u.Person, Action: "upstream.connect", Decision: "allow", Outcome: "ok"}
	if _, err := e.st.SaveGrant(context.Background(), e.org, g, false, ev); err != nil {
		t.Fatal(err)
	}
}

func TestEditServerRefusesOAuthAddress(t *testing.T) {
	e, edit := editEnv(t)
	oauthServerFor(t, e.sessionEnv, "linear", store.RegistrationAdmin)
	out := edit("linear", map[string]any{"url": "https://127.0.0.1:2/mcp"}, http.StatusUnprocessableEntity)
	if !strings.Contains(out["error"].(string), "remove it and add it again") {
		t.Fatalf("message: %v", out)
	}
	edit("linear", map[string]any{"headers": map[string]string{"X-A": "a"}}, http.StatusUnprocessableEntity)
}

func TestEditServerOAuthClient(t *testing.T) {
	e, edit := editEnv(t)
	srv := oauthServerFor(t, e.sessionEnv, "linear", store.RegistrationAdmin)
	ada, _ := e.user(t, "ada", store.RoleMember)
	connectGrant(t, e.sessionEnv, srv, ada)

	// A secret alone keeps people connected.
	edit("linear", map[string]any{"oauth_client": map[string]any{"client_id": "client-1", "client_secret": "s3cret-value"}}, http.StatusOK)
	if gs, _ := e.st.ListServerGrants(context.Background(), e.org, srv.ID); len(gs) != 1 {
		t.Fatalf("a new secret ended connections: %+v", gs)
	}
	after := storedServer(t, e.sessionEnv, "linear")
	if !after.Config.OAuth.HasClientSecret || !slices.Equal(credPurposesOf(t, e.sessionEnv, srv.ID), []string{store.PurposeOAuthGrant, store.PurposeOAuthClient}) {
		t.Fatalf("after the secret: %+v %q", after.Config.OAuth, credPurposesOf(t, e.sessionEnv, srv.ID))
	}
	// Omitted keeps the secret; a new client id ends every connection.
	edit("linear", map[string]any{"oauth_client": map[string]any{"client_id": "client-2"}}, http.StatusOK)
	after = storedServer(t, e.sessionEnv, "linear")
	if after.Config.OAuth.ClientID != "client-2" || !after.Config.OAuth.HasClientSecret {
		t.Fatalf("after the client id: %+v", after.Config.OAuth)
	}
	if gs, _ := e.st.ListServerGrants(context.Background(), e.org, srv.ID); len(gs) != 0 {
		t.Fatalf("a new client id kept connections: %+v", gs)
	}
	// "" removes the secret.
	edit("linear", map[string]any{"oauth_client": map[string]any{"client_id": "client-2", "client_secret": ""}}, http.StatusOK)
	if after = storedServer(t, e.sessionEnv, "linear"); after.Config.OAuth.HasClientSecret || slices.Contains(credPurposesOf(t, e.sessionEnv, srv.ID), store.PurposeOAuthClient) {
		t.Fatalf("the secret was not removed: %+v", after.Config.OAuth)
	}
	if acts := adminActions(t, e.sessionEnv); !strings.Contains(acts, "server.oauth_client") || !strings.Contains(acts, "server.connections_ended:1") {
		t.Fatalf("audit: %s", acts)
	}
	// Refused: a client Privo registered itself; a bad client id; a server
	// without OAuth.
	oauthServerFor(t, e.sessionEnv, "dyn", store.RegistrationDynamic)
	edit("dyn", map[string]any{"oauth_client": map[string]any{"client_id": "mine"}}, http.StatusUnprocessableEntity)
	edit("linear", map[string]any{"oauth_client": map[string]any{"client_id": "has space"}}, http.StatusUnprocessableEntity)
	edit("linear", map[string]any{"oauth_client": map[string]any{"client_id": ""}}, http.StatusUnprocessableEntity)
	e.seedServer(t, "plain", nil)
	edit("plain", map[string]any{"oauth_client": map[string]any{"client_id": "x"}}, http.StatusUnprocessableEntity)
}

// Values never come back: not in the answer, not in the audit.
func TestEditServerNeverReturnsSecrets(t *testing.T) {
	e, _ := editEnv(t)
	up := plainUpstream(t)
	e.post(t, "/api/v1/servers", map[string]any{"alias": "kit", "transport": "http", "url": up.URL + "/mcp"}, http.StatusCreated)
	_, raw := e.api.Serve(context.Background(), e.owner, http.MethodPatch, "/api/v1/servers/kit", map[string]any{"headers": map[string]string{"Authorization": "Bearer top-secret-value"}})
	if strings.Contains(string(raw), "top-secret-value") {
		t.Fatalf("the answer holds the value: %s", raw)
	}
	if !strings.Contains(string(raw), `"header_names":["Authorization"]`) {
		t.Fatalf("the answer does not say which headers are set: %s", raw)
	}
	oauthServerFor(t, e.sessionEnv, "linear", store.RegistrationAdmin)
	_, raw = e.api.Serve(context.Background(), e.owner, http.MethodPatch, "/api/v1/servers/linear", map[string]any{"oauth_client": map[string]any{"client_id": "client-1", "client_secret": "s3cret-value"}})
	if strings.Contains(string(raw), "s3cret-value") || !strings.Contains(string(raw), `"has_client_secret":true`) {
		t.Fatalf("answer: %s", raw)
	}
	if acts := adminActions(t, e.sessionEnv); strings.Contains(acts, "top-secret") || strings.Contains(acts, "s3cret") {
		t.Fatalf("the audit holds a value: %s", acts)
	}
}

// Security review of the third slice, L2: a request refused for an unknown
// team changes nothing, also not the address it carried.
func TestRefusedEditChangesNothing(t *testing.T) {
	e, edit := editEnv(t)
	up, up2 := plainUpstream(t), plainUpstream(t)
	e.post(t, "/api/v1/servers", map[string]any{"alias": "kit", "transport": "http", "url": up.URL + "/mcp",
		"headers": map[string]string{"Authorization": "Bearer a"}}, http.StatusCreated)
	srv := storedServer(t, e.sessionEnv, "kit")
	acceptEcho(t, e, srv)
	edit("kit", map[string]any{"url": up2.URL + "/mcp", "teams": []string{"nosuchteam"}}, http.StatusUnprocessableEntity)
	after := storedServer(t, e.sessionEnv, "kit")
	if after.Config.URL != up.URL+"/mcp" || !slices.Equal(credPurposesOf(t, e.sessionEnv, srv.ID), []string{"header:Authorization"}) {
		t.Fatalf("a refused edit changed the server: %+v %q", after.Config, credPurposesOf(t, e.sessionEnv, srv.ID))
	}
	if ps, _ := e.st.ListPins(context.Background(), e.org, srv.ID); ps[0].Status != store.PinAccepted {
		t.Fatalf("a refused edit sent tools back to review: %+v", ps[0])
	}
}
