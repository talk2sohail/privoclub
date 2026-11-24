package control

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

func TestControlMutationsEvictGrantCache(t *testing.T) {
	ctx := context.Background()
	e := newSessionEnv(t)
	withPersonalServers(e)

	uOwner, owner := e.user(t, "boss", store.RoleOwner)
	uAda, _ := e.user(t, "ada", store.RoleMember)
	uBob, _ := e.user(t, "bob", store.RoleMember)

	addKeySrv := func(alias string) store.Server {
		t.Helper()
		e.call(t, owner, http.MethodPost, "/api/v1/servers", map[string]any{
			"alias": alias, "transport": "http", "url": "https://127.0.0.1:1/" + alias + "/mcp",
			"personal_key": map[string]any{},
		}, http.StatusCreated)
		s, err := e.st.ServerByAlias(ctx, e.org, alias)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	saveKey := func(srv store.Server, u store.User, key string) {
		t.Helper()
		ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: u.Person, Action: "upstream.connect:" + srv.Alias, Decision: "allow", Outcome: "ok"}
		if _, err := e.api.Grants.SaveKey(ctx, e.org, srv, u, key, u.Has(store.RoleOwner), ev); err != nil {
			t.Fatal(err)
		}
		if got, err := e.api.Grants.AccessToken(ctx, e.org, srv, u.ID); err != nil || got != key {
			t.Fatalf("warm AccessToken(%s, %s) = %q, %v", srv.Alias, u.Person, got, err)
		}
	}

	s1 := addKeySrv("acme")
	s2 := addKeySrv("beta")

	// 1. Admin disconnect (DELETE /api/v1/servers/{alias}/connections/{person}) evicts cache.
	saveKey(s1, uAda, "ada-s1-v1")
	e.call(t, owner, http.MethodDelete, "/api/v1/servers/acme/connections/ada", nil, http.StatusOK)
	if _, err := e.api.Grants.AccessToken(ctx, e.org, s1, uAda.ID); !errors.Is(err, grants.ErrNotConnected) {
		t.Fatalf("after admin disconnect: %v, want ErrNotConnected", err)
	}

	// 2. EditServer changing URL (EndConnections) evicts server's cached grants.
	saveKey(s1, uAda, "ada-s1-v2")
	saveKey(s1, uBob, "bob-s1-v1")
	saveKey(s2, uOwner, "boss-s2-v1")
	e.call(t, owner, http.MethodPatch, "/api/v1/servers/acme", map[string]any{
		"url": "https://127.0.0.1:2/acme-new/mcp",
	}, http.StatusOK)
	if _, err := e.api.Grants.AccessToken(ctx, e.org, s1, uAda.ID); !errors.Is(err, grants.ErrNotConnected) {
		t.Fatalf("ada after EditServer URL change: %v", err)
	}
	if _, err := e.api.Grants.AccessToken(ctx, e.org, s1, uBob.ID); !errors.Is(err, grants.ErrNotConnected) {
		t.Fatalf("bob after EditServer URL change: %v", err)
	}
	if got, err := e.api.Grants.AccessToken(ctx, e.org, s2, uOwner.ID); err != nil || got != "boss-s2-v1" {
		t.Fatalf("unrelated server affected by EditServer: %q, %v", got, err)
	}

	// 3. DeleteServer evicts all cached grants for that server.
	saveKey(s1, uAda, "ada-s1-v3")
	e.call(t, owner, http.MethodDelete, "/api/v1/servers/acme", nil, http.StatusOK)
	if _, err := e.api.Grants.AccessToken(ctx, e.org, s1, uAda.ID); !errors.Is(err, grants.ErrNotConnected) {
		t.Fatalf("after DeleteServer: %v", err)
	}

	// 4. RemoveMember (DisableUser) evicts all cached grants for that user across servers.
	saveKey(s2, uAda, "ada-s2-v1")
	e.call(t, owner, http.MethodPost, "/api/v1/members/ada/remove", nil, http.StatusOK)
	if _, err := e.api.Grants.AccessToken(ctx, e.org, s2, uAda.ID); !errors.Is(err, grants.ErrNotConnected) {
		t.Fatalf("after removeMember: %v", err)
	}
	if got, err := e.api.Grants.AccessToken(ctx, e.org, s2, uOwner.ID); err != nil || got != "boss-s2-v1" {
		t.Fatalf("owner affected by removing ada: %q, %v", got, err)
	}

	// 5. Demoting a member to non-gateway role evicts their in-memory cache while preserving DB grant.
	saveKey(s2, uBob, "bob-s2-v1")
	e.call(t, owner, http.MethodPatch, "/api/v1/members/bob", map[string]any{"roles": []string{store.RoleAuditor}}, http.StatusOK)
	origStore := e.api.Grants.Store
	sentinel := errors.New("probe-db-read")
	probe := &grantProbeStore{Store: origStore, fail: sentinel}
	e.api.Grants.Store = probe
	if _, err := e.api.Grants.AccessToken(ctx, e.org, s2, uBob.ID); !errors.Is(err, sentinel) {
		t.Fatalf("after demotion: expected DB read (cache evicted), got %v", err)
	}
	e.api.Grants.Store = origStore
	if _, err := e.st.Grant(ctx, e.org, s2.ID, uBob.ID); err != nil {
		t.Fatalf("role change unexpectedly deleted grant from DB: %v", err)
	}

	// 6. Disabling server (enabled: false) evicts cached tokens from memory without deleting DB grants.
	e.call(t, owner, http.MethodPatch, "/api/v1/servers/beta", map[string]any{"enabled": false}, http.StatusOK)
	e.api.Grants.Store = probe
	if _, err := e.api.Grants.AccessToken(ctx, e.org, s2, uOwner.ID); !errors.Is(err, sentinel) {
		t.Fatalf("after disabling server: expected DB read (cache evicted), got %v", err)
	}
	e.api.Grants.Store = origStore
	if _, err := e.st.Grant(ctx, e.org, s2.ID, uOwner.ID); err != nil {
		t.Fatalf("disabling server deleted grant from DB: %v", err)
	}
}

type grantProbeStore struct {
	grants.Store
	fail error
}

func (p *grantProbeStore) Grant(context.Context, store.OrgID, string, string) (store.Grant, error) {
	return store.Grant{}, p.fail
}
