package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// A person's sign-in identities (ADR 0034): each is a provider and the
// provider's stable subject, and belongs to one member. A member links a
// second one only while signed in; nothing is ever linked by email.

// ErrLastIdentity refuses to unlink the only way a member signs in.
var ErrLastIdentity = errors.New("store: the member's only sign-in")

// Identity is one way a member signs in.
type Identity struct {
	Provider  string // ProviderGitHub or an OIDC issuer
	Subject   string
	Login     string // display snapshot
	CreatedAt time.Time
}

// Identities lists a member's identities, oldest first.
func (s *Store) Identities(ctx context.Context, org OrgID, userID string) ([]Identity, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT provider, subject, login, created_at FROM identities
		WHERE org_id = ? AND user_id = ? ORDER BY created_at, provider, subject`, org, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Identity
	for rows.Next() {
		var i Identity
		var created string
		if err := rows.Scan(&i.Provider, &i.Subject, &i.Login, &created); err != nil {
			return nil, err
		}
		i.CreatedAt = parseTS(created)
		out = append(out, i)
	}
	return out, rows.Err()
}

// LinkIdentity binds another identity to an active member and writes ev, in
// one transaction. ErrIdentityExists when the identity already belongs to
// anyone (this member included); ErrNotFound for no such active member.
func (s *Store) LinkIdentity(ctx context.Context, org OrgID, userID string, id Identity, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM identities WHERE org_id = ? AND provider = ? AND subject = ?`,
			org, id.Provider, id.Subject).Scan(&one); {
		case err == nil:
			return ErrIdentityExists
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO identities (org_id, provider, subject, user_id, login, created_at)
			SELECT ?, ?, ?, id, ?, ? FROM users WHERE org_id = ? AND id = ? AND disabled_at IS NULL`,
			org, id.Provider, id.Subject, id.Login, ts(id.CreatedAt), org, userID)
		if err != nil {
			return mapErr(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// UnlinkIdentity removes one of a member's identities and writes ev, in one
// transaction. It refuses with ErrLastIdentity when no other identity of the
// member is with one of usable, the providers set up now, so nobody unlinks
// themselves out (security review 2026-11-22, L-4); ErrNotFound when the
// member has no such identity. The member's sessions other than keepSession
// end with it: a session may have come in through the identity removed.
func (s *Store) UnlinkIdentity(ctx context.Context, org OrgID, userID, provider, subject string, usable []string, keepSession string, ev AuditEvent) error {
	ev.Phase = "admin"
	if usable == nil {
		usable = []string{}
	}
	list, err := json.Marshal(usable)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM identities WHERE org_id = ? AND user_id = ? AND provider = ? AND subject = ?`,
			org, userID, provider, subject)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k != 1 {
			return ErrNotFound
		}
		var left int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM identities WHERE org_id = ? AND user_id = ?
			AND provider IN (SELECT value FROM json_each(?))`, org, userID, string(list)).Scan(&left); err != nil {
			return err
		}
		if left == 0 {
			return ErrLastIdentity // rolls the delete back
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE org_id = ? AND user_id = ? AND id <> ?`, org, userID, keepSession); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// MembersWithoutProviders counts the active members none of whose identities
// is with one of providers: they cannot sign in while only those are
// configured (ADR 0034 point 9).
func (s *Store) MembersWithoutProviders(ctx context.Context, org OrgID, providers []string) (int, error) {
	if providers == nil {
		providers = []string{}
	}
	list, err := json.Marshal(providers)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.r.QueryRowContext(ctx, `
		SELECT count(*) FROM users u WHERE u.org_id = ? AND u.disabled_at IS NULL AND NOT EXISTS (
			SELECT 1 FROM identities i WHERE i.org_id = u.org_id AND i.user_id = u.id
			AND i.provider IN (SELECT value FROM json_each(?)))`, org, string(list)).Scan(&n)
	return n, err
}
