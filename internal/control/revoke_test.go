package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// F07 R-04: removing a member, deleting a server, and an edit that ends
// connections revoke the grants at the provider too, not only locally.
func TestGrantsDeletedAreRevokedAtTheProvider(t *testing.T) {
	e, edit := editEnv(t)
	var mu sync.Mutex
	var revoked []string
	prov := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		revoked = append(revoked, r.PostForm.Get("token"))
		mu.Unlock()
	}))
	t.Cleanup(prov.Close)
	e.api.Grants.Auth = &upstreamauth.Client{HTTP: egress.NewClient(egress.Options{Policy: e.api.Egress}), Timeout: time.Second}
	got := func(tok string) func() bool {
		return func() bool { mu.Lock(); defer mu.Unlock(); return slices.Contains(revoked, tok) }
	}
	connectAs := func(srv store.Server, u store.User, refresh string) {
		t.Helper()
		ev := store.AuditEvent{ID: "c-" + refresh, CallID: "c", Time: time.Now(), Actor: u.Person, Action: "upstream.connect", Decision: "allow", Outcome: "ok"}
		if _, err := e.api.Grants.Save(context.Background(), e.org, srv, u, upstreamauth.Token{AccessToken: "at", RefreshToken: refresh, Expiry: time.Now().Add(time.Hour)}, false, ev); err != nil {
			t.Fatal(err)
		}
	}
	withRevocation := func(alias string) store.Server {
		t.Helper()
		srv := oauthServerFor(t, e.sessionEnv, alias, store.RegistrationAdmin)
		srv.Config.OAuth.RevocationEndpoint = prov.URL + "/revoke"
		if _, err := e.st.EditServer(context.Background(), e.org, srv.ID, store.ServerEdit{Version: storedServer(t, e.sessionEnv, alias).UpdatedAt, Config: srv.Config},
			[]store.AuditEvent{{ID: "seed-" + alias, CallID: "s", Time: time.Now(), Actor: "t", Action: "seed", Decision: "allow", Outcome: "ok"}}); err != nil {
			t.Fatal(err)
		}
		return storedServer(t, e.sessionEnv, alias)
	}
	linear, notion := withRevocation("linear"), withRevocation("notion")
	ada, _ := e.user(t, "ada", store.RoleMember)
	bob, _ := e.user(t, "bob", store.RoleMember)
	cy, _ := e.user(t, "cy", store.RoleMember)
	connectAs(linear, ada, "rt-ada-linear")
	connectAs(notion, ada, "rt-ada-notion")
	connectAs(linear, bob, "rt-bob-linear")
	connectAs(notion, cy, "rt-cy-notion")

	// Removing ada revokes both of hers, nobody else's.
	e.call(t, e.owner, http.MethodPost, "/api/v1/members/ada/remove", nil, http.StatusOK)
	eventuallyTrue(t, "ada's grants revoked", func() bool { return got("rt-ada-linear")() && got("rt-ada-notion")() })
	if got("rt-bob-linear")() {
		t.Fatal("bob's grant was revoked with ada's")
	}
	// A new client id ends bob's connection: revoked with the old client.
	edit("linear", map[string]any{"oauth_client": map[string]any{"client_id": "client-2"}}, http.StatusOK)
	eventuallyTrue(t, "bob's grant revoked", got("rt-bob-linear"))
	// Deleting a server revokes its grants.
	e.call(t, e.owner, http.MethodDelete, "/api/v1/servers/notion", nil, http.StatusOK)
	eventuallyTrue(t, "cy's grant revoked", got("rt-cy-notion"))
}

func eventuallyTrue(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
