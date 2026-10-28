// Package keyring manages per-organisation data keys (ADR 0003, 0013).
//
// Each org key version records the provider and key reference that wrap it,
// so self-hosted master keys, a cloud KMS and customer-managed keys can sit
// side by side. Unwrapped DEKs are cached briefly in memory only.
package keyring

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// LocalProvider is the provider name for the self-hosted master key.
const LocalProvider = "local"

// DefaultCacheTTL bounds how long an unwrapped DEK stays in memory.
const DefaultCacheTTL = 5 * time.Minute

// Store is the subset of the store the keyring needs.
type Store interface {
	CurrentOrgKey(ctx context.Context, org store.OrgID) (store.OrgKey, error)
	OrgKeyVersion(ctx context.Context, org store.OrgID, version uint32) (store.OrgKey, error)
	InsertOrgKey(ctx context.Context, org store.OrgID, k store.OrgKey) error
}

// Keyring resolves and caches org data keys.
type Keyring struct {
	store     Store
	providers map[string]vault.KeyProvider
	ttl       time.Duration
	now       func() time.Time

	mu    sync.Mutex
	cache map[cacheKey]cached
}

type cacheKey struct {
	org     store.OrgID
	version uint32
}

type cached struct {
	dek     []byte
	expires time.Time
}

// New returns a keyring with the local provider registered.
func New(s Store, local vault.KeyProvider) *Keyring {
	return &Keyring{
		store:     s,
		providers: map[string]vault.KeyProvider{LocalProvider: local},
		ttl:       DefaultCacheTTL,
		now:       time.Now,
		cache:     map[cacheKey]cached{},
	}
}

// EnsureOrgKey creates the first DEK for an org if it has none.
func (k *Keyring) EnsureOrgKey(ctx context.Context, org store.OrgID) error {
	if _, err := k.store.CurrentOrgKey(ctx, org); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	dek, err := vault.NewKey()
	if err != nil {
		return err
	}
	wrapped, err := vault.WrapDEK(k.providers[LocalProvider].Current(), dek, string(org), 1)
	if err != nil {
		return err
	}
	err = k.store.InsertOrgKey(ctx, org, store.OrgKey{Version: 1, Provider: LocalProvider, KeyRef: "master", WrappedDEK: wrapped})
	if errors.Is(err, store.ErrConflict) {
		return nil // created concurrently
	}
	return err
}

// Current returns the newest DEK version for an org.
func (k *Keyring) Current(ctx context.Context, org store.OrgID) (uint32, []byte, error) {
	rec, err := k.store.CurrentOrgKey(ctx, org)
	if err != nil {
		return 0, nil, fmt.Errorf("keyring: %w", err)
	}
	dek, err := k.unwrap(org, rec)
	return rec.Version, dek, err
}

// DEK returns a specific DEK version for an org.
func (k *Keyring) DEK(ctx context.Context, org store.OrgID, version uint32) ([]byte, error) {
	if dek := k.cached(org, version); dek != nil {
		return dek, nil
	}
	rec, err := k.store.OrgKeyVersion(ctx, org, version)
	if err != nil {
		return nil, fmt.Errorf("keyring: %w", err)
	}
	return k.unwrap(org, rec)
}

func (k *Keyring) cached(org store.OrgID, version uint32) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	c, ok := k.cache[cacheKey{org, version}]
	if !ok || k.now().After(c.expires) {
		delete(k.cache, cacheKey{org, version})
		return nil
	}
	return c.dek
}

func (k *Keyring) unwrap(org store.OrgID, rec store.OrgKey) ([]byte, error) {
	if dek := k.cached(org, rec.Version); dek != nil {
		return dek, nil
	}
	p, ok := k.providers[rec.Provider]
	if !ok {
		return nil, fmt.Errorf("keyring: provider %q is not configured", rec.Provider)
	}
	dek, err := vault.UnwrapDEK(p.Current(), rec.WrappedDEK, string(org), rec.Version)
	if err != nil && p.Previous() != nil {
		dek, err = vault.UnwrapDEK(p.Previous(), rec.WrappedDEK, string(org), rec.Version)
	}
	if err != nil {
		return nil, fmt.Errorf("keyring: cannot unwrap key version %d: %w", rec.Version, err)
	}
	k.mu.Lock()
	k.cache[cacheKey{org, rec.Version}] = cached{dek: dek, expires: k.now().Add(k.ttl)}
	k.mu.Unlock()
	return dek, nil
}
