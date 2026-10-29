package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"privo.club/privo/internal/ids"
)

// Org is an organisation.
type Org struct {
	ID   OrgID
	Slug string
	Name string
}

// EnsureOrg returns the org with slug, creating it if needed.
func (s *Store) EnsureOrg(ctx context.Context, slug, name string) (Org, error) {
	var o Org
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT id, slug, name FROM orgs WHERE slug = ?`, slug)
		err := row.Scan(&o.ID, &o.Slug, &o.Name)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		o = Org{ID: OrgID(ids.New()), Slug: slug, Name: name}
		_, err = tx.ExecContext(ctx, `INSERT INTO orgs (id, slug, name, created_at) VALUES (?, ?, ?, ?)`,
			o.ID, o.Slug, o.Name, ts(time.Now()))
		return err
	})
	return o, err
}

// OrgBySlug returns the org with slug, or ErrNotFound. It never creates one.
func (s *Store) OrgBySlug(ctx context.Context, slug string) (Org, error) {
	var o Org
	err := s.r.QueryRowContext(ctx, `SELECT id, slug, name FROM orgs WHERE slug = ?`, slug).Scan(&o.ID, &o.Slug, &o.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Org{}, ErrNotFound
	}
	return o, err
}

// OrgKey is one version of an org's wrapped data key (ADR 0013).
type OrgKey struct {
	Version    uint32
	Provider   string
	KeyRef     string
	WrappedDEK []byte
}

// CurrentOrgKey returns the newest key version of an org.
func (s *Store) CurrentOrgKey(ctx context.Context, org OrgID) (OrgKey, error) {
	var k OrgKey
	err := s.r.QueryRowContext(ctx, `
		SELECT version, provider, key_ref, wrapped_dek FROM org_keys
		WHERE org_id = ? ORDER BY version DESC LIMIT 1`, org).
		Scan(&k.Version, &k.Provider, &k.KeyRef, &k.WrappedDEK)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// OrgKeyVersion returns a specific key version of an org.
func (s *Store) OrgKeyVersion(ctx context.Context, org OrgID, version uint32) (OrgKey, error) {
	k := OrgKey{Version: version}
	err := s.r.QueryRowContext(ctx, `
		SELECT provider, key_ref, wrapped_dek FROM org_keys WHERE org_id = ? AND version = ?`, org, version).
		Scan(&k.Provider, &k.KeyRef, &k.WrappedDEK)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// InsertOrgKey stores a new key version. It fails with ErrConflict if the version exists.
func (s *Store) InsertOrgKey(ctx context.Context, org OrgID, k OrgKey) error {
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO org_keys (org_id, version, provider, key_ref, wrapped_dek, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, org, k.Version, k.Provider, k.KeyRef, k.WrappedDEK, ts(time.Now()))
	return mapErr(err)
}
