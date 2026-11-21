package control

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// F07 A: an admin sees everyone's connections across servers, filters them,
// sees which are unused, and ends one; a member sees none of it.
func TestConnectionsAcrossServers(t *testing.T) {
	e, _ := editEnv(t)
	linear := oauthServerFor(t, e.sessionEnv, "linear", store.RegistrationAdmin)
	notion := oauthServerFor(t, e.sessionEnv, "notion", store.RegistrationAdmin)
	ada, member := e.user(t, "ada", store.RoleMember)
	bob, _ := e.user(t, "bob", store.RoleMember)
	save := func(srv store.Server, u store.User, at time.Time) {
		t.Helper()
		ev := store.AuditEvent{ID: "c-" + srv.Alias + u.Person, CallID: "c", Time: at, Actor: u.Person, Action: "upstream.connect", Decision: "allow", Outcome: "ok"}
		e.api.Grants.Now = func() time.Time { return at }
		if _, err := e.api.Grants.Save(context.Background(), e.org, srv, u, upstreamauth.Token{AccessToken: "secret-at", RefreshToken: "secret-rt", Expiry: at.Add(time.Hour)}, false, ev); err != nil {
			t.Fatal(err)
		}
	}
	save(linear, ada, time.Now())
	save(notion, ada, time.Now().AddDate(0, 0, -120)) // not used since
	save(linear, bob, time.Now())

	out := e.call(t, e.owner, http.MethodGet, "/api/v1/connections", nil, http.StatusOK)
	list := out["connections"].([]any)
	if len(list) != 3 {
		t.Fatalf("connections: %v", out)
	}
	unused := 0
	for _, c := range list {
		m := c.(map[string]any)
		if m["kind"] != "oauth" || m["server"] == "" || m["person"] == "" {
			t.Fatalf("a row: %v", m)
		}
		if m["unused"] == true {
			unused++
			if m["server"] != "notion" || m["person"] != "ada" {
				t.Fatalf("marked unused: %v", m)
			}
		}
	}
	if unused != 1 {
		t.Fatalf("%d unused, want 1", unused)
	}
	if raw := mustRaw(t, e, "/api/v1/connections"); containsAny(raw, "secret-at", "secret-rt") {
		t.Fatalf("a token in the answer: %s", raw)
	}
	if got := e.call(t, e.owner, http.MethodGet, "/api/v1/connections?person=ada", nil, http.StatusOK)["connections"].([]any); len(got) != 2 {
		t.Fatalf("ada's: %d", len(got))
	}
	if got := e.call(t, e.owner, http.MethodGet, "/api/v1/connections?server=linear&limit=1", nil, http.StatusOK); len(got["connections"].([]any)) != 1 || got["next"] == "" {
		t.Fatalf("a page of linear's: %v", got)
	}
	e.call(t, e.owner, http.MethodGet, "/api/v1/connections?after=garbage", nil, http.StatusBadRequest)
	e.call(t, member, http.MethodGet, "/api/v1/connections", nil, http.StatusForbidden)

	// Ending one leaves the others.
	e.call(t, e.owner, http.MethodDelete, "/api/v1/servers/notion/connections/ada", nil, http.StatusOK)
	if got := e.call(t, e.owner, http.MethodGet, "/api/v1/connections", nil, http.StatusOK)["connections"].([]any); len(got) != 2 {
		t.Fatalf("after ending one: %d", len(got))
	}
}

func mustRaw(t *testing.T, e *editT, path string) string {
	t.Helper()
	_, raw := e.api.Serve(context.Background(), e.owner, http.MethodGet, path, nil)
	return string(raw)
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(x string) bool { return strings.Contains(s, x) })
}
