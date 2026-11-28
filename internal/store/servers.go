package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"privo.club/privo/internal/ids"
)

// Server is a registered upstream MCP server. Config never holds secrets.
type Server struct {
	OrgID     OrgID
	ID        string
	Alias     string
	Transport string // stdio | http | sse
	Config    ServerConfig
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
	// Teams that reach the server (ADR 0020). CreateServer stores them; nil
	// means "everyone". Reads leave it empty: see ServerTeams.
	Teams []string
}

// ServerConfig is the non-secret part of a server's configuration.
type ServerConfig struct {
	// stdio
	Path   string   `json:"path,omitempty"`
	SHA256 string   `json:"sha256,omitempty"`
	Args   []string `json:"args,omitempty"`
	// http, sse
	URL string `json:"url,omitempty"`
	// Names of secret env vars and headers (values live in credentials).
	EnvNames    []string `json:"env_names,omitempty"`
	HeaderNames []string `json:"header_names,omitempty"`
	// Auth is "" (none: no auth or the static secrets above), AuthOAuth
	// (each person's own grant, ADR 0017) or AuthKey (each person's own API
	// key, ADR 0023).
	Auth  string       `json:"auth,omitempty"`
	OAuth *ServerOAuth `json:"oauth,omitempty"`
	Key   *ServerKey   `json:"key,omitempty"`
}

// PerPerson reports whether each person brings their own credential to the
// server, so a call runs with the caller's own and never anyone else's.
func (c ServerConfig) PerPerson() bool {
	return c.Auth == AuthOAuth || c.Auth == AuthKey
}

// ServerKey says how each person's own API key is sent to the server (ADR
// 0023). It holds no key: keys are credentials, one per person.
type ServerKey struct {
	// Header is the request header the key goes in, canonical form.
	Header string `json:"header"`
	// Prefix goes before the key in the header ("Bearer " for most servers).
	Prefix string `json:"prefix,omitempty"`
	// Help tells a person where to get their key; HelpURL links there.
	Help    string `json:"help,omitempty"`
	HelpURL string `json:"help_url,omitempty"`
}

// Auth modes and client registration methods (ADR 0017).
const (
	AuthOAuth = "oauth"
	AuthKey   = "key"

	RegistrationAdmin   = "admin"   // a client id the admin entered
	RegistrationCIMD    = "cimd"    // Privo's client ID metadata document
	RegistrationDynamic = "dynamic" // RFC 7591 dynamic registration
	RegistrationManual  = "manual"  // settings and client id entered by hand (ADR 0023)

	// Credential purposes for upstream OAuth.
	PurposeOAuthGrant  = "oauth"        // a person's tokens (owner_id = user id)
	PurposeOAuthClient = "oauth:client" // the client secret, when there is one
	PurposeKeyGrant    = "key"          // a person's own API key (owner_id = user id, ADR 0023)
)

// ServerOAuth is what discovery and registration found for an oauth server.
// It holds no secret: a client secret is a credential (PurposeOAuthClient).
type ServerOAuth struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint,omitempty"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	IssParameter          bool     `json:"iss_parameter,omitempty"` // the AS sends iss in authorization responses
	Resource              string   `json:"resource"`
	Scopes                []string `json:"scopes,omitempty"`
	ClientID              string   `json:"client_id"`
	Registration          string   `json:"registration"`
	HasClientSecret       bool     `json:"has_client_secret,omitempty"`
	// IssuerNamed says the admin named the authorization server (oauth.issuer)
	// rather than Privo finding it from the MCP server: only then may a client
	// secret be sent there (F07 R-05). Hand-entered settings are named too.
	IssuerNamed     bool      `json:"issuer_named,omitempty"`
	TokenAuthMethod string    `json:"token_auth_method,omitempty"` // none | client_secret_basic | client_secret_post
	DiscoveredAt    time.Time `json:"discovered_at"`
}

// Credential is an encrypted secret attached to a server.
type Credential struct {
	ID         string
	ServerID   string
	OwnerID    string
	Purpose    string // env:<NAME> | header:<Name>
	KeyVersion uint32
	RowVersion uint64
	Ciphertext []byte
}

// CreateServer inserts a server, its credentials and its teams atomically.
// It is ErrUnknownTeam when a team does not exist.
func (s *Store) CreateServer(ctx context.Context, srv Server, creds []Credential) error {
	cfg, err := json.Marshal(srv.Config)
	if err != nil {
		return err
	}
	teams := srv.Teams
	if teams == nil {
		teams = []string{TeamEveryone}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := checkTeams(ctx, tx, srv.OrgID, teams); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO servers (org_id, id, alias, transport, config, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			srv.OrgID, srv.ID, srv.Alias, srv.Transport, string(cfg), srv.Enabled, ts(srv.CreatedAt), ts(srv.CreatedAt))
		if err != nil {
			return mapErr(err)
		}
		for _, c := range creds {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO credentials (org_id, id, server_id, owner_id, purpose, key_version, row_version, ciphertext)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				srv.OrgID, c.ID, srv.ID, c.OwnerID, c.Purpose, c.KeyVersion, c.RowVersion, c.Ciphertext)
			if err != nil {
				return mapErr(err)
			}
		}
		return insertServerTeams(ctx, tx, srv.OrgID, srv.ID, teams)
	})
}

func scanServer(org OrgID, sc interface{ Scan(...any) error }) (Server, error) {
	srv := Server{OrgID: org}
	var cfg, created, updated string
	if err := sc.Scan(&srv.ID, &srv.Alias, &srv.Transport, &cfg, &srv.Enabled, &created, &updated); err != nil {
		return srv, err
	}
	if err := json.Unmarshal([]byte(cfg), &srv.Config); err != nil {
		return srv, err
	}
	srv.CreatedAt, srv.UpdatedAt = parseTS(created), parseTS(updated)
	return srv, nil
}

// ListServers lists an org's servers ordered by alias.
func (s *Store) ListServers(ctx context.Context, org OrgID) ([]Server, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, alias, transport, config, enabled, created_at, updated_at
		FROM servers WHERE org_id = ? ORDER BY alias`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Server
	for rows.Next() {
		srv, err := scanServer(org, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// ServerByAlias returns one server.
func (s *Store) ServerByAlias(ctx context.Context, org OrgID, alias string) (Server, error) {
	srv, err := scanServer(org, s.r.QueryRowContext(ctx, `
		SELECT id, alias, transport, config, enabled, created_at, updated_at
		FROM servers WHERE org_id = ? AND alias = ?`, org, alias))
	if errors.Is(err, sql.ErrNoRows) {
		return srv, ErrNotFound
	}
	return srv, err
}

// ServerByID returns one server.
func (s *Store) ServerByID(ctx context.Context, org OrgID, id string) (Server, error) {
	srv, err := scanServer(org, s.r.QueryRowContext(ctx, `
		SELECT id, alias, transport, config, enabled, created_at, updated_at
		FROM servers WHERE org_id = ? AND id = ?`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return srv, ErrNotFound
	}
	return srv, err
}

// SetServerEnabled enables or disables a server.
func (s *Store) SetServerEnabled(ctx context.Context, org OrgID, id string, enabled bool) error {
	res, err := s.w.ExecContext(ctx, `UPDATE servers SET enabled = ?, updated_at = ? WHERE org_id = ? AND id = ?`,
		enabled, ts(time.Now()), org, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteServer removes a server, its credentials and pins.
func (s *Store) DeleteServer(ctx context.Context, org OrgID, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM credentials WHERE org_id = ? AND server_id = ?`,
			`DELETE FROM upstream_grants WHERE org_id = ? AND server_id = ?`,
			`DELETE FROM pins WHERE org_id = ? AND server_id = ?`,
			`DELETE FROM server_teams WHERE org_id = ? AND server_id = ?`,
			`DELETE FROM flow_tags WHERE org_id = ? AND server_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, org, id); err != nil {
				return err
			}
		}
		// Rules name servers by alias, which can be reused: drop them with the server.
		if _, err := tx.ExecContext(ctx, `DELETE FROM policies WHERE org_id = ? AND server = (SELECT alias FROM servers WHERE org_id = ? AND id = ?)`, org, org, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE org_id = ? AND id = ?`, org, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ServerCredentials returns the encrypted credentials of a server.
func (s *Store) ServerCredentials(ctx context.Context, org OrgID, serverID string) ([]Credential, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, owner_id, purpose, key_version, row_version, ciphertext
		FROM credentials WHERE org_id = ? AND server_id = ? ORDER BY purpose`, org, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c := Credential{ServerID: serverID}
		if err := rows.Scan(&c.ID, &c.OwnerID, &c.Purpose, &c.KeyVersion, &c.RowVersion, &c.Ciphertext); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateServerOAuth replaces an oauth server's discovered settings and its
// client secret: secret nil removes the old one.
func (s *Store) UpdateServerOAuth(ctx context.Context, org OrgID, serverID string, o ServerOAuth, secret *Credential) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT config FROM servers WHERE org_id = ? AND id = ?`, org, serverID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return err
		}
		cfg.Auth, cfg.OAuth = AuthOAuth, &o
		b, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE servers SET config = ?, updated_at = ? WHERE org_id = ? AND id = ?`,
			string(b), ts(time.Now()), org, serverID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE org_id = ? AND server_id = ? AND purpose = ?`,
			org, serverID, PurposeOAuthClient); err != nil {
			return err
		}
		if secret == nil {
			return nil
		}
		if secret.ServerID != serverID || secret.Purpose != PurposeOAuthClient || secret.OwnerID != "" {
			return errors.New("store: the client secret credential does not belong to this server")
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO credentials (org_id, id, server_id, owner_id, purpose, key_version, row_version, ciphertext)
			VALUES (?, ?, ?, '', ?, ?, ?, ?)`,
			org, secret.ID, serverID, secret.Purpose, secret.KeyVersion, secret.RowVersion, secret.Ciphertext)
		return mapErr(err)
	})
}

// ServerEdit is one edit of a server (ADR 0029).
type ServerEdit struct {
	// Version is the server's updated_at when the edit was built from it.
	// An edit of a server changed since is refused (ErrConflict), so it
	// cannot undo another edit (security review of the third slice, M1).
	Version time.Time
	// Config is the whole new configuration.
	Config ServerConfig
	// NewAddress deletes every static header credential that SetCreds does
	// not give again: values entered for one address never go to another.
	NewAddress bool
	// SetCreds are sealed server credentials (owner ""), each replacing the
	// server's credential with the same purpose.
	SetCreds []Credential
	// RemoveCreds are purposes of server credentials to delete.
	RemoveCreds []string
	// EndConnections deletes every person's grant or key for the server.
	EndConnections bool
	// ReviewAgain puts every accepted definition of the server back into
	// review.
	ReviewAgain bool
}

// EditOutcome says what an edit ended.
type EditOutcome struct {
	Ended    int // people's connections ended
	Reviewed int // accepted definitions sent back to review
}

// EditServer applies an edit in one transaction with evs, and records how
// many connections it ended (server.connections_ended:N) and definitions it
// sent back to review (server.review_again:N) as events like evs[0]. It is
// ErrNotFound for a server that is not the org's and ErrConflict for one
// changed since e.Version.
func (s *Store) EditServer(ctx context.Context, org OrgID, serverID string, e ServerEdit, evs []AuditEvent) (EditOutcome, error) {
	cfg, err := json.Marshal(e.Config)
	if err != nil {
		return EditOutcome{}, err
	}
	for _, c := range e.SetCreds {
		if c.ServerID != serverID || c.OwnerID != "" || c.Purpose == "" {
			return EditOutcome{}, errors.New("store: a credential of the edit does not belong to this server")
		}
	}
	if e.Version.IsZero() {
		return EditOutcome{}, errors.New("store: an edit needs the version it was built from")
	}
	var out EditOutcome
	err = s.tx(ctx, func(tx *sql.Tx) error {
		out = EditOutcome{}
		now := time.Now()
		if !now.After(e.Version) {
			now = e.Version.Add(time.Microsecond) // updated_at always moves forward
		}
		res, err := tx.ExecContext(ctx, `UPDATE servers SET config = ?, updated_at = ? WHERE org_id = ? AND id = ? AND updated_at = ?`,
			string(cfg), ts(now), org, serverID, ts(e.Version))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE org_id = ? AND id = ?`, org, serverID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			return ErrConflict
		}
		if e.NewAddress {
			if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE org_id = ? AND server_id = ? AND owner_id = '' AND purpose LIKE 'header:%'`, org, serverID); err != nil {
				return err
			}
		}
		for _, p := range e.RemoveCreds {
			if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE org_id = ? AND server_id = ? AND owner_id = '' AND purpose = ?`, org, serverID, p); err != nil {
				return err
			}
		}
		for _, c := range e.SetCreds {
			if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE org_id = ? AND server_id = ? AND owner_id = '' AND purpose = ?`, org, serverID, c.Purpose); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO credentials (org_id, id, server_id, owner_id, purpose, key_version, row_version, ciphertext)
				VALUES (?, ?, ?, '', ?, ?, ?, ?)`,
				org, c.ID, serverID, c.Purpose, c.KeyVersion, c.RowVersion, c.Ciphertext); err != nil {
				return mapErr(err)
			}
		}
		if e.EndConnections {
			if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE org_id = ? AND id IN (
				SELECT credential_id FROM upstream_grants WHERE org_id = ? AND server_id = ?)`, org, org, serverID); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM upstream_grants WHERE org_id = ? AND server_id = ?`, org, serverID)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			out.Ended = int(n)
		}
		if e.ReviewAgain {
			res, err := tx.ExecContext(ctx, `UPDATE pins SET status = ?, updated_at = ? WHERE org_id = ? AND server_id = ? AND status = ?`,
				PinPending, ts(time.Now()), org, serverID, PinAccepted)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			out.Reviewed = int(n)
		}
		all := slices.Clone(evs)
		if len(evs) > 0 {
			counted := func(action string) {
				ev := evs[0]
				ev.ID, ev.CallID, ev.Action = ids.New(), ids.New(), action
				all = append(all, ev)
			}
			if e.EndConnections {
				counted(fmt.Sprintf("server.connections_ended:%d", out.Ended))
			}
			if e.ReviewAgain {
				counted(fmt.Sprintf("server.review_again:%d", out.Reviewed))
			}
		}
		for i := range all {
			all[i].Phase = "admin"
		}
		return insertAudit(ctx, tx, org, all)
	})
	return out, err
}
