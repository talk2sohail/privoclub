package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Pin statuses.
const (
	PinPending  = "pending"
	PinAccepted = "accepted"
	PinBlocked  = "blocked"
)

// AnnotationOverrides holds admin overrides for a tool pin's annotations.
// A nil field leaves the upstream's value unchanged.
type AnnotationOverrides struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

// IsEmpty reports whether no annotation hint is overridden.
func (o AnnotationOverrides) IsEmpty() bool {
	return o.ReadOnlyHint == nil && o.DestructiveHint == nil && o.OpenWorldHint == nil
}

// Pin is the reviewed state of one tool, prompt or resource definition.
type Pin struct {
	ServerID            string
	Kind                string // tool | prompt | resource
	Name                string
	Hash                string
	Definition          string
	Status              string
	AnnotationOverrides AnnotationOverrides
}

// RecordSeen stores a definition seen upstream. New (server, kind, name, hash)
// combinations start pending; known ones are left as they are.
func (s *Store) RecordSeen(ctx context.Context, org OrgID, pins []Pin) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		now := ts(time.Now())
		for _, p := range pins {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO pins (org_id, server_id, kind, name, hash, definition, status, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)
				ON CONFLICT (org_id, server_id, kind, name, hash) DO NOTHING`,
				org, p.ServerID, p.Kind, p.Name, p.Hash, p.Definition, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// AcceptPins accepts exactly the listed definitions of a server that are
// pending or blocked, so a definition that arrives after the admin reviewed
// the list is never accepted by accident, and a block can be undone. It
// returns the keys that were accepted.
func (s *Store) AcceptPins(ctx context.Context, org OrgID, serverID string, keys []PinKey) ([]PinKey, error) {
	return s.decidePins(ctx, org, serverID, keys, PinAccepted, PinPending, PinBlocked)
}

// BlockPins blocks exactly the listed pending definitions of a server: they
// stay hidden from AI clients and leave the review list, and are kept as a
// decision (PrunePending never deletes them). It returns the keys blocked.
func (s *Store) BlockPins(ctx context.Context, org OrgID, serverID string, keys []PinKey) ([]PinKey, error) {
	return s.decidePins(ctx, org, serverID, keys, PinBlocked, PinPending)
}

func (s *Store) decidePins(ctx context.Context, org OrgID, serverID string, keys []PinKey, to string, from ...string) ([]PinKey, error) {
	var done []PinKey
	err := s.tx(ctx, func(tx *sql.Tx) error {
		done = nil
		now := ts(time.Now())
		for _, k := range keys {
			for _, f := range from {
				res, err := tx.ExecContext(ctx, `
					UPDATE pins SET status = ?, updated_at = ?
					WHERE org_id = ? AND server_id = ? AND kind = ? AND name = ? AND hash = ? AND status = ?`, to, now, org, serverID, k.Kind, k.Name, k.Hash, f)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 1 {
					done = append(done, k)
					break
				}
			}
		}
		return nil
	})
	return done, err
}

// ListPins returns every recorded definition of a server, newest first.
func (s *Store) ListPins(ctx context.Context, org OrgID, serverID string) ([]Pin, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT kind, name, hash, definition, status, annotation_overrides FROM pins
		WHERE org_id = ? AND server_id = ? ORDER BY updated_at DESC, kind, name`, org, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Pin
	for rows.Next() {
		p := Pin{ServerID: serverID}
		var rawOv string
		if err := rows.Scan(&p.Kind, &p.Name, &p.Hash, &p.Definition, &p.Status, &rawOv); err != nil {
			return nil, err
		}
		if p.AnnotationOverrides, err = decodeAnnotationOverrides(rawOv); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PinDefinition returns one recorded definition and its status, or
// ErrNotFound.
func (s *Store) PinDefinition(ctx context.Context, org OrgID, serverID string, k PinKey) (Pin, error) {
	p := Pin{ServerID: serverID, Kind: k.Kind, Name: k.Name, Hash: k.Hash}
	var rawOv string
	err := s.r.QueryRowContext(ctx, `
		SELECT definition, status, annotation_overrides FROM pins
		WHERE org_id = ? AND server_id = ? AND kind = ? AND name = ? AND hash = ?`,
		org, serverID, k.Kind, k.Name, k.Hash).Scan(&p.Definition, &p.Status, &rawOv)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if p.AnnotationOverrides, err = decodeAnnotationOverrides(rawOv); err != nil {
		return p, err
	}
	return p, nil
}

// SetPinAnnotations saves annotation overrides for an exact tool pin
// (org_id, server_id, kind='tool', name, hash). Non-tool pins and unknown pins
// return ErrNotFound, and changed definitions with a new hash never inherit
// prior overrides.
func (s *Store) SetPinAnnotations(ctx context.Context, org OrgID, serverID string, key PinKey, ov AnnotationOverrides) error {
	if key.Kind != "" && key.Kind != "tool" {
		return ErrNotFound
	}
	raw := ""
	if !ov.IsEmpty() {
		b, err := json.Marshal(ov)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	res, err := s.w.ExecContext(ctx, `
		UPDATE pins SET annotation_overrides = ?
		WHERE org_id = ? AND server_id = ? AND kind = 'tool' AND name = ? AND hash = ?`,
		raw, org, serverID, key.Name, key.Hash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PinAnnotationOverrides returns active tool annotation overrides by
// (kind, name, hash) for a server in org.
func (s *Store) PinAnnotationOverrides(ctx context.Context, org OrgID, serverID string) (map[PinKey]AnnotationOverrides, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT kind, name, hash, annotation_overrides FROM pins
		WHERE org_id = ? AND server_id = ? AND kind = 'tool' AND annotation_overrides != ''`, org, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[PinKey]AnnotationOverrides{}
	for rows.Next() {
		var k PinKey
		var raw string
		if err := rows.Scan(&k.Kind, &k.Name, &k.Hash, &raw); err != nil {
			return nil, err
		}
		ov, err := decodeAnnotationOverrides(raw)
		if err != nil {
			return nil, err
		}
		if !ov.IsEmpty() {
			out[k] = ov
		}
	}
	return out, rows.Err()
}

func decodeAnnotationOverrides(raw string) (AnnotationOverrides, error) {
	if raw == "" {
		return AnnotationOverrides{}, nil
	}
	var ov AnnotationOverrides
	if err := json.Unmarshal([]byte(raw), &ov); err != nil {
		return AnnotationOverrides{}, err
	}
	return ov, nil
}

// PinStatuses returns status by (kind, name, hash) for a server.
func (s *Store) PinStatuses(ctx context.Context, org OrgID, serverID string) (map[PinKey]string, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT kind, name, hash, status FROM pins WHERE org_id = ? AND server_id = ?`, org, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[PinKey]string{}
	for rows.Next() {
		var k PinKey
		var status string
		if err := rows.Scan(&k.Kind, &k.Name, &k.Hash, &status); err != nil {
			return nil, err
		}
		out[k] = status
	}
	return out, rows.Err()
}

// PinKey identifies one pinned definition.
type PinKey struct {
	Kind, Name, Hash string
}

// PrunePending deletes the pending pins of a server that are not in keep, that
// is, definitions the server offered once and no longer offers. Accepted and
// blocked pins are never deleted: they are decisions. Without this a server
// that changes a definition on every refresh adds rows without end (R-04). It
// returns the number of rows deleted.
func (s *Store) PrunePending(ctx context.Context, org OrgID, serverID string, keep []PinKey) (int, error) {
	keepSet := make(map[PinKey]struct{}, len(keep))
	for _, k := range keep {
		keepSet[k] = struct{}{}
	}
	deleted := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		deleted = 0
		rows, err := tx.QueryContext(ctx, `
			SELECT kind, name, hash FROM pins
			WHERE org_id = ? AND server_id = ? AND status = 'pending'`, org, serverID)
		if err != nil {
			return err
		}
		var stale []PinKey
		for rows.Next() {
			var k PinKey
			if err := rows.Scan(&k.Kind, &k.Name, &k.Hash); err != nil {
				_ = rows.Close()
				return err
			}
			if _, ok := keepSet[k]; !ok {
				stale = append(stale, k)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, k := range stale {
			res, err := tx.ExecContext(ctx, `
				DELETE FROM pins
				WHERE org_id = ? AND server_id = ? AND kind = ? AND name = ? AND hash = ? AND status = 'pending'`,
				org, serverID, k.Kind, k.Name, k.Hash)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			deleted += int(n)
		}
		return nil
	})
	return deleted, err
}
