package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

// Providers and purposes for web sign-in (ADR 0016).
const (
	ProviderGitHub = "github"

	PurposeSignIn  = "signin"
	PurposeSetup   = "setup"
	PurposeInvite  = "invite"  // claiming an invite (ADR 0017)
	PurposeConnect = "connect" // connecting an upstream account (ADR 0017)
)

// maxSetupLinkTTL caps a setup link whatever the caller asks for.
const maxSetupLinkTTL = 24 * time.Hour

// touchEvery is the least time between two writes of a session's last_seen_at.
const touchEvery = time.Minute

// Errors of the sign-in store.
var (
	// ErrSetupUnavailable: the setup link is unknown, expired, already used or
	// replaced by a newer one.
	ErrSetupUnavailable = errors.New("store: the setup link is unknown, expired, used or retired")
	// ErrIdentityExists: the identity already belongs to a member.
	ErrIdentityExists = errors.New("store: this identity already belongs to a member")
	// ErrTooManyStates: too many sign-ins are in flight.
	ErrTooManyStates = errors.New("store: too many sign-ins in progress")
)

// User is a person who can sign in to the admin site.
type User struct {
	OrgID       OrgID
	ID          string
	Person      string // what self-approval compares with a token's person
	DisplayName string
	Roles       []string // sorted; a valid set (NormalizeRoles, ADR 0021)
	CreatedAt   time.Time
	DisabledAt  *time.Time
	Login       string // the GitHub login snapshot; filled by ListUsers only
}

// Session is a browser session. ID is the hex SHA-256 of the cookie secret;
// the secret itself is never stored.
type Session struct {
	ID         string
	UserID     string
	CSRF       string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// NewSetupLink describes a setup link to store. SecretHash is the hex SHA-256 of
// the token that is shown once.
type NewSetupLink struct {
	ID         string
	SecretHash string
	Person     string
	CreatedBy  string
	Now        time.Time
	ExpiresAt  time.Time
}

// SetupLink is a setup link that can still be claimed.
type SetupLink struct {
	ID        string
	Person    string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSetupLink stores a link, retires the org's older unclaimed links and
// writes the audit record ev (as an admin event), all in one transaction.
func (s *Store) CreateSetupLink(ctx context.Context, org OrgID, l NewSetupLink, ev AuditEvent) error {
	if l.ID == "" || l.SecretHash == "" {
		return errors.New("store: a setup link needs an id and a secret hash")
	}
	if !l.ExpiresAt.After(l.Now) || l.ExpiresAt.After(l.Now.Add(maxSetupLinkTTL)) {
		return errors.New("store: a setup link must expire within 24 hours")
	}
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE setup_links SET retired_at = ?
			WHERE org_id = ? AND claimed_at IS NULL AND retired_at IS NULL`, ts(l.Now), org); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO setup_links (org_id, id, secret_hash, person, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			org, l.ID, l.SecretHash, l.Person, l.CreatedBy, ts(l.Now), ts(l.ExpiresAt)); err != nil {
			return mapErr(err)
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// SetupLink finds the claimable link with this secret hash, or ErrNotFound when
// it is unknown, expired, claimed or retired.
func (s *Store) SetupLink(ctx context.Context, org OrgID, secretHash string, now time.Time) (SetupLink, error) {
	var l SetupLink
	var created, expires string
	err := s.r.QueryRowContext(ctx, `
		SELECT id, person, created_by, created_at, expires_at FROM setup_links
		WHERE org_id = ? AND secret_hash = ? AND claimed_at IS NULL AND retired_at IS NULL AND expires_at > ?`,
		org, secretHash, ts(now)).Scan(&l.ID, &l.Person, &l.CreatedBy, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	l.CreatedAt, l.ExpiresAt = parseTS(created), parseTS(expires)
	return l, nil
}

// Claim is what a successful GitHub sign-in hands to ClaimSetup.
type Claim struct {
	LinkID      string
	UserID      string  // id for the new user
	Person      string  // the new user's person name
	Subject     string  // the provider's stable id (GitHub: numeric id)
	Login       string  // display snapshot
	DisplayName string  // display snapshot
	Session     Session // the session to start (UserID is filled in)
	Now         time.Time
}

// ClaimSetup turns a setup link into the org's owner. The person name is the one
// stored on the link when it has one, otherwise c.Person. In one transaction it
// consumes the link, creates the owner user, binds the identity, starts the
// session and writes the audit record ev; if any step fails nothing is kept, so
// a refused claim leaves the link usable. Of several concurrent claims exactly
// one succeeds and the others get ErrSetupUnavailable. It returns
// ErrIdentityExists when the identity is already a member and ErrConflict when
// the person name is taken.
func (s *Store) ClaimSetup(ctx context.Context, org OrgID, c Claim, ev AuditEvent) (User, error) {
	return s.claim(ctx, org, c, ev, func(tx *sql.Tx, u *User) error {
		var linkPerson string
		err := tx.QueryRowContext(ctx, `
			UPDATE setup_links SET claimed_at = ?, claimed_by = ?
			WHERE org_id = ? AND id = ? AND claimed_at IS NULL AND retired_at IS NULL AND expires_at > ?
			RETURNING person`,
			ts(c.Now), c.UserID, org, c.LinkID, ts(c.Now)).Scan(&linkPerson)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupUnavailable
		}
		if err != nil {
			return err
		}
		// A person named on the link (privo admin setup-link --person) wins over
		// the caller's fallback, which is the lowercased GitHub login.
		u.Roles = []string{RoleOwner}
		if linkPerson != "" {
			u.Person = linkPerson
		}
		return nil
	})
}

// claim is the part of a setup or invite claim after the link was consumed by
// consume (which sets the role and may set the person): it refuses an identity
// that is already a member, creates the user, binds the identity, starts the
// session and writes ev, all in one transaction.
func (s *Store) claim(ctx context.Context, org OrgID, c Claim, ev AuditEvent, consume func(*sql.Tx, *User) error) (User, error) {
	ev.Phase = "admin"
	u := User{OrgID: org, ID: c.UserID, Person: c.Person, DisplayName: c.DisplayName, CreatedAt: c.Now}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := consume(tx, &u); err != nil {
			return err
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM identities WHERE org_id = ? AND provider = ? AND subject = ?`,
			org, ProviderGitHub, c.Subject).Scan(&one); {
		case err == nil:
			return ErrIdentityExists
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		roles, err := NormalizeRoles(u.Roles)
		if err != nil {
			return err
		}
		u.Roles = roles
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO users (org_id, id, person, display_name, created_at) VALUES (?, ?, ?, ?, ?)`,
			org, u.ID, u.Person, u.DisplayName, ts(c.Now)); err != nil {
			return mapErr(err)
		}
		if err := insertRoles(ctx, tx, org, u.ID, roles, ev.ActorID, c.Now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO identities (org_id, provider, subject, user_id, login, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			org, ProviderGitHub, c.Subject, u.ID, c.Login, ts(c.Now)); err != nil {
			return mapErr(err)
		}
		sess := c.Session
		sess.UserID = u.ID
		if err := insertSession(ctx, tx, org, sess); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// userCols selects a user u with their roles, comma-joined.
const userCols = `u.id, u.person, u.display_name,
	COALESCE((SELECT group_concat(r.role, ',') FROM user_roles r WHERE r.org_id = u.org_id AND r.user_id = u.id), ''),
	u.created_at, u.disabled_at`

func scanUser(org OrgID, row interface{ Scan(...any) error }, extra ...any) (User, error) {
	u := User{OrgID: org}
	var created, roles string
	var disabled sql.NullString
	dest := append([]any{&u.ID, &u.Person, &u.DisplayName, &roles, &created, &disabled}, extra...)
	if err := row.Scan(dest...); err != nil {
		return User{}, err
	}
	u.Roles = splitRoles(roles)
	u.CreatedAt = parseTS(created)
	if disabled.Valid {
		d := parseTS(disabled.String)
		u.DisabledAt = &d
	}
	return u, nil
}

// UserByIdentity finds the user an identity belongs to, including a disabled
// one (DisabledAt is set), or ErrNotFound.
func (s *Store) UserByIdentity(ctx context.Context, org OrgID, provider, subject string) (User, error) {
	u, err := scanUser(org, s.r.QueryRowContext(ctx, `
		SELECT `+userCols+` FROM identities i
		JOIN users u ON u.org_id = i.org_id AND u.id = i.user_id
		WHERE i.org_id = ? AND i.provider = ? AND i.subject = ?`, org, provider, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func insertSession(ctx context.Context, tx *sql.Tx, org OrgID, s Session) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (org_id, id, user_id, csrf, created_at, last_seen_at, expires_at)
		SELECT ?, ?, id, ?, ?, ?, ? FROM users WHERE org_id = ? AND id = ? AND disabled_at IS NULL`,
		org, s.ID, s.CSRF, ts(s.CreatedAt), ts(s.LastSeenAt), ts(s.ExpiresAt), org, s.UserID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound // no such user, or a disabled one
	}
	return nil
}

// CreateSession stores a session for an active user and writes the audit record
// ev (as an admin event) in the same transaction.
func (s *Store) CreateSession(ctx context.Context, org OrgID, sess Session, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := insertSession(ctx, tx, org, sess); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// SessionByHash returns a session and its live user. It is ErrNotFound when the
// session is unknown, past its absolute lifetime, idle for longer than idle, or
// belongs to a disabled user, so a role change or a disabled user takes effect
// on the next request.
func (s *Store) SessionByHash(ctx context.Context, org OrgID, hash string, now time.Time, idle time.Duration) (Session, User, error) {
	sess := Session{ID: hash}
	var created, seen, expires string
	u, err := scanUser(org, s.r.QueryRowContext(ctx, `
		SELECT `+userCols+`, s.csrf, s.created_at, s.last_seen_at, s.expires_at
		FROM sessions s JOIN users u ON u.org_id = s.org_id AND u.id = s.user_id
		WHERE s.org_id = ? AND s.id = ? AND s.expires_at > ? AND s.last_seen_at > ? AND u.disabled_at IS NULL`,
		org, hash, ts(now), ts(now.Add(-idle))), &sess.CSRF, &created, &seen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, err
	}
	sess.UserID = u.ID
	sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt = parseTS(created), parseTS(seen), parseTS(expires)
	return sess, u, nil
}

// TouchSession records activity. It writes at most once a minute per session, so
// a busy page does not turn every request into a write.
func (s *Store) TouchSession(ctx context.Context, org OrgID, hash string, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `
		UPDATE sessions SET last_seen_at = ? WHERE org_id = ? AND id = ? AND last_seen_at < ?`,
		ts(now), org, hash, ts(now.Add(-touchEvery)))
	return err
}

// DeleteSession ends a session and writes the audit record ev, in one
// transaction. Deleting a session that does not exist is not an error and
// writes nothing.
func (s *Store) DeleteSession(ctx context.Context, org OrgID, hash string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE org_id = ? AND id = ?`, org, hash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// Has reports whether the user holds role.
func (u User) Has(role string) bool { return slices.Contains(u.Roles, role) }

// SetUserRoles replaces a user's roles with a valid set (NormalizeRoles) and
// writes the audit record ev, in one transaction. Roles the user keeps keep
// their grant time; new ones are granted by ev's actor. Losing owner and admin
// drops the user's catalog grants. It returns the stored set.
func (s *Store) SetUserRoles(ctx context.Context, org OrgID, userID string, roles []string, ev AuditEvent) ([]string, error) {
	roles, err := NormalizeRoles(roles)
	if err != nil {
		return nil, err
	}
	ev.Phase = "admin"
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE org_id = ? AND id = ?`, org, userID).Scan(&one); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE org_id = ? AND user_id = ? AND role NOT IN (`+placeholders(len(roles))+`)`,
			append([]any{org, userID}, anySlice(roles)...)...); err != nil {
			return err
		}
		if err := insertRoles(ctx, tx, org, userID, roles, ev.ActorID, ev.Time); err != nil {
			return err
		}
		if !catalogRoles(roles) {
			if err := dropCatalogGrants(ctx, tx, org, userID); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
	if err != nil {
		return nil, err
	}
	return roles, nil
}

// insertRoles grants roles a user does not hold yet.
func insertRoles(ctx context.Context, tx *sql.Tx, org OrgID, userID string, roles []string, by string, at time.Time) error {
	for _, r := range roles {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_roles (org_id, user_id, role, source, granted_by, granted_at) VALUES (?, ?, ?, 'manual', ?, ?)
			ON CONFLICT (org_id, user_id, role) DO NOTHING`, org, userID, r, by, ts(at)); err != nil {
			return err
		}
	}
	return nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// DisableUser stops a user from signing in, ends all of their sessions and
// writes the audit record ev. It is ErrNotFound for an unknown or already
// disabled user.
func (s *Store) DisableUser(ctx context.Context, org OrgID, userID string, now time.Time, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = ? WHERE org_id = ? AND id = ? AND disabled_at IS NULL`, ts(now), org, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE org_id = ? AND user_id = ?`, org, userID); err != nil {
			return err
		}
		if err := deleteUserGrants(ctx, tx, org, userID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// OAuthState is a sign-in in flight. Hash is the hex SHA-256 of the state value
// sent to the provider.
type OAuthState struct {
	Hash        string
	Purpose     string
	SetupLinkID string
	InviteID    string
	Verifier    string // PKCE code verifier
	// For PurposeConnect: who started it, from which session, for which
	// server, and the authorization server expected to answer.
	UserID    string
	SessionID string
	ServerID  string
	Issuer    string
	// ReturnTo is a local path to go back to after signing in; the web
	// layer accepts only fixed shapes.
	ReturnTo  string
	ExpiresAt time.Time
}

// CreateOAuthState stores a sign-in in flight. It first deletes expired rows,
// then refuses with ErrTooManyStates when limit unexpired ones exist, so
// unauthenticated requests cannot fill the disk.
func (s *Store) CreateOAuthState(ctx context.Context, org OrgID, st OAuthState, now time.Time, limit int) error {
	switch st.Purpose {
	case PurposeSignIn, PurposeSetup, PurposeInvite, PurposeConnect:
	default:
		return errors.New("store: unknown sign-in purpose")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE org_id = ? AND expires_at <= ?`, org, ts(now)); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_states WHERE org_id = ?`, org).Scan(&n); err != nil {
			return err
		}
		if n >= limit {
			return ErrTooManyStates
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_states (org_id, state_hash, purpose, setup_link_id, invite_id, verifier, user_id, session_id, server_id, issuer, return_to, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			org, st.Hash, st.Purpose, st.SetupLinkID, st.InviteID, st.Verifier, st.UserID, st.SessionID, st.ServerID, st.Issuer, st.ReturnTo, ts(now), ts(st.ExpiresAt))
		return mapErr(err)
	})
}

// UseOAuthState consumes a state: it returns it and deletes it in one
// statement, so a second use finds nothing. An expired or unknown state is
// ErrNotFound.
func (s *Store) UseOAuthState(ctx context.Context, org OrgID, hash string, now time.Time) (OAuthState, error) {
	st := OAuthState{Hash: hash}
	var expires string
	err := s.w.QueryRowContext(ctx, `
		DELETE FROM oauth_states WHERE org_id = ? AND state_hash = ? AND expires_at > ?
		RETURNING purpose, setup_link_id, invite_id, verifier, user_id, session_id, server_id, issuer, return_to, expires_at`, org, hash, ts(now)).
		Scan(&st.Purpose, &st.SetupLinkID, &st.InviteID, &st.Verifier, &st.UserID, &st.SessionID, &st.ServerID, &st.Issuer, &st.ReturnTo, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthState{}, ErrNotFound
	}
	if err != nil {
		return OAuthState{}, err
	}
	st.ExpiresAt = parseTS(expires)
	return st, nil
}

// PruneSignIn deletes what can no longer be used: sessions past their lifetime
// or idle for longer than idle, expired sign-ins in flight, and setup links that
// expired or were retired without being claimed. Claimed links stay as a record.
func (s *Store) PruneSignIn(ctx context.Context, org OrgID, now time.Time, idle time.Duration) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM sessions WHERE org_id = ? AND (expires_at <= ? OR last_seen_at <= ?)`, []any{org, ts(now), ts(now.Add(-idle))}},
			{`DELETE FROM oauth_states WHERE org_id = ? AND expires_at <= ?`, []any{org, ts(now)}},
			{`DELETE FROM setup_links WHERE org_id = ? AND claimed_at IS NULL AND (retired_at IS NOT NULL OR expires_at <= ?)`, []any{org, ts(now)}},
			{`DELETE FROM invites WHERE org_id = ? AND claimed_at IS NULL AND (retired_at IS NOT NULL OR expires_at <= ?)`, []any{org, ts(now)}},
		} {
			if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
}
