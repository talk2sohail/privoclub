package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
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

// CreateServer inserts a server and its credentials atomically.
func (s *Store) CreateServer(ctx context.Context, srv Server, creds []Credential) error {
	cfg, err := json.Marshal(srv.Config)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
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
		return nil
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
			`DELETE FROM pins WHERE org_id = ? AND server_id = ?`,
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
