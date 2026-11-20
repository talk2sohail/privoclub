package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BootstrapSetting is the per-org setting that records the id of the bootstrap
// token once one has been installed. It is written in the same transaction as
// the token, and never cleared, so a revoked or expired bootstrap token cannot
// come back at the next restart.
const BootstrapSetting = "bootstrap.consumed"

// maxBootstrapTTL caps a bootstrap token's lifetime whatever the caller asks for.
const maxBootstrapTTL = 7 * 24 * time.Hour

// BootstrapResult says what InstallBootstrapToken did.
type BootstrapResult int

// Results of InstallBootstrapToken.
const (
	// BootstrapInstalled: the token and its audit record were stored.
	BootstrapInstalled BootstrapResult = iota
	// BootstrapConsumed: a bootstrap token was installed here before; nothing was written.
	BootstrapConsumed
	// BootstrapNotNeeded: the org already has (or had) a control token; nothing was written.
	BootstrapNotNeeded
)

func (r BootstrapResult) String() string {
	switch r {
	case BootstrapInstalled:
		return "installed"
	case BootstrapConsumed:
		return "consumed"
	case BootstrapNotNeeded:
		return "not-needed"
	}
	return "unknown"
}

// InstallBootstrapToken stores t as an org's first control token, at most once
// per org. The token, the consumed marker and the audit record ev (written with
// phase admin) commit together, or not at all. It installs only owner control
// tokens with a short, bounded lifetime, and only when the org has never had a
// control token or a member: an org that has been administered, by token or
// through the browser, does not need a bootstrap (F24 second review R-01).
func (s *Store) InstallBootstrapToken(ctx context.Context, t APIToken, ev AuditEvent) (BootstrapResult, error) {
	if t.Role != RoleOwner || t.Kind != KindControl {
		return 0, errors.New("store: a bootstrap token must be an owner control token")
	}
	if !t.ExpiresAt.After(t.CreatedAt) || t.ExpiresAt.After(t.CreatedAt.Add(maxBootstrapTTL)) {
		return 0, errors.New("store: a bootstrap token must expire within 7 days of creation")
	}
	ev.Phase = "admin"
	result := BootstrapInstalled
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var marker string
		switch err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE org_id = ? AND key = ?`, t.OrgID, BootstrapSetting).Scan(&marker); {
		case err == nil:
			result = BootstrapConsumed
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		administered, err := administered(ctx, tx, t.OrgID)
		if err != nil {
			return err
		}
		if administered {
			result = BootstrapNotNeeded
			return nil
		}
		// A live token with the same name would make the unique name index fail
		// the install and stop the process (R-11); the bootstrap token takes a
		// name of its own instead and the other token is left alone.
		var sameName int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE org_id = ? AND name = ? AND revoked_at IS NULL`, t.OrgID, t.Name).Scan(&sameName); err != nil {
			return err
		}
		if sameName > 0 {
			t.Name += "-" + t.ID[:min(len(t.ID), 8)]
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_tokens (org_id, id, name, role, kind, person, secret_hash, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.OrgID, t.ID, t.Name, t.Role, t.Kind, t.Person, t.SecretHash, ts(t.CreatedAt), ts(t.ExpiresAt)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (org_id, key, value) VALUES (?, ?, ?)`, t.OrgID, BootstrapSetting, t.ID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, t.OrgID, []AuditEvent{ev})
	})
	if err != nil {
		return 0, mapErr(err)
	}
	return result, nil
}

// BootstrapNeeded reports whether an org could still take a bootstrap token: no
// bootstrap token was installed before and it has never had a control token or
// a member. It
// only reads, so that startup can tell a bootstrap file that matters from a
// leftover; InstallBootstrapToken still decides, in its own transaction.
func (s *Store) BootstrapNeeded(ctx context.Context, org OrgID) (bool, error) {
	if marker, err := s.Setting(ctx, org, BootstrapSetting, ""); err != nil || marker != "" {
		return false, err
	}
	done, err := administered(ctx, s.r, org)
	if err != nil {
		return false, mapErr(err)
	}
	return !done, nil
}

// administered reports whether an org has ever had a control token (revoked
// ones count) or a member (removed ones count).
func administered(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, org OrgID) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM api_tokens WHERE org_id = ? AND kind = ?) + (SELECT COUNT(*) FROM users WHERE org_id = ?)`,
		org, KindControl, org).Scan(&n)
	return n > 0, err
}
