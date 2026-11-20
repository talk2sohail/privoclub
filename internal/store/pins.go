package store

import (
	"context"
	"database/sql"
	"time"
)

// Pin statuses.
const (
	PinPending  = "pending"
	PinAccepted = "accepted"
	PinBlocked  = "blocked"
)

// Pin is the reviewed state of one tool, prompt or resource definition.
type Pin struct {
	ServerID   string
	Kind       string // tool | prompt | resource
	Name       string
	Hash       string
	Definition string
	Status     string
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
		SELECT kind, name, hash, definition, status FROM pins
		WHERE org_id = ? AND server_id = ? ORDER BY updated_at DESC, kind, name`, org, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Pin
	for rows.Next() {
		p := Pin{ServerID: serverID}
		if err := rows.Scan(&p.Kind, &p.Name, &p.Hash, &p.Definition, &p.Status); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
