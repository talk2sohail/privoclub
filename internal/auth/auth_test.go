package auth

import (
	"context"
	"errors"
	"reflect"
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
	if err != nil || p.OrgID != "org" || !p.Has(store.RoleMember) || len(p.Roles) != 1 {
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
	member := Principal{Roles: []string{store.RoleMember}}
	if !member.Can(PermUseGateway) || member.Can(PermManageServers) || member.Can(PermReadAudit) || member.Can(PermManageAudit) {
		t.Fatal("member permissions wrong")
	}
	auditor := Principal{Roles: []string{store.RoleAuditor}}
	if !auditor.Can(PermReadAudit) || auditor.Can(PermManageAudit) || auditor.Can(PermUseGateway) {
		t.Fatal("auditor permissions wrong")
	}
	approver := Principal{Roles: []string{store.RoleApprover}}
	if !approver.Can(PermDecide) || approver.Can(PermManagePolicy) || approver.Can(PermManageServers) || approver.Can(PermUseGateway) {
		t.Fatal("approver permissions wrong")
	}
	if member.Can(PermDecide) || member.Can(PermManagePolicy) {
		t.Fatal("member can decide approvals or manage policy")
	}
	if (Principal{Roles: []string{"nobody"}}).Can(PermUseGateway) {
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

// F07 review R-01: the member behind a token is the user it is bound to, never
// the person name it carries.
func TestPrincipalCarriesTokenUser(t *testing.T) {
	f := &fakeStore{tokens: map[string]store.APIToken{}}
	a := New(f)
	ctx := context.Background()
	own, _ := token.New()
	f.tokens[own.ID] = store.APIToken{OrgID: "org", ID: own.ID, Name: "laptop", Role: store.RoleMember, Kind: store.KindGateway,
		Person: "ada", UserID: "u-ada", SecretHash: own.SecretHash, ExpiresAt: time.Now().Add(time.Hour)}
	named, _ := token.New()
	f.tokens[named.ID] = store.APIToken{OrgID: "org", ID: named.ID, Name: "ada", Role: store.RoleMember, Kind: store.KindGateway,
		Person: "ada", SecretHash: named.SecretHash, ExpiresAt: time.Now().Add(time.Hour)}

	if p, _, err := a.Verify(ctx, own.Plain); err != nil || p.UserID != "u-ada" || p.ActorID() != own.ID {
		t.Fatalf("own token: %+v %v", p, err)
	}
	if p, _, err := a.Verify(ctx, named.Plain); err != nil || p.UserID != "" || p.Person != "ada" {
		t.Fatalf("a token that only names ada: %+v %v", p, err)
	}
}

// ADR 0021: a person's permissions are the union of their roles.
func TestCanIsUnionOfRoles(t *testing.T) {
	both := Principal{Roles: []string{store.RoleApprover, store.RoleMember}}
	if !both.Can(PermUseGateway) || !both.Can(PermDecide) || both.Can(PermManageServers) || both.Can(PermReadAudit) {
		t.Fatal("member and approver: want the gateway and deciding, nothing else")
	}
	if !both.Has(store.RoleApprover) || both.Has(store.RoleAdmin) {
		t.Fatal("Has")
	}
	if a := (Principal{Roles: []string{store.RoleAuditor, store.RoleMember}}); !a.Can(PermReadAudit) || a.Can(PermManageAudit) {
		t.Fatal("auditor and member")
	}
}

func TestNoRolesNoPermissions(t *testing.T) {
	for _, p := range []Principal{{}, {Roles: []string{}}, {Roles: []string{""}}, {Roles: []string{"root", "superuser"}}} {
		for _, perm := range []Permission{PermUseGateway, PermManageServers, PermManageTokens, PermReadAudit, PermManageAudit,
			PermReadServers, PermManagePolicy, PermDecide, PermManageMembers} {
			if p.Can(perm) {
				t.Errorf("%q can %s", p.Roles, perm)
			}
		}
	}
}

func TestApproverAloneCannotUseGateway(t *testing.T) {
	if (Principal{Roles: []string{store.RoleApprover}}).Can(PermUseGateway) {
		t.Fatal("an approver alone uses the gateway")
	}
	if !(Principal{Roles: []string{store.RoleApprover, store.RoleMember}}).Can(PermUseGateway) {
		t.Fatal("an approver who is also a member cannot use the gateway")
	}
}

// Principal and User carry a set of roles, never one: a single-role field
// brought back would let code compare one role again (ADR 0021).
func TestNoSingleRoleField(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Principal{}), reflect.TypeOf(store.User{})} {
		if _, ok := typ.FieldByName("Role"); ok {
			t.Errorf("%s has a Role field; use Roles with Has and Can", typ)
		}
	}
}
