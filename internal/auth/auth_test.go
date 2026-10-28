package auth

import (
	"context"
	"errors"
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
