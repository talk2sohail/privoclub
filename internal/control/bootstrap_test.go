package control

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
)

func newOrg(t *testing.T) (*store.Store, store.OrgID) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(context.Background(), "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	return st, org.ID
}

func TestBootstrapToken(t *testing.T) {
	t.Run("installs an owner control token that authenticates", func(t *testing.T) {
		ctx := context.Background()
		st, org := newOrg(t)
		tok, err := token.New()
		if err != nil {
			t.Fatal(err)
		}
		res, rec, err := InstallBootstrapToken(ctx, st, org, tok.Plain, time.Now())
		if err != nil || res != store.BootstrapInstalled {
			t.Fatalf("install = %v, %v", res, err)
		}
		if rec.ID != tok.ID || rec.Name != BootstrapTokenName {
			t.Fatalf("record %+v", rec)
		}
		p, exp, err := auth.New(st).Verify(ctx, tok.Plain)
		if err != nil {
			t.Fatalf("bootstrap token does not authenticate: %v", err)
		}
		if !p.Has(store.RoleOwner) || p.Kind != store.KindControl || p.OrgID != org || p.Name != BootstrapTokenName {
			t.Fatalf("principal %+v", p)
		}
		if d := time.Until(exp); d < 23*time.Hour || d > 24*time.Hour+time.Minute {
			t.Fatalf("expires in %s, want about 24h", d)
		}
	})

	t.Run("only the hash of the secret is stored", func(t *testing.T) {
		ctx := context.Background()
		st, org := newOrg(t)
		tok, _ := token.New()
		if _, _, err := InstallBootstrapToken(ctx, st, org, tok.Plain, time.Now()); err != nil {
			t.Fatal(err)
		}
		_, secret, _ := token.Parse(tok.Plain)
		got, err := st.TokenByID(ctx, tok.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.SecretHash != token.HashSecret(secret) || strings.Contains(got.SecretHash, secret) {
			t.Fatalf("stored hash %q", got.SecretHash)
		}
	})

	t.Run("it is a control token, never accepted on /mcp", func(t *testing.T) {
		ctx := context.Background()
		st, org := newOrg(t)
		tok, _ := token.New()
		if _, _, err := InstallBootstrapToken(ctx, st, org, tok.Plain, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.New(st).MCPVerifier()(ctx, tok.Plain, nil); err == nil {
			t.Fatal("control token accepted by the gateway verifier")
		}
	})

	t.Run("a malformed token is rejected without echoing it", func(t *testing.T) {
		ctx := context.Background()
		st, org := newOrg(t)
		bad := "privo_pat_NOT-A-REAL-SECRET-marker-7731"
		_, _, err := InstallBootstrapToken(ctx, st, org, bad, time.Now())
		if !errors.Is(err, token.ErrMalformed) {
			t.Fatalf("err = %v, want ErrMalformed", err)
		}
		if strings.Contains(err.Error(), "marker-7731") {
			t.Fatalf("error repeats the input: %v", err)
		}
		if list, _ := st.ListTokens(ctx, org); len(list) != 0 {
			t.Fatal("token stored from malformed input")
		}
	})

	t.Run("installs at most once, even after revocation", func(t *testing.T) {
		ctx := context.Background()
		st, org := newOrg(t)
		tok, _ := token.New()
		if _, _, err := InstallBootstrapToken(ctx, st, org, tok.Plain, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := st.RevokeToken(ctx, org, tok.ID); err != nil {
			t.Fatal(err)
		}
		res, _, err := InstallBootstrapToken(ctx, st, org, tok.Plain, time.Now())
		if err != nil || res != store.BootstrapConsumed {
			t.Fatalf("reinstall = %v, %v", res, err)
		}
		if _, _, err := auth.New(st).Verify(ctx, tok.Plain); err == nil {
			t.Fatal("revoked bootstrap token authenticates again")
		}
	})

	t.Run("it expires 24 hours after install", func(t *testing.T) {
		ctx := context.Background()
		st, org := newOrg(t)
		tok, _ := token.New()
		long := time.Now().Add(-25 * time.Hour)
		if _, rec, err := InstallBootstrapToken(ctx, st, org, tok.Plain, long); err != nil || !rec.ExpiresAt.Equal(long.Add(BootstrapTokenTTL)) {
			t.Fatalf("expiry %v, %v", rec.ExpiresAt, err)
		}
		if _, _, err := auth.New(st).Verify(ctx, tok.Plain); err == nil {
			t.Fatal("expired bootstrap token authenticates")
		}
	})

	t.Run("is not needed once the org has a control token", func(t *testing.T) {
		ctx := context.Background()
		st, org := newOrg(t)
		if _, _, err := IssueToken(ctx, st, org, TokenSpec{Name: "owner", Role: store.RoleOwner, Kind: store.KindControl, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		tok, _ := token.New()
		res, _, err := InstallBootstrapToken(ctx, st, org, tok.Plain, time.Now())
		if err != nil || res != store.BootstrapNotNeeded {
			t.Fatalf("install = %v, %v", res, err)
		}
	})
}

// R-10 (independent review, 2026-10-29): the installer is the boundary of the
// control plane, so it refuses a token with no entropy itself and does not rely
// on its caller.
func TestInstallBootstrapTokenRefusesWeakToken(t *testing.T) {
	ctx := context.Background()
	st, org := newOrg(t)
	id, secret := "aaaaaaaaaaaaaaaa", strings.Repeat("a", 52)
	body := token.Prefix + id + "_" + secret
	plain := fmt.Sprintf("%s%08x", body, crc32.ChecksumIEEE([]byte(body)))
	if _, _, err := token.Parse(plain); err != nil {
		t.Fatalf("the test token must be well-formed: %v", err)
	}
	_, _, err := InstallBootstrapToken(ctx, st, org, plain, time.Now())
	if !errors.Is(err, token.ErrWeak) {
		t.Fatalf("err = %v, want token.ErrWeak", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error repeats the secret: %v", err)
	}
	if _, err := st.TokenByID(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the weak token was stored: %v", err)
	}
	if needed, _ := st.BootstrapNeeded(ctx, org); !needed {
		t.Fatal("a refused token used up the one install")
	}
}
