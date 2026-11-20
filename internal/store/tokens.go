package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

// Role names (ADR 0011). A person holds a set of them (ADR 0025); a token
// carries one.
const (
	RoleOwner    = "owner"
	RoleAdmin    = "admin"
	RoleAuditor  = "auditor"
	RoleApprover = "approver"
	RoleMember   = "member"
)

// Token kinds. Gateway tokens are for AI clients on /mcp; control tokens are
// for people on the admin API. Neither works on the other surface, so an agent
// holding a gateway token can never reach the admin API.
const (
	KindGateway = "gateway"
	KindControl = "control"
)

// ValidKind reports whether k is a token kind.
func ValidKind(k string) bool { return k == KindGateway || k == KindControl }

// ErrInvalidRoles is returned for a role set that is not {owner}, {admin} or
// a non-empty subset of {member, approver, auditor} (ADR 0025).
var ErrInvalidRoles = errors.New("store: roles must be owner alone, admin alone, or any of member, approver and auditor")

// NormalizeRoles checks a person's role set and returns it sorted, without
// repeats and surrounding spaces. Owner and admin already hold every
// permission, so they combine with nothing; the empty set is refused.
func NormalizeRoles(roles []string) ([]string, error) {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		r = strings.TrimSpace(r)
		if !ValidRole(r) {
			return nil, ErrInvalidRoles
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	if len(out) == 0 || (len(out) > 1 && (slices.Contains(out, RoleOwner) || slices.Contains(out, RoleAdmin))) {
		return nil, ErrInvalidRoles
	}
	return out, nil
}

// splitRoles reads the comma-joined roles of a query (group_concat), sorted.
func splitRoles(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, ",")
	slices.Sort(out)
	return out
}

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleAuditor, RoleApprover, RoleMember:
		return true
	}
	return false
}

// APIToken is a stored personal access token. The secret is never stored.
type APIToken struct {
	OrgID      OrgID
	ID         string
	Name       string
	Role       string
	Kind       string
	Person     string // a label: who the token is for. It gives no access; UserID does
	UserID     string // the member the token belongs to; "" is a token of nobody (F27)
	SecretHash string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// CreateToken stores a token. With t.UserID set the token belongs to that
// member, who must be an active member of the same org: the check and the
// insert are one statement, and ErrNotFound means nothing was stored.
func (s *Store) CreateToken(ctx context.Context, t APIToken) error {
	if t.UserID == "" {
		_, err := s.w.ExecContext(ctx, `
			INSERT INTO api_tokens (org_id, id, name, role, kind, person, user_id, secret_hash, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
			t.OrgID, t.ID, t.Name, t.Role, t.Kind, t.Person, t.SecretHash, ts(t.CreatedAt), ts(t.ExpiresAt))
		return mapErr(err)
	}
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO api_tokens (org_id, id, name, role, kind, person, user_id, secret_hash, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM users WHERE org_id = ? AND id = ? AND disabled_at IS NULL)`,
		t.OrgID, t.ID, t.Name, t.Role, t.Kind, t.Person, t.UserID, t.SecretHash, ts(t.CreatedAt), ts(t.ExpiresAt),
		t.OrgID, t.UserID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateTokenAudited stores a token and its audit record ev (phase admin) in
// one transaction: a token is never created without its trace (F24 second
// review R-02).
func (s *Store) CreateTokenAudited(ctx context.Context, t APIToken, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_tokens (org_id, id, name, role, kind, person, user_id, secret_hash, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.OrgID, t.ID, t.Name, t.Role, t.Kind, t.Person, t.UserID, t.SecretHash, ts(t.CreatedAt), ts(t.ExpiresAt)); err != nil {
			return mapErr(err)
		}
		return insertAudit(ctx, tx, t.OrgID, []AuditEvent{ev})
	})
}

// TokenByID looks a token up by its public id across orgs (it is globally unique).
func (s *Store) TokenByID(ctx context.Context, id string) (APIToken, error) {
	var t APIToken
	var created, expires string
	var revoked sql.NullString
	err := s.r.QueryRowContext(ctx, `
		SELECT org_id, id, name, role, kind, person, user_id, secret_hash, created_at, expires_at, revoked_at
		FROM api_tokens WHERE id = ?`, id).
		Scan(&t.OrgID, &t.ID, &t.Name, &t.Role, &t.Kind, &t.Person, &t.UserID, &t.SecretHash, &created, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.CreatedAt, t.ExpiresAt = parseTS(created), parseTS(expires)
	if revoked.Valid {
		r := parseTS(revoked.String)
		t.RevokedAt = &r
	}
	return t, nil
}

// ListTokens lists an org's active tokens, without secrets.
func (s *Store) ListTokens(ctx context.Context, org OrgID) ([]APIToken, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, name, role, kind, person, user_id, created_at, expires_at FROM api_tokens
		WHERE org_id = ? AND revoked_at IS NULL ORDER BY created_at`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []APIToken
	for rows.Next() {
		t := APIToken{OrgID: org}
		var created, expires string
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.Kind, &t.Person, &t.UserID, &created, &expires); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ExpiresAt = parseTS(created), parseTS(expires)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken marks a token revoked.
func (s *Store) RevokeToken(ctx context.Context, org OrgID, id string) error {
	res, err := s.w.ExecContext(ctx, `
		UPDATE api_tokens SET revoked_at = ? WHERE org_id = ? AND id = ? AND revoked_at IS NULL`,
		ts(time.Now()), org, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAllTokens lists an org's tokens, revoked ones included, without
// secrets: who a past call's token belonged to (F12 erasure).
func (s *Store) ListAllTokens(ctx context.Context, org OrgID) ([]APIToken, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, name, role, kind, person, user_id FROM api_tokens WHERE org_id = ?`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []APIToken
	for rows.Next() {
		t := APIToken{OrgID: org}
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.Kind, &t.Person, &t.UserID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
