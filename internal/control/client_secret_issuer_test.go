package control

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// F07 R-05: a client secret goes only to an authorization server the admin
// named. Without oauth.issuer, the server would choose where it goes.
func TestClientSecretNeedsANamedIssuer(t *testing.T) {
	e := newSessionEnv(t)
	withPersonalServers(e)
	e.api.OAuth = &upstreamauth.Client{HTTP: egress.NewClient(egress.Options{Policy: e.api.Egress}), Timeout: time.Second}
	_, owner := e.user(t, "boss", store.RoleOwner)
	out := e.call(t, owner, http.MethodPost, "/api/v1/servers", map[string]any{"alias": "gh", "transport": "http", "url": "https://127.0.0.1:1/mcp",
		"oauth": map[string]any{"client_id": "Iv1.abc", "client_secret": "s3cret"}}, http.StatusUnprocessableEntity)
	if msg, _ := out["error"].(string); !strings.Contains(msg, "oauth.issuer") {
		t.Fatalf("message: %v", out)
	}
}

// The same rule when a secret is added later by editing the client: a server
// whose authorization server was found from the server, not named by the
// admin, takes no secret (security review of the findings batch, M1).
func TestEditedClientSecretNeedsANamedIssuer(t *testing.T) {
	e, edit := editEnv(t)
	seed := store.AuditEvent{ID: "seed", CallID: "seed", Time: time.Now(), Actor: "t", Action: "seed", Decision: "allow", Outcome: "ok"}
	// A server whose sign-in was found from the server (issuer not named).
	found := oauthServerFor(t, e.sessionEnv, "found", store.RegistrationAdmin)
	found.Config.OAuth.IssuerNamed = false
	if _, err := e.st.EditServer(t.Context(), e.org, found.ID, store.ServerEdit{Version: storedServer(t, e.sessionEnv, "found").UpdatedAt, Config: found.Config},
		[]store.AuditEvent{seed}); err != nil {
		t.Fatal(err)
	}
	out := edit("found", map[string]any{"oauth_client": map[string]any{"client_id": "client-1", "client_secret": "s3cret"}}, http.StatusUnprocessableEntity)
	if msg, _ := out["error"].(string); !strings.Contains(msg, "oauth.issuer") {
		t.Fatalf("message: %v", out)
	}
	// Removing a secret, or changing only the client id, is always allowed.
	edit("found", map[string]any{"oauth_client": map[string]any{"client_id": "client-2"}}, http.StatusOK)
	edit("found", map[string]any{"oauth_client": map[string]any{"client_id": "client-2", "client_secret": ""}}, http.StatusOK)
	// Named at add time, or entered by hand: a secret is fine.
	oauthServerFor(t, e.sessionEnv, "named", store.RegistrationAdmin)
	edit("named", map[string]any{"oauth_client": map[string]any{"client_id": "client-1", "client_secret": "s3cret"}}, http.StatusOK)
	oauthServerFor(t, e.sessionEnv, "hand", store.RegistrationManual)
	edit("hand", map[string]any{"oauth_client": map[string]any{"client_id": "client-1", "client_secret": "s3cret"}}, http.StatusOK)
}
