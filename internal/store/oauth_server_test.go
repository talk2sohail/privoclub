package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for the storage of Privo's own authorization server (F06, ADR 0022).

func clientGrant(id, userID string) ClientGrant {
	return ClientGrant{ID: id, UserID: userID, ClientID: "https://client.example/doc", ClientName: "Client", ClientKind: ClientKnown,
		Scope: "mcp", CreatedAt: t0, ExpiresAt: t0.Add(90 * 24 * time.Hour)}
}

func oauthTok(kind, sig, grant string, expires time.Time) OAuthToken {
	return OAuthToken{Kind: kind, Signature: sig, GrantID: grant, Request: []byte(`{"id":"` + grant + `"}`), ExpiresAt: expires}
}

// liveGrant makes a grant with an access and a refresh token.
func liveGrant(t *testing.T, s *Store, org OrgID, id, userID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateClientGrant(ctx, org, clientGrant(id, userID)); err != nil {
		t.Fatalf("CreateClientGrant %s: %v", id, err)
	}
	for _, tok := range []OAuthToken{oauthTok(OAuthAccess, "at-"+id, id, t0.Add(time.Hour)), oauthTok(OAuthRefresh, "rt-"+id, id, t0.Add(30*24*time.Hour))} {
		if err := s.PutOAuthToken(ctx, org, tok, t0); err != nil {
			t.Fatalf("PutOAuthToken %s: %v", tok.Signature, err)
		}
	}
}

func oauthRows(t *testing.T, s *Store, table string) (n int) {
	t.Helper()
	if err := s.r.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGrantBelongsToAMember(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	approver := member(t, s, org, "2", "bea", RoleApprover)
	auditor := member(t, s, org, "3", "cy", RoleAuditor)
	gone := member(t, s, org, "4", "dee", RoleMember)
	if err := s.DisableUser(ctx, org, gone.ID, t0, adminEv("user.disable")); err != nil {
		t.Fatal(err)
	}
	other, _ := s.EnsureOrg(ctx, "other", "Other")

	if err := s.CreateClientGrant(ctx, org, clientGrant("g1", ada.ID)); err != nil {
		t.Fatalf("a member's grant: %v", err)
	}
	// The same id again (the library stores the code and the PKCE session of
	// one request) changes nothing; another member cannot take the id.
	if err := s.CreateClientGrant(ctx, org, clientGrant("g1", ada.ID)); err != nil {
		t.Errorf("the same grant again: %v", err)
	}
	for name, g := range map[string]ClientGrant{
		"an approver":             clientGrant("g2", approver.ID),
		"an auditor":              clientGrant("g3", auditor.ID),
		"a disabled member":       clientGrant("g4", gone.ID),
		"nobody":                  clientGrant("g5", "no-such-user"),
		"another member, same id": clientGrant("g1", approver.ID),
	} {
		if err := s.CreateClientGrant(ctx, org, g); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if err := s.CreateClientGrant(ctx, other.ID, clientGrant("g6", ada.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("a member of another org: %v, want ErrNotFound", err)
	}
	if err := s.CreateClientGrant(ctx, org, ClientGrant{ID: "g7", ClientID: "c", Scope: "mcp"}); err == nil {
		t.Error("a grant without a member was accepted")
	}
	if n := oauthRows(t, s, "oauth_grants"); n != 1 {
		t.Fatalf("%d grants, want 1", n)
	}

	// A token needs its grant, in its org.
	if err := s.PutOAuthToken(ctx, org, oauthTok(OAuthAccess, "at", "g1", t0.Add(time.Hour)), t0); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOAuthToken(ctx, org, oauthTok(OAuthAccess, "at", "g1", t0.Add(time.Hour)), t0); !errors.Is(err, ErrConflict) {
		t.Errorf("the same signature twice: %v, want ErrConflict", err)
	}
	if err := s.PutOAuthToken(ctx, org, oauthTok(OAuthAccess, "at2", "no-grant", t0.Add(time.Hour)), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a token of no grant: %v, want ErrNotFound", err)
	}
	if err := s.PutOAuthToken(ctx, other.ID, oauthTok(OAuthAccess, "at3", "g1", t0.Add(time.Hour)), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a token for another org's grant: %v, want ErrNotFound", err)
	}
	tok, g, err := s.OAuthTokenBySignature(ctx, org, OAuthAccess, "at")
	if err != nil || !tok.Active || tok.GrantID != "g1" || g.UserID != ada.ID || g.ClientID != "https://client.example/doc" || g.Scope != "mcp" ||
		!g.ExpiresAt.Equal(t0.Add(90*24*time.Hour)) || string(tok.Request) != `{"id":"g1"}` {
		t.Fatalf("OAuthTokenBySignature = %+v, %+v, %v", tok, g, err)
	}
	// The kind is part of the key, and so is the org.
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "at"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an access signature as a refresh token: %v", err)
	}
	if _, _, err := s.OAuthTokenBySignature(ctx, other.ID, OAuthAccess, "at"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org's token: %v", err)
	}
}

func TestCodeWorksOnce(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	_ = s.CreateClientGrant(ctx, org, clientGrant("g1", ada.ID))
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthCode, "code", "g1", t0.Add(time.Minute)), t0)

	// Of many uses at once exactly one is the first.
	var wg sync.WaitGroup
	firsts := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first, err := s.DeactivateOAuthToken(ctx, org, OAuthCode, "code")
			if err != nil {
				t.Error(err)
			}
			firsts <- first
		}()
	}
	wg.Wait()
	close(firsts)
	n := 0
	for f := range firsts {
		if f {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d of 16 uses were the first, want 1", n)
	}
	// The used code stays, inactive, so a second use is recognised.
	tok, g, err := s.OAuthTokenBySignature(ctx, org, OAuthCode, "code")
	if err != nil || tok.Active || g.ID != "g1" {
		t.Errorf("the used code: %+v, %+v, %v", tok, g, err)
	}
	if first, _ := s.DeactivateOAuthToken(ctx, org, OAuthCode, "never-issued"); first {
		t.Error("an unknown code was used")
	}
}

func TestRefreshRotates(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	liveGrant(t, s, org, "g1", ada.ID)
	liveGrant(t, s, org, "g2", ada.ID)

	later := t0.Add(50 * time.Minute)
	if err := s.RotateRefreshToken(ctx, org, "g1", "rt-g1", later); err != nil {
		t.Fatal(err)
	}
	if tok, g, err := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "rt-g1"); err != nil || tok.Active || !g.LastUsedAt.Equal(later) {
		t.Errorf("the rotated refresh token: %+v, last used %v, %v", tok, g.LastUsedAt, err)
	}
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthAccess, "at-g1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the access token of the rotated grant: %v, want ErrNotFound", err)
	}
	// The other grant is untouched.
	if tok, _, err := s.OAuthTokenBySignature(ctx, org, OAuthAccess, "at-g2"); err != nil || !tok.Active {
		t.Errorf("another grant's access token: %+v, %v", tok, err)
	}
	// A second rotation of the same token loses.
	if err := s.RotateRefreshToken(ctx, org, "g1", "rt-g1", later); !errors.Is(err, ErrConflict) {
		t.Errorf("rotating a used refresh token: %v, want ErrConflict", err)
	}
}

// What the library does when a rotated refresh token is used again (ADR 0022
// point 9, no grace): it ends the grant. Everything of the grant goes.
func TestRotatedRefreshTokenIsRefusedAndRevokesTheGrant(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	liveGrant(t, s, org, "g1", ada.ID)
	liveGrant(t, s, org, "g2", ada.ID)
	_ = s.RotateRefreshToken(ctx, org, "g1", "rt-g1", t0)
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthAccess, "at-g1-2", "g1", t0.Add(time.Hour)), t0)
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthRefresh, "rt-g1-2", "g1", t0.Add(time.Hour)), t0)

	ended, err := s.EndClientGrant(ctx, org, "g1")
	if err != nil || ended.UserID != ada.ID || ended.ClientName != "Client" {
		t.Fatalf("EndClientGrant = %+v, %v", ended, err)
	}
	for _, sig := range []struct{ kind, sig string }{{OAuthRefresh, "rt-g1"}, {OAuthRefresh, "rt-g1-2"}, {OAuthAccess, "at-g1-2"}} {
		if _, _, err := s.OAuthTokenBySignature(ctx, org, sig.kind, sig.sig); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s of the ended grant: %v, want ErrNotFound", sig.sig, err)
		}
	}
	if err := s.PutOAuthToken(ctx, org, oauthTok(OAuthAccess, "late", "g1", t0.Add(time.Hour)), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a token for an ended grant: %v, want ErrNotFound", err)
	}
	if _, err := s.EndClientGrant(ctx, org, "g1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ending it twice: %v", err)
	}
	if n := oauthRows(t, s, "oauth_tokens"); n != 2 {
		t.Errorf("%d token rows left, want the 2 of the other grant", n)
	}
}

func TestRemovingAMemberRevokesTheirGrants(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	bob := member(t, s, org, "2", "bob", RoleMember)
	cy := member(t, s, org, "3", "cy", RoleAdmin)
	for _, u := range []User{ada, bob, cy} {
		liveGrant(t, s, org, "g-"+u.Person, u.ID)
	}
	live := func() (out []string) {
		gs, err := s.ListClientGrants(ctx, org, "", t0)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range gs {
			out = append(out, g.Person)
		}
		return out
	}

	// Removed.
	if err := s.DisableUser(ctx, org, ada.ID, t0, adminEv("user.disable")); err != nil {
		t.Fatal(err)
	}
	// A role that may not use the gateway; a role that may keeps its grants.
	if err := s.SetUserRole(ctx, org, bob.ID, RoleAuditor, adminEv("role:bob")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserRole(ctx, org, cy.ID, RoleMember, adminEv("role:cy")); err != nil {
		t.Fatal(err)
	}
	if got := live(); len(got) != 1 || got[0] != "cy" {
		t.Fatalf("grants left: %v, want only cy's", got)
	}
	if n := oauthRows(t, s, "oauth_tokens"); n != 2 {
		t.Errorf("%d token rows, want cy's 2", n)
	}
	// Giving the role back does not bring a grant back.
	_ = s.SetUserRole(ctx, org, bob.ID, RoleMember, adminEv("role:bob2"))
	if got := live(); len(got) != 1 {
		t.Errorf("grants after the role came back: %v", got)
	}
}

func TestListAndRevokeClientGrants(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	bob := member(t, s, org, "2", "bob", RoleMember)
	liveGrant(t, s, org, "ga", ada.ID)
	liveGrant(t, s, org, "gb", bob.ID)
	// Not shown: a consent whose code was never exchanged, a grant whose
	// refresh token was used up, and one past its 90 days.
	_ = s.CreateClientGrant(ctx, org, clientGrant("pending", ada.ID))
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthCode, "code", "pending", t0.Add(time.Minute)), t0)
	liveGrant(t, s, org, "spent", ada.ID)
	_, _ = s.DeactivateOAuthToken(ctx, org, OAuthRefresh, "rt-spent")
	old := clientGrant("old", ada.ID)
	old.ExpiresAt = t0.Add(time.Hour)
	_ = s.CreateClientGrant(ctx, org, old)
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthRefresh, "rt-old", "old", t0.Add(30*24*time.Hour)), t0)

	mine, err := s.ListClientGrants(ctx, org, ada.ID, t0.Add(2*time.Hour))
	if err != nil || len(mine) != 1 || mine[0].ID != "ga" || mine[0].Person != "ada" || mine[0].ClientName != "Client" {
		t.Fatalf("ada's clients = %+v, %v", mine, err)
	}
	all, _ := s.ListClientGrants(ctx, org, "", t0.Add(2*time.Hour))
	if len(all) != 2 {
		t.Fatalf("everyone's clients: %d, want 2", len(all))
	}

	// A member ends only their own.
	if _, err := s.RevokeClientGrant(ctx, org, "gb", ada.ID, adminEv("oauth.grant.revoke:1")); !errors.Is(err, ErrNotFound) {
		t.Errorf("ada revoking bob's client: %v, want ErrNotFound", err)
	}
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthAccess, "at-gb"); err != nil {
		t.Errorf("bob's token after ada's attempt: %v", err)
	}
	g, err := s.RevokeClientGrant(ctx, org, "ga", ada.ID, adminEv("oauth.grant.revoke:2"))
	if err != nil || g.UserID != ada.ID {
		t.Fatalf("ada revoking her own: %+v, %v", g, err)
	}
	// An administrator ends anyone's; the audit row is written with it.
	if _, err := s.RevokeClientGrant(ctx, org, "gb", "", adminEv("oauth.grant.revoke:3")); err != nil {
		t.Fatal(err)
	}
	events, err := s.RecentAdminEvents(ctx, org, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if strings.HasPrefix(ev.Action, "oauth.grant.revoke:") && ev.Client == "Client" && (ev.UserID == ada.ID || ev.UserID == bob.ID) {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d admin audit events naming the client and the member, want 2", n)
	}
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if _, err := s.RevokeClientGrant(ctx, other.ID, "spent", "", adminEv("oauth.grant.revoke:4")); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org's admin: %v", err)
	}
}

func TestOAuthTransactionCommitsRollsBackAndLetsGo(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	liveGrant(t, s, org, "g1", ada.ID)

	// Rolled back: nothing of it stays, and a read inside saw its own write.
	tx, err := s.BeginOAuthTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginOAuthTx(tx); err == nil {
		t.Error("a transaction inside a transaction was opened")
	}
	if err := s.RotateRefreshToken(tx, org, "g1", "rt-g1", t0); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := s.OAuthTokenBySignature(tx, org, OAuthRefresh, "rt-g1"); err != nil || tok.Active {
		t.Errorf("inside the transaction the token is %+v, %v", tok, err)
	}
	if tok, _, err := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "rt-g1"); err != nil || !tok.Active {
		t.Errorf("outside the transaction the token is %+v, %v", tok, err)
	}
	if err := s.RollbackOAuthTx(tx); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackOAuthTx(tx); err != nil {
		t.Errorf("rolling back twice: %v", err)
	}
	if tok, _, _ := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "rt-g1"); !tok.Active {
		t.Error("a rolled-back rotation stayed")
	}
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthAccess, "at-g1"); err != nil {
		t.Errorf("the access token after the rollback: %v", err)
	}

	// Committed.
	tx, _ = s.BeginOAuthTx(ctx)
	_ = s.RotateRefreshToken(tx, org, "g1", "rt-g1", t0)
	_ = s.PutOAuthToken(tx, org, oauthTok(OAuthRefresh, "rt-new", "g1", t0.Add(time.Hour)), t0)
	if err := s.CommitOAuthTx(tx); err != nil {
		t.Fatal(err)
	}
	if tok, _, err := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "rt-new"); err != nil || !tok.Active {
		t.Errorf("after the commit: %+v, %v", tok, err)
	}
	if err := s.CommitOAuthTx(ctx); err == nil {
		t.Error("a commit without a transaction succeeded")
	}

	// Forgotten: when the request's context ends, the single writer is free
	// again. Without that one lost transaction would stop every write.
	reqCtx, cancel := context.WithCancel(ctx)
	if _, err := s.BeginOAuthTx(reqCtx); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- s.CreateClientGrant(ctx, org, clientGrant("g2", ada.ID)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a write after an abandoned transaction: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an abandoned transaction still holds the writer")
	}
}

func TestStoredClients(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	ev := adminEv("oauth.client.approve")
	approved := OAuthClient{ClientID: "https://tool.example/client.json", Kind: ClientApproved, Name: "Tool", CreatedAt: t0, CreatedBy: ada.ID}
	if err := s.SaveOAuthClient(ctx, org, approved, 0, &ev); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOAuthClient(ctx, org, approved, 0, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("approving twice: %v, want ErrConflict", err)
	}
	if err := s.SaveOAuthClient(ctx, org, OAuthClient{ClientID: "x", Kind: ClientKnown}, 0, nil); err == nil {
		t.Error("a known client was stored")
	}
	// Registered clients are bounded in total; approved ones do not count.
	for i := 0; i < 3; i++ {
		c := OAuthClient{ClientID: fmt.Sprint("reg-", i), Kind: ClientRegistered, Name: "Self", RedirectURIs: []string{"http://127.0.0.1/cb"}, CreatedAt: t0.Add(time.Duration(i) * time.Minute)}
		if err := s.SaveOAuthClient(ctx, org, c, 3, nil); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	// At the bound, the oldest registration nobody allowed makes room (review
	// of 2026-11-15, S-01): reg-0 has a grant, so reg-1 goes.
	allowed := clientGrant("g-allowed", ada.ID)
	allowed.ClientID, allowed.ClientKind = "reg-0", ClientRegistered
	if err := s.CreateClientGrant(ctx, org, allowed); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOAuthClient(ctx, org, OAuthClient{ClientID: "reg-3", Kind: ClientRegistered, CreatedAt: t0.Add(time.Hour)}, 3, nil); err != nil {
		t.Fatalf("the fourth registration: %v", err)
	}
	if _, err := s.OAuthClient(ctx, org, "reg-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the oldest unallowed registration is still there: %v", err)
	}
	// When every registration has been allowed, there is no room.
	for _, id := range []string{"reg-2", "reg-3"} {
		g := clientGrant("g-"+id, ada.ID)
		g.ClientID, g.ClientKind = id, ClientRegistered
		if err := s.CreateClientGrant(ctx, org, g); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveOAuthClient(ctx, org, OAuthClient{ClientID: "reg-4", Kind: ClientRegistered, CreatedAt: t0}, 3, nil); !errors.Is(err, ErrTooManyClients) {
		t.Errorf("a registration when all are allowed: %v, want ErrTooManyClients", err)
	}
	got, err := s.OAuthClient(ctx, org, "reg-0")
	if err != nil || got.Kind != ClientRegistered || len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "http://127.0.0.1/cb" || got.AuthorizedAt != nil {
		t.Fatalf("OAuthClient = %+v, %v", got, err)
	}
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if _, err := s.OAuthClient(ctx, other.ID, "reg-0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org's client: %v", err)
	}

	// Deleting a client ends its grants and leaves the others.
	g := clientGrant("g-reg", ada.ID)
	g.ClientID = "reg-0"
	_ = s.CreateClientGrant(ctx, org, g)
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthRefresh, "rt-reg", "g-reg", t0.Add(time.Hour)), t0)
	_ = s.MarkOAuthClientAuthorized(ctx, org, "reg-0", t0)
	liveGrant(t, s, org, "g-other", ada.ID)
	list, _ := s.ListOAuthClients(ctx, org)
	if len(list) != 4 {
		t.Fatalf("%d stored clients, want 4", len(list))
	}
	for _, c := range list {
		if want := map[string]int{"reg-0": 2, "reg-2": 1, "reg-3": 1}[c.ClientID]; c.Grants != want {
			t.Errorf("%s: %d grants, want %d", c.ClientID, c.Grants, want)
		}
	}
	if n, err := s.DeleteOAuthClient(ctx, org, "reg-0", adminEv("oauth.client.delete")); err != nil || n != 2 {
		t.Fatalf("DeleteOAuthClient = %d, %v", n, err)
	}
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "rt-reg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the deleted client's token: %v", err)
	}
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "rt-g-other"); err != nil {
		t.Errorf("another client's token: %v", err)
	}
	if _, err := s.DeleteOAuthClient(ctx, org, "reg-0", adminEv("oauth.client.delete2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	// A client without a row (known, or any document) can lose its grants too.
	if n, err := s.EndClientGrantsOfClient(ctx, org, "https://client.example/doc", adminEv("oauth.client.revoke")); err != nil || n != 1 {
		t.Errorf("EndClientGrantsOfClient = %d, %v", n, err)
	}
}

func TestPruneOAuth(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	ada := member(t, s, org, "1", "ada", RoleMember)
	liveGrant(t, s, org, "live", ada.ID)
	// A consent whose code ran out unexchanged: the grant goes with it.
	_ = s.CreateClientGrant(ctx, org, clientGrant("unused", ada.ID))
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthCode, "code", "unused", t0.Add(time.Minute)), t0)
	// A grant past its 90 days, with a refresh token that would still run.
	old := clientGrant("old", ada.ID)
	old.ExpiresAt = t0.Add(time.Hour)
	_ = s.CreateClientGrant(ctx, org, old)
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthRefresh, "rt-old", "old", t0.Add(30*24*time.Hour)), t0)
	// Registrations: never allowed and an hour old; never allowed and new;
	// allowed long ago with no grant; allowed long ago but with a grant.
	for id, created := range map[string]time.Time{"stale": t0, "fresh": t0.Add(90 * time.Minute), "idle": t0, "used": t0} {
		_ = s.SaveOAuthClient(ctx, org, OAuthClient{ClientID: id, Kind: ClientRegistered, CreatedAt: created}, 0, nil)
	}
	_ = s.MarkOAuthClientAuthorized(ctx, org, "idle", t0.Add(-100*24*time.Hour))
	_ = s.MarkOAuthClientAuthorized(ctx, org, "used", t0.Add(-100*24*time.Hour))
	g := clientGrant("g-used", ada.ID)
	g.ClientID = "used"
	_ = s.CreateClientGrant(ctx, org, g)
	_ = s.PutOAuthToken(ctx, org, oauthTok(OAuthRefresh, "rt-used", "g-used", t0.Add(30*24*time.Hour)), t0)
	_ = s.SaveOAuthClient(ctx, org, OAuthClient{ClientID: "https://approved.example/c", Kind: ClientApproved, CreatedAt: t0.Add(-200 * 24 * time.Hour)}, 0, nil)

	now := t0.Add(2 * time.Hour)
	if err := s.PruneOAuth(ctx, org, now, time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var grants []string
	rows, _ := s.r.Query(`SELECT id FROM oauth_grants ORDER BY id`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		grants = append(grants, id)
	}
	_ = rows.Close()
	if fmt.Sprint(grants) != "[g-used live]" {
		t.Errorf("grants after the prune: %v, want [g-used live]", grants)
	}
	// The access token of the live grant ran out (one hour) and is gone; its
	// refresh token stays.
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthAccess, "at-live"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired access token after the prune: %v", err)
	}
	if _, _, err := s.OAuthTokenBySignature(ctx, org, OAuthRefresh, "rt-live"); err != nil {
		t.Errorf("a live refresh token after the prune: %v", err)
	}
	var clients []string
	list, _ := s.ListOAuthClients(ctx, org)
	for _, c := range list {
		clients = append(clients, c.ClientID)
	}
	if fmt.Sprint(clients) != "[https://approved.example/c fresh used]" {
		t.Errorf("clients after the prune: %v, want the approved one, fresh and used", clients)
	}
}

func TestOAuthSigningKeyIsStoredOnce(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	if _, _, err := s.OAuthSigningKey(ctx, org); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before any key: %v", err)
	}
	if err := s.CreateOAuthSigningKey(ctx, org, 1, []byte("sealed-first"), t0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthSigningKey(ctx, org, 2, []byte("sealed-second"), t0); err != nil {
		t.Fatal(err)
	}
	v, sealed, err := s.OAuthSigningKey(ctx, org)
	if err != nil || v != 1 || string(sealed) != "sealed-first" {
		t.Errorf("OAuthSigningKey = %d, %q, %v; want the first one stored", v, sealed, err)
	}
}

func TestGatewayRole(t *testing.T) {
	for role, want := range map[string]bool{RoleOwner: true, RoleAdmin: true, RoleMember: true, RoleApprover: false, RoleAuditor: false, "": false, "root": false} {
		if GatewayRole(role) != want {
			t.Errorf("GatewayRole(%q) = %v", role, !want)
		}
	}
}
