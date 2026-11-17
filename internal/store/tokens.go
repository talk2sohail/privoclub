package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Role names (ADR 0011). Roles combine in the full product; v1 tokens carry one.
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
	Person     string // the name it acts as; never used to find a member (F07 R-01)
	UserID     string // the member whose own token this is; "" for every other token
	SecretHash string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// CreateToken stores a token.
func (s *Store) CreateToken(ctx context.Context, t APIToken) error {
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO api_tokens (org_id, id, name, role, kind, person, user_id, secret_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.OrgID, t.ID, t.Name, t.Role, t.Kind, t.Person, t.UserID, t.SecretHash, ts(t.CreatedAt), ts(t.ExpiresAt))
	return mapErr(err)
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
