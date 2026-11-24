package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	ID       string
	Server   string
	Tool     string
	Subject  string
	Action   string
	Priority int
	PinHash  string // preset rules: only match the reviewed definition they were built from
	// Conditions is the JSON list of conditions on the call's arguments, ""
	// for none (ADR 0027); the policy package reads and checks it.
	Conditions string
	// GrantOf names the approval a grant was made from (F10); a grant only
	// turns an ask into allow (ADR 0027).
	GrantOf   string
	Note      string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt *time.Time
}

// ErrUnknownUser is returned for a rule naming a person who is not an active
// member of the org.
var ErrUnknownUser = errors.New("store: no such member")

// CreatePolicies inserts rules atomically. A rule for "team:<slug>" needs the
// team to exist (ErrUnknownTeam), checked in the same transaction, so a team
// created later never inherits a rule written for a name (F26 review R-02).
func (s *Store) CreatePolicies(ctx context.Context, org OrgID, ps ...Policy) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := insertPolicies(ctx, tx, org, ps); err != nil {
			return err
		}
		return bumpPolicyVersion(ctx, tx, org)
	})
}

// ImportPolicies applies an import of rules in one transaction (ADR 0027):
// with replace, every rule that is not a grant is deleted first; the rules
// are then added as CreatePolicies adds them; a non-empty defaultAction
// becomes the org default; ev records it. Any refusal changes nothing.
func (s *Store) ImportPolicies(ctx context.Context, org OrgID, replace bool, ps []Policy, defaultKey, defaultAction string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		if replace {
			if _, err := tx.ExecContext(ctx, `DELETE FROM policies WHERE org_id = ? AND grant_of = ''`, org); err != nil {
				return err
			}
		}
		if err := insertPolicies(ctx, tx, org, ps); err != nil {
			return err
		}
		if defaultAction != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (org_id, key, value) VALUES (?, ?, ?)
				ON CONFLICT (org_id, key) DO UPDATE SET value = excluded.value`, org, defaultKey, defaultAction); err != nil {
				return err
			}
		}
		if err := bumpPolicyVersion(ctx, tx, org); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

func insertPolicies(ctx context.Context, tx *sql.Tx, org OrgID, ps []Policy) error {
	for _, p := range ps {
		if team, ok := strings.CutPrefix(p.Subject, "team:"); ok {
			if err := checkTeams(ctx, tx, org, []string{team}); err != nil {
				return err
			}
		}
		// A rule for one person needs an active member of this org (F10).
		if user, ok := strings.CutPrefix(p.Subject, "user:"); ok {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE org_id = ? AND id = ? AND disabled_at IS NULL`, org, user).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrUnknownUser
			}
			if err != nil {
				return err
			}
		}
		var exp any
		if p.ExpiresAt != nil {
			exp = ts(*p.ExpiresAt)
		}
		if _, err := tx.ExecContext(ctx, `
				INSERT INTO policies (org_id, id, server, tool, subject, action, priority, pin_hash, conditions, grant_of, note, created_by, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			org, p.ID, p.Server, p.Tool, p.Subject, p.Action, p.Priority, p.PinHash, p.Conditions, p.GrantOf, p.Note, p.CreatedBy, ts(p.CreatedAt), exp); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// ListPolicies returns an org's rules that have not expired.
func (s *Store) ListPolicies(ctx context.Context, org OrgID) ([]Policy, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, server, tool, subject, action, priority, pin_hash, conditions, grant_of, note, created_by, created_at, expires_at
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
		if err := rows.Scan(&p.ID, &p.Server, &p.Tool, &p.Subject, &p.Action, &p.Priority, &p.PinHash, &p.Conditions, &p.GrantOf, &p.Note, &p.CreatedBy, &created, &exp); err != nil {
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
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM policies WHERE org_id = ? AND id = ?`, org, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return bumpPolicyVersion(ctx, tx, org)
	})
}

// PolicyVersion returns the org's monotonic policy version, bumped inside
// every transaction that alters policies or policy defaults (F09 A).
func (s *Store) PolicyVersion(ctx context.Context, org OrgID) (int64, error) {
	var v int64
	err := s.r.QueryRowContext(ctx, `SELECT policy_version FROM orgs WHERE id = ?`, org).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return v, err
}

func bumpPolicyVersion(ctx context.Context, tx *sql.Tx, org OrgID) error {
	res, err := tx.ExecContext(ctx, `UPDATE orgs SET policy_version = policy_version + 1 WHERE id = ?`, org)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}
