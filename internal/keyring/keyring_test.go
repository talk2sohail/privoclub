package keyring

import (
	"bytes"
	"context"
	"testing"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

func setup(t *testing.T) (*store.Store, store.OrgID) {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	org, err := s.EnsureOrg(context.Background(), "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	return s, org.ID
}

func TestEnsureAndUnwrap(t *testing.T) {
	ctx := context.Background()
	kek, _ := vault.NewKey()
	s, org := setup(t)
	kr := New(s, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := kr.EnsureOrgKey(ctx, org); err != nil {
		t.Fatal("second EnsureOrgKey failed:", err)
	}
	v, dek, err := kr.Current(ctx, org)
	if err != nil || v != 1 || len(dek) != vault.KeySize {
		t.Fatalf("Current: v=%d len=%d err=%v", v, len(dek), err)
	}
	rec, _ := s.CurrentOrgKey(ctx, org)
	if rec.Provider != LocalProvider || bytes.Contains(rec.WrappedDEK, dek) {
		t.Fatal("stored key record is wrong or holds the plain DEK")
	}

	// A fresh keyring with the same KEK gets the same DEK.
	again, err := New(s, vault.StaticKeyProvider(kek, nil)).DEK(ctx, org, 1)
	if err != nil || !bytes.Equal(again, dek) {
		t.Fatalf("DEK mismatch: %v", err)
	}
	// Rotation: new KEK current, old KEK previous, still unwraps.
	newKEK, _ := vault.NewKey()
	rotated, err := New(s, vault.StaticKeyProvider(newKEK, kek)).DEK(ctx, org, 1)
	if err != nil || !bytes.Equal(rotated, dek) {
		t.Fatalf("unwrap during rotation: %v", err)
	}
	// Wrong KEK fails.
	if _, err := New(s, vault.StaticKeyProvider(newKEK, nil)).DEK(ctx, org, 1); err == nil {
		t.Fatal("wrong KEK unwrapped the DEK")
	}
}

func TestCacheExpires(t *testing.T) {
	ctx := context.Background()
	kek, _ := vault.NewKey()
	s, org := setup(t)
	kr := New(s, vault.StaticKeyProvider(kek, nil))
	_ = kr.EnsureOrgKey(ctx, org)
	now := time.Now()
	kr.now = func() time.Time { return now }
	if _, err := kr.DEK(ctx, org, 1); err != nil {
		t.Fatal(err)
	}
	if kr.cached(org, 1) == nil {
		t.Fatal("DEK not cached")
	}
	now = now.Add(DefaultCacheTTL + time.Second)
	if kr.cached(org, 1) != nil {
		t.Fatal("DEK cached past its lifetime")
	}
}
