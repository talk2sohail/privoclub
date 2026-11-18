package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for invites and per-person upstream grants (ADR 0017).

func newInvite(t *testing.T, s *Store, org OrgID, id, hash, person, role string, now time.Time) {
	t.Helper()
	err := s.CreateInvite(context.Background(), org, NewInvite{ID: id, SecretHash: hash, Person: person, Roles: []string{role}, CreatedBy: "tester", Now: now, ExpiresAt: now.Add(72 * time.Hour)}, adminEv("invite.create:"+id))
	if err != nil {
		t.Fatal(err)
	}
}

// oauthServer stores an http server in oauth mode and returns its id.
func oauthServer(t *testing.T, s *Store, org OrgID, alias string) string {
	t.Helper()
	srv := Server{OrgID: org, ID: "srv-" + alias, Alias: alias, Transport: "http", Enabled: true, CreatedAt: t0,
		Config: ServerConfig{URL: "https://" + alias + ".example/mcp", Auth: AuthOAuth, OAuth: &ServerOAuth{
			Issuer: "https://as.example", AuthorizationEndpoint: "https://as.example/authorize", TokenEndpoint: "https://as.example/token",
			Resource: "https://" + alias + ".example/mcp", ClientID: "client-1", Registration: RegistrationAdmin}}}
	if err := s.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
	return srv.ID
}

// member adds a user with role through an invite claim.
func member(t *testing.T, s *Store, org OrgID, subject, person, role string) User {
	t.Helper()
	newInvite(t, s, org, "inv-"+subject, "ih-"+subject, person, role, t0)
	c := claimFor("inv-"+subject, subject, "ignored", "sess-"+subject, t0.Add(time.Minute))
	u, err := s.ClaimInvite(context.Background(), org, c, adminEv("invite.claim:"+subject))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func grantFor(serverID, userID, credID string) Grant {
	return Grant{ServerID: serverID, UserID: userID, Scopes: "repo read:user", ConnectedAt: t0, Credential: Credential{
		ID: credID, ServerID: serverID, OwnerID: userID, Purpose: PurposeOAuthGrant, KeyVersion: 1, RowVersion: 1, Ciphertext: []byte("sealed-" + credID)}}
}

func TestInviteSingleUseRoleAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	newInvite(t, s, org, "i1", "ih-1", "ada", RoleMember, t0)

	inv, err := s.Invite(ctx, org, "ih-1", t0.Add(time.Hour))
	if err != nil || inv.Person != "ada" || !slices.Equal(inv.Roles, []string{RoleMember}) {
		t.Fatalf("Invite = %+v, %v", inv, err)
	}
	if _, err := s.Invite(ctx, org, "ih-1", t0.Add(72*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("at expiry: %v, want ErrNotFound", err)
	}
	u, err := s.ClaimInvite(ctx, org, claimFor("i1", "2001", "github-login", "sess-1", t0.Add(time.Minute)), adminEv("invite.claim"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Person != "ada" || !slices.Equal(u.Roles, []string{RoleMember}) {
		t.Errorf("claimed user %+v: the person and role come from the invite", u)
	}
	if _, err := s.ClaimInvite(ctx, org, claimFor("i1", "2002", "x", "sess-2", t0.Add(2*time.Minute)), adminEv("invite.claim2")); !errors.Is(err, ErrSetupUnavailable) {
		t.Errorf("second claim: %v, want ErrSetupUnavailable", err)
	}
	if _, err := s.Invite(ctx, org, "ih-1", t0.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("claimed invite still found: %v", err)
	}

	// Invites cannot make owners, need a known role, cannot outlive 7 days and
	// cannot name a person who is already a member.
	for name, inv := range map[string]NewInvite{
		"owner":     {ID: "x1", SecretHash: "x1", Person: "bob", Roles: []string{RoleOwner}, CreatedBy: "t", Now: t0, ExpiresAt: t0.Add(time.Hour)},
		"bad role":  {ID: "x2", SecretHash: "x2", Person: "bob", Roles: []string{"root"}, CreatedBy: "t", Now: t0, ExpiresAt: t0.Add(time.Hour)},
		"too long":  {ID: "x3", SecretHash: "x3", Person: "bob", Roles: []string{RoleMember}, CreatedBy: "t", Now: t0, ExpiresAt: t0.Add(8 * 24 * time.Hour)},
		"no person": {ID: "x4", SecretHash: "x4", Person: "", Roles: []string{RoleMember}, CreatedBy: "t", Now: t0, ExpiresAt: t0.Add(time.Hour)},
	} {
		if err := s.CreateInvite(ctx, org, inv, adminEv("invite.create:"+name)); err == nil {
			t.Errorf("%s: invite accepted", name)
		}
	}
	if err := s.CreateInvite(ctx, org, NewInvite{ID: "x5", SecretHash: "x5", Person: "ada", Roles: []string{RoleMember}, CreatedBy: "t", Now: t0, ExpiresAt: t0.Add(time.Hour)}, adminEv("invite.create:x5")); !errors.Is(err, ErrConflict) {
		t.Errorf("invite for an existing member: %v, want ErrConflict", err)
	}

	// A new invite for the same person retires the older one.
	newInvite(t, s, org, "b1", "bh-1", "bob", RoleAdmin, t0)
	newInvite(t, s, org, "b2", "bh-2", "bob", RoleMember, t0.Add(time.Minute))
	if _, err := s.Invite(ctx, org, "bh-1", t0.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("older invite still usable: %v", err)
	}
	if _, err := s.Invite(ctx, org, "bh-2", t0.Add(time.Hour)); err != nil {
		t.Errorf("newer invite: %v", err)
	}
	acts := strings.Join(auditActions(t, s, org), " ")
	for _, want := range []string{"invite.create:i1", "invite.claim"} {
		if !strings.Contains(acts, want) {
			t.Errorf("audit has no %s: %s", want, acts)
		}
	}
}

func TestInviteBindsTheAccountNotTheLogin(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	startSession(t, s, org, "owner-sess", t0) // owner with GitHub subject "1"
	newInvite(t, s, org, "i1", "ih-1", "ada", RoleMember, t0)

	// The owner's GitHub account cannot claim a second membership, and the
	// invite stays usable.
	if _, err := s.ClaimInvite(ctx, org, claimFor("i1", "1", "ada", "s-x", t0.Add(time.Minute)), adminEv("c1")); !errors.Is(err, ErrIdentityExists) {
		t.Fatalf("claim by an existing member: %v, want ErrIdentityExists", err)
	}
	if _, err := s.Invite(ctx, org, "ih-1", t0.Add(time.Minute)); err != nil {
		t.Fatalf("refused claim consumed the invite: %v", err)
	}
	// Any account may claim (the link is the secret); the identity bound is the
	// numeric subject, whatever the login says.
	u, err := s.ClaimInvite(ctx, org, claimFor("i1", "9876", "ada", "s-y", t0.Add(time.Minute)), adminEv("c2"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.UserByIdentity(ctx, org, ProviderGitHub, "9876")
	if err != nil || got.ID != u.ID {
		t.Fatalf("identity not bound by subject: %+v %v", got, err)
	}
	if byPerson, err := s.UserByPerson(ctx, org, "ada"); err != nil || byPerson.ID != u.ID {
		t.Fatalf("UserByPerson = %+v, %v", byPerson, err)
	}
	if _, err := s.UserByPerson(ctx, org, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UserByPerson(unknown) = %v", err)
	}
}

func TestGrantOnePerServerAndUser(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := oauthServer(t, s, org, "linear")
	ada := member(t, s, org, "2001", "ada", RoleMember)

	g, err := s.SaveGrant(ctx, org, grantFor(srv, ada.ID, "c1"), false, adminEv("upstream.connect:linear"))
	if err != nil || g.Catalog {
		t.Fatalf("SaveGrant = %+v, %v (a member's grant is never the catalog grant)", g, err)
	}
	// Connecting again replaces the grant and its credential.
	if _, err := s.SaveGrant(ctx, org, grantFor(srv, ada.ID, "c2"), false, adminEv("upstream.connect:linear#2")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Grant(ctx, org, srv, ada.ID)
	if err != nil || got.Credential.ID != "c2" || string(got.Credential.Ciphertext) != "sealed-c2" || got.Credential.OwnerID != ada.ID {
		t.Fatalf("Grant = %+v, %v", got, err)
	}
	creds, _ := s.ServerCredentials(ctx, org, srv)
	if len(creds) != 1 || creds[0].ID != "c2" {
		t.Fatalf("credentials after reconnect = %+v, want only c2", creds)
	}
	list, err := s.ListGrants(ctx, org, ada.ID)
	if err != nil || len(list) != 1 || list[0].Alias != "linear" || list[0].Scopes != "repo read:user" || list[0].Credential.Ciphertext != nil {
		t.Fatalf("ListGrants = %+v, %v (no ciphertext in lists)", list, err)
	}
	// A credential that does not belong to the user and server is refused.
	bad := grantFor(srv, ada.ID, "c3")
	bad.Credential.OwnerID = "someone-else"
	if _, err := s.SaveGrant(ctx, org, bad, false, adminEv("bad")); err == nil {
		t.Error("grant with another owner's credential accepted")
	}
	// Unknown server or user.
	if _, err := s.SaveGrant(ctx, org, grantFor("no-such-server", ada.ID, "c4"), false, adminEv("bad2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}
	if _, err := s.SaveGrant(ctx, org, grantFor(srv, "no-such-user", "c5"), false, adminEv("bad3")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := s.Grant(ctx, org, srv, "no-such-user"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Grant(unknown) = %v", err)
	}

	// Touch writes at most once a minute.
	if err := s.TouchGrant(ctx, org, srv, ada.ID, t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	g1, _ := s.Grant(ctx, org, srv, ada.ID)
	if !g1.LastUsedAt.Equal(t0) {
		t.Errorf("touched within a minute: %v", g1.LastUsedAt)
	}
	_ = s.TouchGrant(ctx, org, srv, ada.ID, t0.Add(2*time.Minute))
	g2, _ := s.Grant(ctx, org, srv, ada.ID)
	if !g2.LastUsedAt.Equal(t0.Add(2 * time.Minute)) {
		t.Errorf("last used = %v, want %v", g2.LastUsedAt, t0.Add(2*time.Minute))
	}

	// Disconnect removes the grant and its tokens.
	if err := s.DeleteGrant(ctx, org, srv, ada.ID, adminEv("upstream.disconnect:linear")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, org, srv, ada.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("grant after disconnect: %v", err)
	}
	if creds, _ := s.ServerCredentials(ctx, org, srv); len(creds) != 0 {
		t.Errorf("credentials after disconnect: %+v", creds)
	}
	if err := s.DeleteGrant(ctx, org, srv, ada.ID, adminEv("again")); !errors.Is(err, ErrNotFound) {
		t.Errorf("second disconnect: %v", err)
	}
}

func TestCatalogGrantFirstManagerAndPromotion(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := oauthServer(t, s, org, "linear")
	owner := startSession(t, s, org, "owner-sess", t0)
	admin := member(t, s, org, "3001", "carol", RoleAdmin)
	ada := member(t, s, org, "2001", "ada", RoleMember)

	if _, err := s.CatalogGrant(ctx, org, srv); !errors.Is(err, ErrNotFound) {
		t.Fatalf("catalog grant before anyone connected: %v", err)
	}
	// A member connects first: never the catalog.
	if g, _ := s.SaveGrant(ctx, org, grantFor(srv, ada.ID, "c-ada"), false, adminEv("a")); g.Catalog {
		t.Fatal("member's grant became the catalog grant")
	}
	g, err := s.SaveGrant(ctx, org, grantFor(srv, owner.ID, "c-owner"), true, adminEv("o"))
	if err != nil || !g.Catalog {
		t.Fatalf("first manager's grant: %+v %v, want catalog", g, err)
	}
	g, _ = s.SaveGrant(ctx, org, grantFor(srv, admin.ID, "c-admin"), true, adminEv("c"))
	if g.Catalog {
		t.Fatal("a second catalog grant for one server")
	}
	// Reconnecting keeps the catalog mark.
	if g, _ := s.SaveGrant(ctx, org, grantFor(srv, owner.ID, "c-owner2"), true, adminEv("o2")); !g.Catalog {
		t.Fatal("reconnecting lost the catalog mark")
	}
	cat, err := s.CatalogGrant(ctx, org, srv)
	if err != nil || cat.UserID != owner.ID || cat.Credential.ID != "c-owner2" {
		t.Fatalf("CatalogGrant = %+v, %v", cat, err)
	}
	// The owner disconnects: the other manager's grant takes over, never the member's.
	if err := s.DeleteGrant(ctx, org, srv, owner.ID, adminEv("od")); err != nil {
		t.Fatal(err)
	}
	if cat, err := s.CatalogGrant(ctx, org, srv); err != nil || cat.UserID != admin.ID {
		t.Fatalf("after the owner left: %+v, %v, want carol's grant", cat, err)
	}
	// Carol is demoted to member: her grant stops being the catalog, and no
	// other manager has a grant.
	if _, err := s.SetUserRoles(ctx, org, admin.ID, []string{RoleMember}, adminEv("demote")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CatalogGrant(ctx, org, srv); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a member's grant is the catalog after demotion: %v", err)
	}
}

func TestGrantCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := oauthServer(t, s, org, "linear")
	ada := member(t, s, org, "2001", "ada", RoleMember)
	if _, err := s.SaveGrant(ctx, org, grantFor(srv, ada.ID, "c1"), false, adminEv("a")); err != nil {
		t.Fatal(err)
	}
	// Two refreshes race from row version 1: exactly one wins.
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := Credential{ID: "c1", ServerID: srv, OwnerID: ada.ID, Purpose: PurposeOAuthGrant, KeyVersion: 1, RowVersion: 2, Ciphertext: []byte{byte(i)}}
			switch err := s.UpdateGrantToken(ctx, org, srv, ada.ID, 1, c, t0.Add(time.Minute)); {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrConflict):
				conflicts.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 7 {
		t.Fatalf("wins %d, conflicts %d; want 1 and 7", wins.Load(), conflicts.Load())
	}
	g, _ := s.Grant(ctx, org, srv, ada.ID)
	if g.Credential.RowVersion != 2 || !g.RefreshedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after refresh: row version %d, refreshed %v", g.Credential.RowVersion, g.RefreshedAt)
	}
	// The new row version must be the next one.
	c := g.Credential
	c.RowVersion = 7
	if err := s.UpdateGrantToken(ctx, org, srv, ada.ID, 2, c, t0); err == nil {
		t.Error("row version jump accepted")
	}
	// A grant that was deleted meanwhile is not found.
	_ = s.DeleteGrant(ctx, org, srv, ada.ID, adminEv("d"))
	c.RowVersion = 3
	if err := s.UpdateGrantToken(ctx, org, srv, ada.ID, 2, c, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("refresh of a deleted grant: %v, want ErrNotFound", err)
	}
}

func TestDisableUserDeletesGrants(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := oauthServer(t, s, org, "linear")
	owner := startSession(t, s, org, "owner-sess", t0)
	admin := member(t, s, org, "3001", "carol", RoleAdmin)
	_, _ = s.SaveGrant(ctx, org, grantFor(srv, owner.ID, "c-owner"), true, adminEv("o"))
	_, _ = s.SaveGrant(ctx, org, grantFor(srv, admin.ID, "c-admin"), true, adminEv("c"))

	if err := s.DisableUser(ctx, org, owner.ID, t0.Add(time.Hour), adminEv("disable")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, org, srv, owner.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("disabled user's grant survived: %v", err)
	}
	creds, _ := s.ServerCredentials(ctx, org, srv)
	for _, c := range creds {
		if c.OwnerID == owner.ID {
			t.Errorf("disabled user's tokens survived: %+v", c)
		}
	}
	if cat, err := s.CatalogGrant(ctx, org, srv); err != nil || cat.UserID != admin.ID {
		t.Errorf("catalog after disabling the owner: %+v, %v", cat, err)
	}
}

func TestDeleteServerDeletesGrants(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := oauthServer(t, s, org, "linear")
	ada := member(t, s, org, "2001", "ada", RoleMember)
	_, _ = s.SaveGrant(ctx, org, grantFor(srv, ada.ID, "c1"), false, adminEv("a"))
	if err := s.DeleteServer(ctx, org, srv); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListGrants(ctx, org, ada.ID); len(list) != 0 {
		t.Errorf("grants after deleting the server: %+v", list)
	}
	var n int
	_ = s.r.QueryRow(`SELECT count(*) FROM credentials WHERE org_id = ? AND owner_id = ?`, org, ada.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d token rows left", n)
	}
	// The alias can be reused for a new server with no grants.
	srv2 := oauthServer(t, s, org, "linear2")
	if _, err := s.Grant(ctx, org, srv2, ada.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("new server inherited a grant: %v", err)
	}
}

func TestServerOAuthConfigAndClientSecret(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := oauthServer(t, s, org, "notion")
	if byID, err := s.ServerByID(ctx, org, srv); err != nil || byID.Alias != "notion" {
		t.Fatalf("ServerByID = %+v, %v", byID, err)
	}
	if _, err := s.ServerByID(ctx, org, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ServerByID(unknown) = %v", err)
	}
	got, err := s.ServerByAlias(ctx, org, "notion")
	if err != nil || got.Config.Auth != AuthOAuth || got.Config.OAuth == nil || got.Config.OAuth.TokenEndpoint != "https://as.example/token" {
		t.Fatalf("stored config %+v %v", got.Config, err)
	}
	// A dynamic registration replaces the client id and its secret.
	o := *got.Config.OAuth
	o.ClientID, o.Registration, o.HasClientSecret = "dcr-client", RegistrationDynamic, true
	sec := Credential{ID: "cs1", ServerID: srv, Purpose: PurposeOAuthClient, KeyVersion: 1, RowVersion: 1, Ciphertext: []byte("sealed-secret")}
	if err := s.UpdateServerOAuth(ctx, org, srv, o, &sec); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ServerByAlias(ctx, org, "notion")
	if got.Config.OAuth.ClientID != "dcr-client" || got.Config.OAuth.Registration != RegistrationDynamic {
		t.Fatalf("after update: %+v", got.Config.OAuth)
	}
	// Registering again replaces the old secret; registering without one drops it.
	sec2 := sec
	sec2.ID = "cs2"
	_ = s.UpdateServerOAuth(ctx, org, srv, o, &sec2)
	creds, _ := s.ServerCredentials(ctx, org, srv)
	if len(creds) != 1 || creds[0].ID != "cs2" {
		t.Fatalf("client secrets = %+v, want only cs2", creds)
	}
	o.HasClientSecret = false
	_ = s.UpdateServerOAuth(ctx, org, srv, o, nil)
	if creds, _ := s.ServerCredentials(ctx, org, srv); len(creds) != 0 {
		t.Fatalf("client secret kept: %+v", creds)
	}
	if err := s.UpdateServerOAuth(ctx, org, "no-such", o, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}
}

func TestConnectStateCarriesUserSessionServerIssuer(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	st := OAuthState{Hash: "h", Purpose: PurposeConnect, Verifier: "v", UserID: "u1", SessionID: "sess", ServerID: "srv", Issuer: "https://as.example", ReturnTo: "/connect/x", ExpiresAt: t0.Add(10 * time.Minute)}
	if err := s.CreateOAuthState(ctx, org, st, t0, 500); err != nil {
		t.Fatal(err)
	}
	inv := OAuthState{Hash: "h2", Purpose: PurposeInvite, Verifier: "v", InviteID: "i1", ExpiresAt: t0.Add(10 * time.Minute)}
	if err := s.CreateOAuthState(ctx, org, inv, t0, 500); err != nil {
		t.Fatal(err)
	}
	got, err := s.UseOAuthState(ctx, org, "h", t0)
	if err != nil || got.UserID != "u1" || got.SessionID != "sess" || got.ServerID != "srv" || got.Issuer != "https://as.example" || got.Purpose != PurposeConnect || got.ReturnTo != "/connect/x" {
		t.Fatalf("connect state %+v %v", got, err)
	}
	if got, err := s.UseOAuthState(ctx, org, "h2", t0); err != nil || got.InviteID != "i1" {
		t.Fatalf("invite state %+v %v", got, err)
	}
	if err := s.CreateOAuthState(ctx, org, OAuthState{Hash: "h3", Purpose: "steal", Verifier: "v", ExpiresAt: t0.Add(time.Minute)}, t0, 500); err == nil {
		t.Error("unknown purpose accepted")
	}
}

func TestPruneRemovesDeadInvites(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	newInvite(t, s, org, "old", "oh", "ada", RoleMember, t0)
	newInvite(t, s, org, "live", "lh", "bob", RoleMember, t0.Add(71*time.Hour))
	if err := s.PruneSignIn(ctx, org, t0.Add(73*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.r.QueryRow(`SELECT count(*) FROM invites WHERE org_id = ?`, org).Scan(&n)
	if n != 1 {
		t.Fatalf("%d invites left, want the live one", n)
	}
}

func TestUpstreamOAuthTablesIsolatedPerOrg(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	srv := oauthServer(t, s, org, "linear")
	ada := member(t, s, org, "2001", "ada", RoleMember)
	_, _ = s.SaveGrant(ctx, org, grantFor(srv, ada.ID, "c1"), false, adminEv("a"))

	if _, err := s.Invite(ctx, other.ID, "ih-2001", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("invite visible from another org: %v", err)
	}
	if _, err := s.Grant(ctx, other.ID, srv, ada.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("grant visible from another org: %v", err)
	}
	if list, _ := s.ListGrants(ctx, other.ID, ada.ID); len(list) != 0 {
		t.Errorf("grants listed in another org: %+v", list)
	}
	if err := s.DeleteGrant(ctx, other.ID, srv, ada.ID, adminEv("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org deleted a grant: %v", err)
	}
	if _, err := s.SaveGrant(ctx, other.ID, grantFor(srv, ada.ID, "c9"), false, adminEv("y")); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org saved a grant for this org's server: %v", err)
	}
	if _, err := s.UserByPerson(ctx, other.ID, "ada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("user visible from another org: %v", err)
	}
}

func TestUpstreamOAuthSchemaHasNoSecretColumns(t *testing.T) {
	s, _ := signinOrg(t)
	for _, table := range []string{"invites", "upstream_grants", "oauth_states"} {
		rows, err := s.r.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var col string
			_ = rows.Scan(&col)
			n++
			for _, bad := range []string{"token", "access", "refresh_token", "client_secret", "password", "code", "verifier_plain"} {
				if strings.Contains(col, bad) {
					t.Errorf("%s.%s looks like it holds a secret", table, col)
				}
			}
		}
		_ = rows.Close()
		if n == 0 {
			t.Errorf("table %s does not exist", table)
		}
	}
}
