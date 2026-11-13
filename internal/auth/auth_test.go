package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
)

type fakeStore struct{ tokens map[string]store.APIToken }

func (f *fakeStore) TokenByID(_ context.Context, id string) (store.APIToken, error) {
	t, ok := f.tokens[id]
	if !ok {
		return t, store.ErrNotFound
	}
	return t, nil
}

func issue(f *fakeStore, role string, exp time.Time) string {
	tok, _ := token.New()
	f.tokens[tok.ID] = store.APIToken{OrgID: "org", ID: tok.ID, Name: "n", Role: role, Kind: store.KindGateway, SecretHash: tok.SecretHash, ExpiresAt: exp}
	return tok.Plain
}

func TestVerify(t *testing.T) {
	f := &fakeStore{tokens: map[string]store.APIToken{}}
	a := New(f)
	ctx := context.Background()
	plain := issue(f, store.RoleMember, time.Now().Add(time.Hour))

	p, _, err := a.Verify(ctx, plain)
	if err != nil || p.OrgID != "org" || p.Role != store.RoleMember {
		t.Fatalf("Verify: %+v %v", p, err)
	}
	last := plain[len(plain)-1]
	changed := byte('0')
	if last == '0' {
		changed = '1'
	}
	for _, bad := range []string{"", "privo_pat_x", plain[:len(plain)-1] + string(changed), "Bearer " + plain} {
		if _, _, err := a.Verify(ctx, bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%q accepted", bad)
		}
	}
	expired := issue(f, store.RoleOwner, time.Now().Add(-time.Second))
	if _, _, err := a.Verify(ctx, expired); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestRevocationTakesEffectWithinCacheTTL(t *testing.T) {
	f := &fakeStore{tokens: map[string]store.APIToken{}}
	a := New(f)
	now := time.Now()
	a.now = func() time.Time { return now }
	ctx := context.Background()
	plain := issue(f, store.RoleMember, now.Add(time.Hour))
	id, _, _ := token.Parse(plain)

	if _, _, err := a.Verify(ctx, plain); err != nil {
		t.Fatal(err)
	}
	rev := now
	tok := f.tokens[id]
	tok.RevokedAt = &rev
	f.tokens[id] = tok

	now = now.Add(CacheTTL + time.Millisecond)
	if _, _, err := a.Verify(ctx, plain); err == nil {
		t.Fatal("revoked token still accepted after the cache lifetime")
	}
	if CacheTTL > 5*time.Second {
		t.Fatal("cache lifetime breaks the 5-second revocation promise")
	}
}

func TestPermissions(t *testing.T) {
	member := Principal{Role: store.RoleMember}
	if !member.Can(PermUseGateway) || member.Can(PermManageServers) || member.Can(PermReadAudit) || member.Can(PermManageAudit) {
		t.Fatal("member permissions wrong")
	}
	auditor := Principal{Role: store.RoleAuditor}
	if !auditor.Can(PermReadAudit) || auditor.Can(PermManageAudit) || auditor.Can(PermUseGateway) {
		t.Fatal("auditor permissions wrong")
	}
	approver := Principal{Role: store.RoleApprover}
	if !approver.Can(PermDecide) || approver.Can(PermManagePolicy) || approver.Can(PermManageServers) || approver.Can(PermUseGateway) {
		t.Fatal("approver permissions wrong")
	}
	if member.Can(PermDecide) || member.Can(PermManagePolicy) {
		t.Fatal("member can decide approvals or manage policy")
	}
	if (Principal{Role: "nobody"}).Can(PermUseGateway) {
		t.Fatal("unknown role granted access")
	}
}

func TestTokenKindsAreSeparated(t *testing.T) {
	f := &fakeStore{tokens: map[string]store.APIToken{}}
	a := New(f)
	verify := a.MCPVerifier()
	ctx := context.Background()
	gw := issue(f, store.RoleOwner, time.Now().Add(time.Hour))
	if _, err := verify(ctx, gw, nil); err != nil {
		t.Fatalf("gateway token refused on /mcp: %v", err)
	}
	ctl, _ := token.New()
	f.tokens[ctl.ID] = store.APIToken{OrgID: "org", ID: ctl.ID, Name: "c", Role: store.RoleOwner, Kind: store.KindControl, SecretHash: ctl.SecretHash, ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := verify(ctx, ctl.Plain, nil); err == nil {
		t.Fatal("control token accepted on /mcp")
	}
}

// F27: the principal carries the member a token belongs to, taken from the
// token's user id. The person label alone never makes a principal a member.
func TestVerifyCarriesTheTokensMember(t *testing.T) {
	f := &fakeStore{tokens: map[string]store.APIToken{}}
	a := New(f)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)

	bound, _ := token.New()
	f.tokens[bound.ID] = store.APIToken{OrgID: "org", ID: bound.ID, Name: "laptop", Role: store.RoleMember, Kind: store.KindGateway, Person: "ada", UserID: "u-ada", SecretHash: bound.SecretHash, ExpiresAt: exp}
	p, _, err := a.Verify(ctx, bound.Plain)
	if err != nil || p.UserID != "u-ada" || p.Person != "ada" {
		t.Fatalf("bound token: %+v %v", p, err)
	}
	// Audit rows name a token by its id, also when it belongs to a member.
	if p.ActorID() != bound.ID {
		t.Fatalf("actor id %q, want the token id", p.ActorID())
	}
	// Cached answers carry it too.
	if p, _, _ := a.Verify(ctx, bound.Plain); p.UserID != "u-ada" {
		t.Fatalf("cached principal lost the member: %+v", p)
	}

	label, _ := token.New()
	f.tokens[label.ID] = store.APIToken{OrgID: "org", ID: label.ID, Name: "ci", Role: store.RoleMember, Kind: store.KindGateway, Person: "ada", SecretHash: label.SecretHash, ExpiresAt: exp}
	p, _, err = a.Verify(ctx, label.Plain)
	if err != nil || p.UserID != "" || p.Person != "ada" {
		t.Fatalf("a token that only names ada: %+v %v", p, err)
	}
}

// ---- OAuth access tokens (ADR 0022)

// fakeOAuth stands in for Privo's authorization server.
type fakeOAuth struct {
	grants map[string]store.ClientGrant // by bearer
	users  map[string]store.User        // by user id
	exp    time.Time
	calls  int
}

func (f *fakeOAuth) Recognizes(bearer string) bool { return strings.HasPrefix(bearer, "privo_at_") }

func (f *fakeOAuth) VerifyAccessToken(_ context.Context, bearer string) (store.ClientGrant, store.User, time.Time, error) {
	f.calls++
	g, ok := f.grants[bearer]
	u, live := f.users[g.UserID]
	if !ok || !live {
		return store.ClientGrant{}, store.User{}, time.Time{}, errors.New("no")
	}
	return g, u, f.exp, nil
}

func newOAuthAuth() (*Authenticator, *fakeOAuth, *time.Time) {
	now := time.Unix(1_800_000_000, 0)
	f := &fakeOAuth{exp: now.Add(time.Hour),
		grants: map[string]store.ClientGrant{"privo_at_key.sig": {ID: "g1", UserID: "u-ada", ClientID: "https://c.example/doc", ClientName: "Claude Code"}},
		users:  map[string]store.User{"u-ada": {OrgID: "org1", ID: "u-ada", Person: "ada", Role: store.RoleMember}}}
	a := New(&fakeStore{tokens: map[string]store.APIToken{}})
	a.now = func() time.Time { return now }
	a.AcceptOAuth(f)
	return a, f, &now
}

func TestOAuthCallerIsTheMemberWhoAllowedTheClient(t *testing.T) {
	a, _, _ := newOAuthAuth()
	p, exp, err := a.VerifyGateway(context.Background(), "privo_at_key.sig")
	if err != nil {
		t.Fatal(err)
	}
	want := Principal{OrgID: "org1", TokenID: "oauth:g1", Name: "Claude Code", Role: store.RoleMember, Kind: store.KindGateway, Person: "ada", UserID: "u-ada", Client: "Claude Code"}
	if p != want {
		t.Errorf("principal = %+v\nwant        %+v", p, want)
	}
	if !exp.Equal(time.Unix(1_800_000_000, 0).Add(time.Hour)) {
		t.Errorf("expiry = %v", exp)
	}
	if ti, err := a.MCPVerifier()(context.Background(), "privo_at_key.sig", nil); err != nil || ti == nil {
		t.Errorf("the gateway's verifier: %v", err)
	}
}

// ADR 0022 point 4: an access token is for /mcp. Verify is what the admin
// API calls, and it never takes one, whatever role the member has.
func TestAccessTokenWorksOnlyOnTheGateway(t *testing.T) {
	a, f, _ := newOAuthAuth()
	f.users["u-ada"] = store.User{OrgID: "org1", ID: "u-ada", Person: "ada", Role: store.RoleOwner}
	if _, _, err := a.Verify(context.Background(), "privo_at_key.sig"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Verify took an OAuth access token: %v", err)
	}
	if f.calls != 0 {
		t.Error("Verify asked the authorization server")
	}
	p, _, err := a.VerifyGateway(context.Background(), "privo_at_key.sig")
	if err != nil || p.Kind != store.KindGateway {
		t.Fatalf("VerifyGateway: %+v, %v", p, err)
	}
	// With the server off the shape means nothing.
	off := New(&fakeStore{tokens: map[string]store.APIToken{}})
	if _, _, err := off.VerifyGateway(context.Background(), "privo_at_key.sig"); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("an access token with the server off: %v", err)
	}
	// An unknown or dead access token, and one whose member may not use the
	// gateway any more (the server refuses those itself).
	if _, _, err := a.VerifyGateway(context.Background(), "privo_at_other.sig"); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("an unknown access token: %v", err)
	}
}

// A revocation, a removal and a role change count within the cache's two
// seconds, as for personal tokens, and never later.
func TestOAuthVerificationIsCachedForTwoSeconds(t *testing.T) {
	a, f, now := newOAuthAuth()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, _, err := a.VerifyGateway(ctx, "privo_at_key.sig"); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 1 {
		t.Errorf("%d lookups for five calls within the cache time, want 1", f.calls)
	}
	// Promoted, then removed.
	f.users["u-ada"] = store.User{OrgID: "org1", ID: "u-ada", Person: "ada", Role: store.RoleAdmin}
	*now = now.Add(CacheTTL)
	if p, _, err := a.VerifyGateway(ctx, "privo_at_key.sig"); err != nil || p.Role != store.RoleAdmin {
		t.Errorf("after the cache time the role is %q, %v", p.Role, err)
	}
	delete(f.users, "u-ada")
	if _, _, err := a.VerifyGateway(ctx, "privo_at_key.sig"); err != nil {
		t.Errorf("within the cache time: %v", err)
	}
	*now = now.Add(CacheTTL)
	if _, _, err := a.VerifyGateway(ctx, "privo_at_key.sig"); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("after the cache time a removed member's token: %v", err)
	}
	// The token's own time ends the cache early.
	a2, f2, now2 := newOAuthAuth()
	f2.exp = now2.Add(time.Second)
	_, _, _ = a2.VerifyGateway(ctx, "privo_at_key.sig")
	*now2 = now2.Add(time.Second)
	if _, _, err := a2.VerifyGateway(ctx, "privo_at_key.sig"); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("a token past its time, still in the cache: %v", err)
	}
}

// store.GatewayRole decides who may allow an AI client; it has to agree with
// the permission table here.
func TestGatewayRoleMatchesThePermissions(t *testing.T) {
	for _, role := range []string{store.RoleOwner, store.RoleAdmin, store.RoleMember, store.RoleApprover, store.RoleAuditor, "", "root"} {
		if got, want := store.GatewayRole(role), (Principal{Role: role}).Can(PermUseGateway); got != want {
			t.Errorf("role %q: store.GatewayRole = %v, the permission table says %v", role, got, want)
		}
	}
}
