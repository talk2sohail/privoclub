package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Storage for Privo's own authorization server (F06, ADR 0022): the grants
// members give to AI clients, the codes and tokens of those grants, the
// clients accepted by a row, and the key the tokens are signed with.
//
// The OAuth library never touches the database. It talks to an adapter in
// internal/oauthserver, and the adapter talks to these methods. A code
// exchange and a refresh are several statements that must commit together;
// the adapter opens a transaction with BeginOAuthTx and passes its context
// to every call, and each method here uses that transaction when the context
// carries one.

// Kinds of row in oauth_tokens.
const (
	OAuthCode    = "code"
	OAuthPKCE    = "pkce"
	OAuthAccess  = "access"
	OAuthRefresh = "refresh"
)

// Kinds of client. Known and document clients have no row in oauth_clients.
const (
	ClientKnown      = "known"      // on Privo's built-in list
	ClientApproved   = "approved"   // a metadata document an administrator approved
	ClientDocument   = "document"   // any metadata document, accepted because the policy is "any"
	ClientRegistered = "registered" // registered itself (dynamic registration)
)

// GatewayRole reports whether a member with this role may use the gateway,
// and so may allow an AI client to use it as them (ADR 0011, ADR 0022 point
// 2). It mirrors auth.PermUseGateway; a test in internal/auth keeps the two
// in step.
func GatewayRole(role string) bool {
	return role == RoleOwner || role == RoleAdmin || role == RoleMember
}

// ClientGrant is one member's permission for one client to use the gateway
// as them.
type ClientGrant struct {
	ID         string
	UserID     string
	ClientID   string
	ClientName string
	ClientKind string
	Scope      string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
	// Person and DisplayName name the member; filled by ListClientGrants.
	Person, DisplayName string
}

// OAuthToken is a code or token of a grant, by its signature.
type OAuthToken struct {
	Kind      string
	Signature string
	GrantID   string
	Request   []byte // the library's request, as JSON
	Active    bool
	ExpiresAt time.Time
}

// OAuthClient is a client accepted by a row: approved or registered.
type OAuthClient struct {
	ClientID     string
	Kind         string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
	CreatedBy    string
	AuthorizedAt *time.Time // the last time a member allowed it
	Grants       int        // live grants; filled by ListOAuthClients
}

type oauthTxKey struct{}

// BeginOAuthTx starts a write transaction and returns a context that carries
// it. The transaction is bound to ctx: when ctx ends (the request is over)
// and it was neither committed nor rolled back, database/sql rolls it back,
// so a forgotten transaction cannot hold the single writer.
func (s *Store) BeginOAuthTx(ctx context.Context) (context.Context, error) {
	if _, ok := ctx.Value(oauthTxKey{}).(*sql.Tx); ok {
		return nil, errors.New("store: an OAuth transaction is already open")
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, oauthTxKey{}, tx), nil
}

// CommitOAuthTx commits the transaction ctx carries.
func (s *Store) CommitOAuthTx(ctx context.Context) error {
	tx, ok := ctx.Value(oauthTxKey{}).(*sql.Tx)
	if !ok {
		return errors.New("store: no OAuth transaction to commit")
	}
	return tx.Commit()
}

// RollbackOAuthTx rolls the transaction ctx carries back. Rolling back twice,
// or after a commit, is not an error.
func (s *Store) RollbackOAuthTx(ctx context.Context) error {
	tx, ok := ctx.Value(oauthTxKey{}).(*sql.Tx)
	if !ok {
		return nil
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

// dbtx is what both a transaction and a database handle can do.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// oauthW is where an OAuth write goes: the transaction of ctx, or the writer.
func (s *Store) oauthW(ctx context.Context) dbtx {
	if tx, ok := ctx.Value(oauthTxKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.w
}

// oauthR is where an OAuth read goes: the transaction of ctx, so it sees what
// the transaction wrote, or a reader.
func (s *Store) oauthR(ctx context.Context) dbtx {
	if tx, ok := ctx.Value(oauthTxKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.r
}

// CreateClientGrant records that an active member who may use the gateway
// allowed a client. It is ErrNotFound when the member does not exist, is
// disabled or has another role: the check and the insert are one statement.
// A grant with this id that already exists is left as it is.
func (s *Store) CreateClientGrant(ctx context.Context, org OrgID, g ClientGrant) error {
	if g.ID == "" || g.UserID == "" || g.ClientID == "" || g.Scope == "" {
		return errors.New("store: a client grant needs its id, member, client and scope")
	}
	res, err := s.oauthW(ctx).ExecContext(ctx, `
		INSERT INTO oauth_grants (org_id, id, user_id, client_id, client_name, client_kind, scope, created_at, expires_at, last_used_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM users WHERE org_id = ? AND id = ? AND disabled_at IS NULL AND role IN (?, ?, ?))
		ON CONFLICT (org_id, id) DO NOTHING`,
		org, g.ID, g.UserID, g.ClientID, g.ClientName, g.ClientKind, g.Scope, ts(g.CreatedAt), ts(g.ExpiresAt), ts(g.CreatedAt),
		org, g.UserID, RoleOwner, RoleAdmin, RoleMember)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either the member may not, or the grant exists already.
		var one int
		err := s.oauthR(ctx).QueryRowContext(ctx, `SELECT 1 FROM oauth_grants WHERE org_id = ? AND id = ? AND user_id = ?`, org, g.ID, g.UserID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// PutOAuthToken stores a code or token of an existing grant. It is
// ErrNotFound when the grant is gone (it was ended meanwhile) and ErrConflict
// when the signature exists.
func (s *Store) PutOAuthToken(ctx context.Context, org OrgID, t OAuthToken, now time.Time) error {
	if t.Signature == "" || t.GrantID == "" {
		return errors.New("store: a token needs its signature and grant")
	}
	res, err := s.oauthW(ctx).ExecContext(ctx, `
		INSERT INTO oauth_tokens (org_id, kind, signature, grant_id, request, active, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, 1, ?, ?
		WHERE EXISTS (SELECT 1 FROM oauth_grants WHERE org_id = ? AND id = ?)`,
		org, t.Kind, t.Signature, t.GrantID, string(t.Request), ts(now), ts(t.ExpiresAt), org, t.GrantID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OAuthTokenBySignature returns a code or token and its grant. Expiry is not
// judged here: the caller knows which clock applies.
func (s *Store) OAuthTokenBySignature(ctx context.Context, org OrgID, kind, signature string) (OAuthToken, ClientGrant, error) {
	t := OAuthToken{Kind: kind, Signature: signature}
	var g ClientGrant
	var request, expires, created, gExpires, used string
	err := s.oauthR(ctx).QueryRowContext(ctx, `
		SELECT t.grant_id, t.request, t.active, t.expires_at,
		       g.user_id, g.client_id, g.client_name, g.client_kind, g.scope, g.created_at, g.expires_at, g.last_used_at
		FROM oauth_tokens t JOIN oauth_grants g ON g.org_id = t.org_id AND g.id = t.grant_id
		WHERE t.org_id = ? AND t.kind = ? AND t.signature = ?`, org, kind, signature).
		Scan(&t.GrantID, &request, &t.Active, &expires, &g.UserID, &g.ClientID, &g.ClientName, &g.ClientKind, &g.Scope, &created, &gExpires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthToken{}, ClientGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthToken{}, ClientGrant{}, err
	}
	t.Request, t.ExpiresAt = []byte(request), parseTS(expires)
	g.ID, g.CreatedAt, g.ExpiresAt, g.LastUsedAt = t.GrantID, parseTS(created), parseTS(gExpires), parseTS(used)
	return t, g, nil
}

// DeactivateOAuthToken marks a code or refresh token as used and reports
// whether this call was the one that did it. Two uses at the same time
// cannot both get true.
func (s *Store) DeactivateOAuthToken(ctx context.Context, org OrgID, kind, signature string) (bool, error) {
	res, err := s.oauthW(ctx).ExecContext(ctx, `UPDATE oauth_tokens SET active = 0 WHERE org_id = ? AND kind = ? AND signature = ? AND active = 1`, org, kind, signature)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteOAuthToken removes one code or token. Removing one that is not there
// is not an error.
func (s *Store) DeleteOAuthToken(ctx context.Context, org OrgID, kind, signature string) error {
	_, err := s.oauthW(ctx).ExecContext(ctx, `DELETE FROM oauth_tokens WHERE org_id = ? AND kind = ? AND signature = ?`, org, kind, signature)
	return err
}

// DeleteAccessTokens removes the access tokens of a grant.
func (s *Store) DeleteAccessTokens(ctx context.Context, org OrgID, grantID string) error {
	_, err := s.oauthW(ctx).ExecContext(ctx, `DELETE FROM oauth_tokens WHERE org_id = ? AND grant_id = ? AND kind = ?`, org, grantID, OAuthAccess)
	return err
}

// RotateRefreshToken uses a refresh token up: it is marked used, the access
// tokens of the grant go, and the grant's last use is now. It is ErrConflict
// when the refresh token was already used: of two refreshes at the same time
// one wins.
func (s *Store) RotateRefreshToken(ctx context.Context, org OrgID, grantID, signature string, now time.Time) error {
	first, err := s.DeactivateOAuthToken(ctx, org, OAuthRefresh, signature)
	if err != nil {
		return err
	}
	if !first {
		return ErrConflict
	}
	if err := s.DeleteAccessTokens(ctx, org, grantID); err != nil {
		return err
	}
	_, err = s.oauthW(ctx).ExecContext(ctx, `UPDATE oauth_grants SET last_used_at = ? WHERE org_id = ? AND id = ?`, ts(now), org, grantID)
	return err
}

func scanClientGrant(row interface{ Scan(...any) error }, g *ClientGrant) error {
	var created, expires, used string
	if err := row.Scan(&g.ID, &g.UserID, &g.ClientID, &g.ClientName, &g.ClientKind, &g.Scope, &created, &expires, &used); err != nil {
		return err
	}
	g.CreatedAt, g.ExpiresAt, g.LastUsedAt = parseTS(created), parseTS(expires), parseTS(used)
	return nil
}

const clientGrantCols = `id, user_id, client_id, client_name, client_kind, scope, created_at, expires_at, last_used_at`

// endClientGrants deletes the grants a condition selects, with their codes
// and tokens, and returns what they were. where is a fixed fragment written
// in this file, never input.
func endClientGrants(ctx context.Context, db dbtx, org OrgID, where string, args ...any) ([]ClientGrant, error) {
	all := append([]any{org}, args...)
	rows, err := db.QueryContext(ctx, `SELECT `+clientGrantCols+` FROM oauth_grants WHERE org_id = ? AND `+where, all...)
	if err != nil {
		return nil, err
	}
	var out []ClientGrant
	for rows.Next() {
		var g ClientGrant
		if err := scanClientGrant(rows, &g); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, g := range out {
		if _, err := db.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE org_id = ? AND grant_id = ?`, org, g.ID); err != nil {
			return nil, err
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM oauth_grants WHERE org_id = ? AND id = ?`, org, g.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EndClientGrant deletes a grant with every code and token it has, inside the
// transaction of ctx if there is one, and returns what the grant was. It is
// ErrNotFound when there is no such grant.
func (s *Store) EndClientGrant(ctx context.Context, org OrgID, grantID string) (ClientGrant, error) {
	ended, err := endClientGrants(ctx, s.oauthW(ctx), org, `id = ?`, grantID)
	if err != nil {
		return ClientGrant{}, err
	}
	if len(ended) == 0 {
		return ClientGrant{}, ErrNotFound
	}
	return ended[0], nil
}

// RevokeClientGrant ends a grant on a person's word and writes the audit
// record with it. With userID set, only that member's grant is ended, so a
// member cannot end another's; "" is for an administrator.
func (s *Store) RevokeClientGrant(ctx context.Context, org OrgID, grantID, userID string, ev AuditEvent) (ClientGrant, error) {
	ev.Phase = "admin"
	var out ClientGrant
	err := s.tx(ctx, func(tx *sql.Tx) error {
		where, args := `id = ?`, []any{grantID}
		if userID != "" {
			where, args = `id = ? AND user_id = ?`, []any{grantID, userID}
		}
		ended, err := endClientGrants(ctx, tx, org, where, args...)
		if err != nil {
			return err
		}
		if len(ended) == 0 {
			return ErrNotFound
		}
		out = ended[0]
		ev.Client, ev.UserID = out.ClientName, out.UserID
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
	return out, err
}

// ListClientGrants lists the clients that can still act: grants that have not
// run out and hold a refresh token that was not used up. userID "" lists
// everyone's.
func (s *Store) ListClientGrants(ctx context.Context, org OrgID, userID string, now time.Time) ([]ClientGrant, error) {
	q := `SELECT g.id, g.user_id, g.client_id, g.client_name, g.client_kind, g.scope, g.created_at, g.expires_at, g.last_used_at,
	             COALESCE(u.person, ''), COALESCE(u.display_name, '')
	      FROM oauth_grants g LEFT JOIN users u ON u.org_id = g.org_id AND u.id = g.user_id
	      WHERE g.org_id = ? AND g.expires_at > ?
	        AND EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.org_id = g.org_id AND t.grant_id = g.id AND t.kind = 'refresh' AND t.active = 1 AND t.expires_at > ?)`
	args := []any{org, ts(now), ts(now)}
	if userID != "" {
		q += ` AND g.user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY g.created_at DESC, g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ClientGrant
	for rows.Next() {
		var g ClientGrant
		var created, expires, used string
		if err := rows.Scan(&g.ID, &g.UserID, &g.ClientID, &g.ClientName, &g.ClientKind, &g.Scope, &created, &expires, &used, &g.Person, &g.DisplayName); err != nil {
			return nil, err
		}
		g.CreatedAt, g.ExpiresAt, g.LastUsedAt = parseTS(created), parseTS(expires), parseTS(used)
		out = append(out, g)
	}
	return out, rows.Err()
}

// endUserClientGrants ends every grant of a member inside the caller's
// transaction: a member who is removed, or whose new role may not use the
// gateway, stops being someone a client can act as (ADR 0022 point 3).
func endUserClientGrants(ctx context.Context, tx *sql.Tx, org OrgID, userID string) error {
	_, err := endClientGrants(ctx, tx, org, `user_id = ?`, userID)
	return err
}

// SaveOAuthClient adds a client that is accepted by a row. For a registered
// client limit bounds how many registered clients there may be in total;
// ErrConflict means the client exists, ErrTooManyClients that the bound is
// reached. ev, if set, is written in the same transaction.
func (s *Store) SaveOAuthClient(ctx context.Context, org OrgID, c OAuthClient, limit int, ev *AuditEvent) error {
	if c.ClientID == "" || (c.Kind != ClientApproved && c.Kind != ClientRegistered) {
		return errors.New("store: a stored client needs its id and is approved or registered")
	}
	uris, err := json.Marshal(append([]string{}, c.RedirectURIs...))
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if c.Kind == ClientRegistered && limit > 0 {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_clients WHERE org_id = ? AND kind = ?`, org, ClientRegistered).Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return ErrTooManyClients
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_clients (org_id, client_id, kind, name, redirect_uris, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			org, c.ClientID, c.Kind, c.Name, string(uris), ts(c.CreatedAt), c.CreatedBy); err != nil {
			return mapErr(err)
		}
		if ev != nil {
			ev.Phase = "admin"
			return insertAudit(ctx, tx, org, []AuditEvent{*ev})
		}
		return nil
	})
}

// ErrTooManyClients: the bound on self-registered clients is reached.
var ErrTooManyClients = errors.New("store: too many registered clients")

func scanOAuthClient(row interface{ Scan(...any) error }, c *OAuthClient, extra ...any) error {
	var uris, created string
	var authorized sql.NullString
	if err := row.Scan(append([]any{&c.ClientID, &c.Kind, &c.Name, &uris, &created, &c.CreatedBy, &authorized}, extra...)...); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return err
	}
	c.CreatedAt = parseTS(created)
	if authorized.Valid {
		t := parseTS(authorized.String)
		c.AuthorizedAt = &t
	}
	return nil
}

// OAuthClient returns a stored client.
func (s *Store) OAuthClient(ctx context.Context, org OrgID, clientID string) (OAuthClient, error) {
	var c OAuthClient
	err := scanOAuthClient(s.oauthR(ctx).QueryRowContext(ctx, `SELECT client_id, kind, name, redirect_uris, created_at, created_by, authorized_at
		FROM oauth_clients WHERE org_id = ? AND client_id = ?`, org, clientID), &c)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	return c, err
}

// ListOAuthClients lists the stored clients with their number of grants.
func (s *Store) ListOAuthClients(ctx context.Context, org OrgID) ([]OAuthClient, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT c.client_id, c.kind, c.name, c.redirect_uris, c.created_at, c.created_by, c.authorized_at,
		(SELECT COUNT(*) FROM oauth_grants g WHERE g.org_id = c.org_id AND g.client_id = c.client_id)
		FROM oauth_clients c WHERE c.org_id = ? ORDER BY c.kind, c.name, c.client_id`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OAuthClient
	for rows.Next() {
		var c OAuthClient
		if err := scanOAuthClient(rows, &c, &c.Grants); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkOAuthClientAuthorized notes that a member allowed a stored client now,
// so a registration nobody uses can be told from one in use.
func (s *Store) MarkOAuthClientAuthorized(ctx context.Context, org OrgID, clientID string, now time.Time) error {
	_, err := s.oauthW(ctx).ExecContext(ctx, `UPDATE oauth_clients SET authorized_at = ? WHERE org_id = ? AND client_id = ?`, ts(now), org, clientID)
	return err
}

// DeleteOAuthClient removes a stored client and ends every grant it has, with
// the audit record, in one transaction. It returns how many grants ended.
func (s *Store) DeleteOAuthClient(ctx context.Context, org OrgID, clientID string, ev AuditEvent) (int, error) {
	ev.Phase = "admin"
	ended := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM oauth_clients WHERE org_id = ? AND client_id = ?`, org, clientID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		gs, err := endClientGrants(ctx, tx, org, `client_id = ?`, clientID)
		if err != nil {
			return err
		}
		ended = len(gs)
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
	return ended, err
}

// EndClientGrantsOfClient ends every grant of a client that has no row (a
// known or document client an administrator no longer wants), with the audit
// record. It returns how many ended.
func (s *Store) EndClientGrantsOfClient(ctx context.Context, org OrgID, clientID string, ev AuditEvent) (int, error) {
	ev.Phase = "admin"
	ended := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		gs, err := endClientGrants(ctx, tx, org, `client_id = ?`, clientID)
		if err != nil {
			return err
		}
		ended = len(gs)
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
	return ended, err
}

// OAuthSigningKey returns the sealed secret the tokens are signed with and
// the version of the data key it is sealed under.
func (s *Store) OAuthSigningKey(ctx context.Context, org OrgID) (uint32, []byte, error) {
	var version uint32
	var sealed []byte
	err := s.r.QueryRowContext(ctx, `SELECT key_version, secret FROM oauth_keys WHERE org_id = ?`, org).Scan(&version, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	return version, sealed, err
}

// CreateOAuthSigningKey stores the sealed signing secret unless the org has
// one. The caller reads it back: of two processes starting together, both
// then use the one that was stored first.
func (s *Store) CreateOAuthSigningKey(ctx context.Context, org OrgID, version uint32, sealed []byte, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO oauth_keys (org_id, key_version, secret, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (org_id) DO NOTHING`,
		org, version, sealed, ts(now))
	return err
}

// PruneOAuth deletes what can no longer be used: codes and tokens past their
// time, grants past theirs or left without any code or token, and
// registrations nobody allowed within unusedFor of registering or that
// nobody has allowed for idleFor and that have no grant left.
func (s *Store) PruneOAuth(ctx context.Context, org OrgID, now time.Time, unusedFor, idleFor time.Duration) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE org_id = ? AND expires_at <= ?`, org, ts(now)); err != nil {
			return err
		}
		if _, err := endClientGrants(ctx, tx, org,
			`(expires_at <= ? OR NOT EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.org_id = oauth_grants.org_id AND t.grant_id = oauth_grants.id))`, ts(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			DELETE FROM oauth_clients WHERE org_id = ? AND kind = ?
			  AND NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.org_id = oauth_clients.org_id AND g.client_id = oauth_clients.client_id)
			  AND ((authorized_at IS NULL AND created_at <= ?) OR (authorized_at IS NOT NULL AND authorized_at <= ?))`,
			org, ClientRegistered, ts(now.Add(-unusedFor)), ts(now.Add(-idleFor)))
		return err
	})
}
