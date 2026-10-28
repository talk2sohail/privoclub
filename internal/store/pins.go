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

// AcceptPins accepts exactly the listed pending definitions of a server, so a
// definition that arrives after the admin reviewed the list is never accepted
// by accident. It returns the keys that were accepted.
func (s *Store) AcceptPins(ctx context.Context, org OrgID, serverID string, keys []PinKey) ([]PinKey, error) {
	var accepted []PinKey
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := ts(time.Now())
		for _, k := range keys {
			res, err := tx.ExecContext(ctx, `
				UPDATE pins SET status = 'accepted', updated_at = ?
				WHERE org_id = ? AND server_id = ? AND kind = ? AND name = ? AND hash = ? AND status = 'pending'`,
				now, org, serverID, k.Kind, k.Name, k.Hash)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				accepted = append(accepted, k)
			}
		}
		return nil
	})
	return accepted, err
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
