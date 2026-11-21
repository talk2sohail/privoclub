package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Invites and per-person upstream grants (ADR 0017).

// maxInviteTTL caps an invite whatever the caller asks for.
const maxInviteTTL = 7 * 24 * time.Hour

// catalogRoles reports whether a user with roles may hold a server's catalog
// grant: the roles that manage servers (auth.PermManageServers).
func catalogRoles(roles []string) bool {
	return slices.Contains(roles, RoleOwner) || slices.Contains(roles, RoleAdmin)
}

// NewInvite describes an invite to store. SecretHash is the hex SHA-256 of the
// token that is shown once.
type NewInvite struct {
	ID         string
	SecretHash string
	Person     string
	Roles      []string
	CreatedBy  string
	Now        time.Time
	ExpiresAt  time.Time
}

// Invite is an invite that can still be claimed.
type Invite struct {
	ID        string
	Person    string
	Roles     []string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateInvite stores an invite and writes ev, retiring older unclaimed invites
// for the same person. Invites never make owners (only a setup link does), and
// a person who is already a member gets ErrConflict.
func (s *Store) CreateInvite(ctx context.Context, org OrgID, in NewInvite, ev AuditEvent) error {
	roles, err := NormalizeRoles(in.Roles)
	switch {
	case in.ID == "" || in.SecretHash == "" || in.Person == "":
		return errors.New("store: an invite needs an id, a secret hash and a person")
	case err != nil:
		return err
	case slices.Contains(roles, RoleOwner):
		return errors.New("store: an invite never makes an owner")
	case !in.ExpiresAt.After(in.Now) || in.ExpiresAt.After(in.Now.Add(maxInviteTTL)):
		return errors.New("store: an invite must expire within 7 days")
	}
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE org_id = ? AND person = ?`, org, in.Person).Scan(&one); {
		case err == nil:
			return ErrConflict
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE invites SET retired_at = ?
			WHERE org_id = ? AND person = ? AND claimed_at IS NULL AND retired_at IS NULL`, ts(in.Now), org, in.Person); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO invites (org_id, id, secret_hash, person, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			org, in.ID, in.SecretHash, in.Person, in.CreatedBy, ts(in.Now), ts(in.ExpiresAt)); err != nil {
			return mapErr(err)
		}
		for _, r := range roles {
			if _, err := tx.ExecContext(ctx, `INSERT INTO invite_roles (org_id, invite_id, role) VALUES (?, ?, ?)`, org, in.ID, r); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// Invite finds the claimable invite with this secret hash, or ErrNotFound.
func (s *Store) Invite(ctx context.Context, org OrgID, secretHash string, now time.Time) (Invite, error) {
	var in Invite
	var created, expires, roles string
	err := s.r.QueryRowContext(ctx, `
		SELECT i.id, i.person, `+inviteRoles+`, i.created_by, i.created_at, i.expires_at FROM invites i
		WHERE i.org_id = ? AND i.secret_hash = ? AND i.claimed_at IS NULL AND i.retired_at IS NULL AND i.expires_at > ?`,
		org, secretHash, ts(now)).Scan(&in.ID, &in.Person, &roles, &in.CreatedBy, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	if err != nil {
		return in, err
	}
	in.Roles = splitRoles(roles)
	in.CreatedAt, in.ExpiresAt = parseTS(created), parseTS(expires)
	return in, nil
}

// inviteRoles selects an invite's roles, comma-joined, for the invite i.
const inviteRoles = `COALESCE((SELECT group_concat(r.role, ',') FROM invite_roles r WHERE r.org_id = i.org_id AND r.invite_id = i.id), '')`

// ClaimInvite turns an invite into a member, with the invite's person and
// roles (c.Person is ignored). Like ClaimSetup, a refused claim keeps the
// invite and of concurrent claims exactly one wins.
func (s *Store) ClaimInvite(ctx context.Context, org OrgID, c Claim, ev AuditEvent) (User, error) {
	return s.claim(ctx, org, c, ev, func(tx *sql.Tx, u *User) error {
		err := tx.QueryRowContext(ctx, `
			UPDATE invites SET claimed_at = ?, claimed_by = ?
			WHERE org_id = ? AND id = ? AND claimed_at IS NULL AND retired_at IS NULL AND expires_at > ?
			RETURNING person`,
			ts(c.Now), c.UserID, org, c.LinkID, ts(c.Now)).Scan(&u.Person)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupUnavailable
		}
		if err != nil {
			return err
		}
		var roles string
		if err := tx.QueryRowContext(ctx, `SELECT `+inviteRoles+` FROM invites i WHERE i.org_id = ? AND i.id = ?`, org, c.LinkID).Scan(&roles); err != nil {
			return err
		}
		u.Roles = splitRoles(roles)
		return nil
	})
}

// ListUsers lists the org's members, removed ones included, by person, with
// their GitHub login.
func (s *Store) ListUsers(ctx context.Context, org OrgID) ([]User, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT `+userCols+`, COALESCE((SELECT i.login FROM identities i WHERE i.org_id = u.org_id AND i.user_id = u.id ORDER BY i.created_at LIMIT 1), '')
		FROM users u WHERE u.org_id = ? ORDER BY u.person`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		var login string
		u, err := scanUser(org, rows, &login)
		if err != nil {
			return nil, err
		}
		u.Login = login
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserByID returns the member with this id, removed ones included: how a
// token finds the member it belongs to (F27).
func (s *Store) UserByID(ctx context.Context, org OrgID, id string) (User, error) {
	u, err := scanUser(org, s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.org_id = ? AND u.id = ?`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// UserByPerson returns the user with this person name: how the admin API finds
// the member an administrator names. It never decides whose account or teams a
// caller gets; that is the caller's user id (F27).
func (s *Store) UserByPerson(ctx context.Context, org OrgID, person string) (User, error) {
	u, err := scanUser(org, s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.org_id = ? AND u.person = ?`, org, person))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// Grant is a person's upstream OAuth grant for one server. Credential holds the
// encrypted tokens; it is empty in lists.
type Grant struct {
	ServerID    string
	Alias       string // filled in lists
	UserID      string
	Person      string // filled in lists
	Scopes      string
	Catalog     bool
	ConnectedAt time.Time
	LastUsedAt  time.Time
	RefreshedAt time.Time
	Credential  Credential
	Kind        string // in ListAllGrants: PurposeOAuthGrant or PurposeKeyGrant
}

// SaveGrant stores a person's grant for a server, replacing an earlier one and
// its tokens, and writes ev. The grant becomes the server's catalog grant when
// it already was, or when catalogOK and the server has none; the caller sets
// catalogOK for users who manage servers. The credential must belong to this
// server and user, and be an OAuth grant or a person's own key (ADR 0023).
// Unknown or disabled users and unknown servers are
// ErrNotFound.
func (s *Store) SaveGrant(ctx context.Context, org OrgID, g Grant, catalogOK bool, ev AuditEvent) (Grant, error) {
	c := g.Credential
	if c.ServerID != g.ServerID || c.OwnerID != g.UserID || (c.Purpose != PurposeOAuthGrant && c.Purpose != PurposeKeyGrant) || c.ID == "" {
		return Grant{}, errors.New("store: the grant's credential does not belong to this server and user")
	}
	ev.Phase = "admin"
	g.LastUsedAt, g.RefreshedAt = g.ConnectedAt, g.ConnectedAt
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE org_id = ? AND id = ?`, org, g.ServerID).Scan(&one); err != nil {
			return notFound(err)
		}
		u, err := scanUser(org, tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.org_id = ? AND u.id = ? AND u.disabled_at IS NULL`, org, g.UserID))
		if err != nil {
			return notFound(err)
		}
		var wasCatalog bool
		var oldCred string
		switch err := tx.QueryRowContext(ctx, `SELECT catalog, credential_id FROM upstream_grants WHERE org_id = ? AND server_id = ? AND user_id = ?`,
			org, g.ServerID, g.UserID).Scan(&wasCatalog, &oldCred); {
		case err == nil:
			if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE org_id = ? AND id = ?`, org, oldCred); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM upstream_grants WHERE org_id = ? AND server_id = ? AND user_id = ?`, org, g.ServerID, g.UserID); err != nil {
				return err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		g.Catalog = wasCatalog
		if !g.Catalog && catalogOK && catalogRoles(u.Roles) {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM upstream_grants WHERE org_id = ? AND server_id = ? AND catalog = 1`, org, g.ServerID).Scan(&n); err != nil {
				return err
			}
			g.Catalog = n == 0
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO credentials (org_id, id, server_id, owner_id, purpose, key_version, row_version, ciphertext)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			org, c.ID, c.ServerID, c.OwnerID, c.Purpose, c.KeyVersion, c.RowVersion, c.Ciphertext); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO upstream_grants (org_id, server_id, user_id, credential_id, scopes, catalog, connected_at, last_used_at, refreshed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			org, g.ServerID, g.UserID, c.ID, g.Scopes, g.Catalog, ts(g.ConnectedAt), ts(g.ConnectedAt), ts(g.ConnectedAt)); err != nil {
			return mapErr(err)
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
	if err != nil {
		return Grant{}, err
	}
	return g, nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

const grantCols = `g.server_id, g.user_id, g.scopes, g.catalog, g.connected_at, g.last_used_at, g.refreshed_at,
	c.id, c.purpose, c.key_version, c.row_version, c.ciphertext`

func scanGrant(row interface{ Scan(...any) error }) (Grant, error) {
	var g Grant
	var connected, used, refreshed string
	c := &g.Credential
	if err := row.Scan(&g.ServerID, &g.UserID, &g.Scopes, &g.Catalog, &connected, &used, &refreshed,
		&c.ID, &c.Purpose, &c.KeyVersion, &c.RowVersion, &c.Ciphertext); err != nil {
		return Grant{}, notFound(err)
	}
	g.ConnectedAt, g.LastUsedAt, g.RefreshedAt = parseTS(connected), parseTS(used), parseTS(refreshed)
	// The purpose is part of what the credential is sealed under: an OAuth
	// grant, or a person's own key (ADR 0023). Anything else is not a grant.
	if c.Purpose != PurposeOAuthGrant && c.Purpose != PurposeKeyGrant {
		return Grant{}, fmt.Errorf("store: a grant's credential has the purpose %q", c.Purpose)
	}
	c.ServerID, c.OwnerID = g.ServerID, g.UserID
	return g, nil
}

// Grant returns a person's grant for a server with its encrypted tokens.
func (s *Store) Grant(ctx context.Context, org OrgID, serverID, userID string) (Grant, error) {
	return scanGrant(s.r.QueryRowContext(ctx, `
		SELECT `+grantCols+` FROM upstream_grants g
		JOIN credentials c ON c.org_id = g.org_id AND c.id = g.credential_id
		WHERE g.org_id = ? AND g.server_id = ? AND g.user_id = ?`, org, serverID, userID))
}

// CatalogGrant returns the grant a server's catalog is read with.
func (s *Store) CatalogGrant(ctx context.Context, org OrgID, serverID string) (Grant, error) {
	return scanGrant(s.r.QueryRowContext(ctx, `
		SELECT `+grantCols+` FROM upstream_grants g
		JOIN credentials c ON c.org_id = g.org_id AND c.id = g.credential_id
		WHERE g.org_id = ? AND g.server_id = ? AND g.catalog = 1`, org, serverID))
}

// ListGrants lists a person's grants without their tokens, by server alias.
func (s *Store) ListGrants(ctx context.Context, org OrgID, userID string) ([]Grant, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT g.server_id, srv.alias, g.user_id, u.person, g.scopes, g.catalog, g.connected_at, g.last_used_at, g.refreshed_at
		FROM upstream_grants g
		JOIN servers srv ON srv.org_id = g.org_id AND srv.id = g.server_id
		JOIN users u ON u.org_id = g.org_id AND u.id = g.user_id
		WHERE g.org_id = ? AND g.user_id = ? ORDER BY srv.alias, u.person`, org, userID)
	if err != nil {
		return nil, err
	}
	return scanGrantList(rows)
}

// ListServerGrants lists everyone's grants for a server without their tokens.
func (s *Store) ListServerGrants(ctx context.Context, org OrgID, serverID string) ([]Grant, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT g.server_id, srv.alias, g.user_id, u.person, g.scopes, g.catalog, g.connected_at, g.last_used_at, g.refreshed_at
		FROM upstream_grants g
		JOIN servers srv ON srv.org_id = g.org_id AND srv.id = g.server_id
		JOIN users u ON u.org_id = g.org_id AND u.id = g.user_id
		WHERE g.org_id = ? AND g.server_id = ? ORDER BY srv.alias, u.person`, org, serverID)
	if err != nil {
		return nil, err
	}
	return scanGrantList(rows)
}

func scanGrantList(rows *sql.Rows) ([]Grant, error) {
	defer func() { _ = rows.Close() }()
	var out []Grant
	for rows.Next() {
		var g Grant
		var connected, used, refreshed string
		if err := rows.Scan(&g.ServerID, &g.Alias, &g.UserID, &g.Person, &g.Scopes, &g.Catalog, &connected, &used, &refreshed); err != nil {
			return nil, err
		}
		g.ConnectedAt, g.LastUsedAt, g.RefreshedAt = parseTS(connected), parseTS(used), parseTS(refreshed)
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateGrantToken stores refreshed tokens if the grant's credential is still
// at row version from: c.RowVersion must be from+1. Of two refreshes that
// raced, the loser gets ErrConflict and must re-read; a grant deleted
// meanwhile is ErrNotFound.
func (s *Store) UpdateGrantToken(ctx context.Context, org OrgID, serverID, userID string, from uint64, c Credential, now time.Time) error {
	if c.RowVersion != from+1 || c.ServerID != serverID || c.OwnerID != userID || c.Purpose != PurposeOAuthGrant {
		return errors.New("store: a refreshed credential must be the next row version of this grant")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var credID string
		if err := tx.QueryRowContext(ctx, `SELECT credential_id FROM upstream_grants WHERE org_id = ? AND server_id = ? AND user_id = ?`,
			org, serverID, userID).Scan(&credID); err != nil {
			return notFound(err)
		}
		if credID != c.ID {
			return ErrConflict // reconnected meanwhile
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE credentials SET ciphertext = ?, key_version = ?, row_version = ?
			WHERE org_id = ? AND id = ? AND row_version = ?`,
			c.Ciphertext, c.KeyVersion, c.RowVersion, org, c.ID, from)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE upstream_grants SET refreshed_at = ? WHERE org_id = ? AND server_id = ? AND user_id = ?`,
			ts(now), org, serverID, userID)
		return err
	})
}

// TouchGrant records a use, at most once a minute.
func (s *Store) TouchGrant(ctx context.Context, org OrgID, serverID, userID string, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `
		UPDATE upstream_grants SET last_used_at = ?
		WHERE org_id = ? AND server_id = ? AND user_id = ? AND last_used_at <= ?`,
		ts(now), org, serverID, userID, ts(now.Add(-touchEvery)))
	return err
}

// DeleteGrant removes a person's grant and its tokens and writes ev. When it was
// the catalog grant, another server manager's grant takes over.
func (s *Store) DeleteGrant(ctx context.Context, org OrgID, serverID, userID string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := deleteGrant(ctx, tx, org, serverID, userID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

func deleteGrant(ctx context.Context, tx *sql.Tx, org OrgID, serverID, userID string) error {
	var credID string
	var catalog bool
	err := tx.QueryRowContext(ctx, `
		DELETE FROM upstream_grants WHERE org_id = ? AND server_id = ? AND user_id = ?
		RETURNING credential_id, catalog`, org, serverID, userID).Scan(&credID, &catalog)
	if err != nil {
		return notFound(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE org_id = ? AND id = ?`, org, credID); err != nil {
		return err
	}
	if catalog {
		return promoteCatalog(ctx, tx, org, serverID)
	}
	return nil
}

// promoteCatalog makes the oldest grant of an active server manager the
// server's catalog grant, if there is one.
func promoteCatalog(ctx context.Context, tx *sql.Tx, org OrgID, serverID string) error {
	_, err := promoteCatalogExcept(ctx, tx, org, serverID, "")
	return err
}

// promoteCatalogExcept is promoteCatalog leaving out one user's grant. It
// reports whether a grant was promoted.
func promoteCatalogExcept(ctx context.Context, tx *sql.Tx, org OrgID, serverID, except string) (bool, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE upstream_grants SET catalog = 1
		WHERE org_id = ? AND server_id = ? AND user_id = (
			SELECT g.user_id FROM upstream_grants g
			JOIN users u ON u.org_id = g.org_id AND u.id = g.user_id
			WHERE g.org_id = ? AND g.server_id = ? AND u.disabled_at IS NULL
			  AND EXISTS (SELECT 1 FROM user_roles r WHERE r.org_id = u.org_id AND r.user_id = u.id AND r.role IN (?, ?)) AND g.user_id != ?
			  AND NOT EXISTS (SELECT 1 FROM upstream_grants x WHERE x.org_id = g.org_id AND x.server_id = g.server_id AND x.catalog = 1)
			ORDER BY g.connected_at, g.user_id LIMIT 1)`,
		org, serverID, org, serverID, RoleOwner, RoleAdmin, except)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PassCatalog hands the catalog mark of a server from userID's grant to the
// oldest grant of another active server manager, when there is one: the
// upstream refused userID's own key, which stays theirs (ADR 0023 point 5),
// and the tool list should not stop for everyone because of it (review of
// 2026-11-15, U-03). It reports whether the mark moved; without another
// grant it stays where it was.
func (s *Store) PassCatalog(ctx context.Context, org OrgID, serverID, userID string) (bool, error) {
	var moved bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE upstream_grants SET catalog = 0 WHERE org_id = ? AND server_id = ? AND user_id = ? AND catalog = 1`, org, serverID, userID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err // not the catalog grant (any more): nothing to pass
		}
		if moved, err = promoteCatalogExcept(ctx, tx, org, serverID, userID); err != nil || moved {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE upstream_grants SET catalog = 1 WHERE org_id = ? AND server_id = ? AND user_id = ?`, org, serverID, userID)
		return err
	})
	return moved, err
}

// deleteUserGrants removes all of a user's grants (a disabled user).
func deleteUserGrants(ctx context.Context, tx *sql.Tx, org OrgID, userID string) error {
	servers, err := grantServers(ctx, tx, org, userID, false)
	if err != nil {
		return err
	}
	for _, srv := range servers {
		if err := deleteGrant(ctx, tx, org, srv, userID); err != nil {
			return err
		}
	}
	return nil
}

// dropCatalogGrants takes the catalog mark from a user who no longer manages
// servers and hands it to another manager.
func dropCatalogGrants(ctx context.Context, tx *sql.Tx, org OrgID, userID string) error {
	servers, err := grantServers(ctx, tx, org, userID, true)
	if err != nil {
		return err
	}
	for _, srv := range servers {
		if _, err := tx.ExecContext(ctx, `UPDATE upstream_grants SET catalog = 0 WHERE org_id = ? AND server_id = ? AND user_id = ?`, org, srv, userID); err != nil {
			return err
		}
		if err := promoteCatalog(ctx, tx, org, srv); err != nil {
			return err
		}
	}
	return nil
}

func grantServers(ctx context.Context, tx *sql.Tx, org OrgID, userID string, catalogOnly bool) ([]string, error) {
	q := `SELECT server_id FROM upstream_grants WHERE org_id = ? AND user_id = ?`
	if catalogOnly {
		q += ` AND catalog = 1`
	}
	rows, err := tx.QueryContext(ctx, q, org, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GrantQuery filters ListAllGrants. Empty fields do not filter.
type GrantQuery struct {
	Person string // exactly
	Server string // the alias, exactly
	After  string // cursor: the next of the previous page
	Limit  int    // 1 to 500, default 100
}

// ErrBadGrantQuery is returned for a cursor this store did not make.
var ErrBadGrantQuery = errors.New("store: bad grant query")

// ListAllGrants lists the org's grants across servers (F07 A), by server
// alias then person, with each one's kind and never its credential, and the
// cursor of the next page ("" on the last).
func (s *Store) ListAllGrants(ctx context.Context, org OrgID, q GrantQuery) ([]Grant, string, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	where := `g.org_id = ?`
	args := []any{org}
	if q.Person != "" {
		where += ` AND u.person = ?`
		args = append(args, q.Person)
	}
	if q.Server != "" {
		where += ` AND srv.alias = ?`
		args = append(args, q.Server)
	}
	if q.After != "" {
		alias, person, ok := strings.Cut(q.After, "\x00")
		if !ok || alias == "" || person == "" {
			return nil, "", ErrBadGrantQuery
		}
		where += ` AND (srv.alias > ? OR (srv.alias = ? AND u.person > ?))`
		args = append(args, alias, alias, person)
	}
	args = append(args, q.Limit+1)
	rows, err := s.r.QueryContext(ctx, `
		SELECT g.server_id, srv.alias, g.user_id, u.person, g.scopes, g.catalog, g.connected_at, g.last_used_at, g.refreshed_at, c.purpose
		FROM upstream_grants g
		JOIN servers srv ON srv.org_id = g.org_id AND srv.id = g.server_id
		JOIN users u ON u.org_id = g.org_id AND u.id = g.user_id
		JOIN credentials c ON c.org_id = g.org_id AND c.id = g.credential_id
		WHERE `+where+`
		ORDER BY srv.alias, u.person LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	var out []Grant
	for rows.Next() {
		var g Grant
		var connected, used, refreshed string
		if err := rows.Scan(&g.ServerID, &g.Alias, &g.UserID, &g.Person, &g.Scopes, &g.Catalog, &connected, &used, &refreshed, &g.Kind); err != nil {
			return nil, "", err
		}
		g.ConnectedAt, g.LastUsedAt, g.RefreshedAt = parseTS(connected), parseTS(used), parseTS(refreshed)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > q.Limit {
		out = out[:q.Limit]
		last := out[len(out)-1]
		next = last.Alias + "\x00" + last.Person
	}
	return out, next, nil
}
