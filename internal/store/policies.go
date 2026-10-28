package store

import (
	"context"
	"database/sql"
	"time"
)

// Policy actions.
const (
	ActionAllow = "allow"
	ActionAsk   = "ask"
	ActionDeny  = "deny"
)

// Policy is one access rule.
type Policy struct {
	ID        string
	Server    string
	Tool      string
	Subject   string
	Action    string
	Priority  int
	PinHash   string // preset rules: only match the reviewed definition they were built from
	Note      string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt *time.Time
}

// CreatePolicies inserts rules atomically.
func (s *Store) CreatePolicies(ctx context.Context, org OrgID, ps ...Policy) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, p := range ps {
			var exp any
			if p.ExpiresAt != nil {
				exp = ts(*p.ExpiresAt)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO policies (org_id, id, server, tool, subject, action, priority, pin_hash, note, created_by, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				org, p.ID, p.Server, p.Tool, p.Subject, p.Action, p.Priority, p.PinHash, p.Note, p.CreatedBy, ts(p.CreatedAt), exp); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

// ListPolicies returns an org's rules that have not expired.
func (s *Store) ListPolicies(ctx context.Context, org OrgID) ([]Policy, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, server, tool, subject, action, priority, pin_hash, note, created_by, created_at, expires_at
		FROM policies WHERE org_id = ? AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY priority DESC, created_at`, org, ts(time.Now()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Policy
	for rows.Next() {
		var p Policy
		var created string
		var exp sql.NullString
		if err := rows.Scan(&p.ID, &p.Server, &p.Tool, &p.Subject, &p.Action, &p.Priority, &p.PinHash, &p.Note, &p.CreatedBy, &created, &exp); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTS(created)
		if exp.Valid {
			t := parseTS(exp.String)
			p.ExpiresAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePolicy removes a rule.
func (s *Store) DeletePolicy(ctx context.Context, org OrgID, id string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM policies WHERE org_id = ? AND id = ?`, org, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
