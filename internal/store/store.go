// Package store persists Privo's state (ADR 0002). This file holds the SQLite
// engine used by single-node installs; a Postgres engine implements the same
// methods for HA and Privo Cloud.
//
// The driver is modernc.org/sqlite (pure Go; no C toolchain, static binary).
//
// SQLite discipline: WAL, synchronous=NORMAL, busy_timeout, one writer
// connection with BEGIN IMMEDIATE, a separate read pool, and a lease so two
// processes never write the same database.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers "sqlite"

	"privo.club/privo/internal/ids"
)

//go:embed migrations/*.sql
var migrations embed.FS

// OrgID identifies an organisation. Every query that touches org data takes one.
type OrgID string

// ErrNotFound is returned when a row does not exist in the caller's org.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when a unique constraint would be violated.
var ErrConflict = errors.New("store: already exists")

// Store is the SQLite-backed store.
type Store struct {
	w      *sql.DB // single writer
	r      *sql.DB // readers
	holder string
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	err    error
}

// LeaseTTL is how long a writer lease lasts without a heartbeat.
const LeaseTTL = 15 * time.Second

// Open opens (creating if needed) the database in dir, migrates it and takes
// the writer lease. It refuses to start if another live process holds it.
func Open(ctx context.Context, dir string) (*Store, error) {
	return openDB(ctx, dir, true)
}

// OpenShared opens an existing, already migrated database without the writer
// lease, for short administrative commands (such as `privo token create`)
// while a server is running. SQLite serialises the writes.
func OpenShared(ctx context.Context, dir string) (*Store, error) {
	return openDB(ctx, dir, false)
}

func openDB(ctx context.Context, dir string, lease bool) (*Store, error) {
	restrictUmask()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: creating %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs the execute bit; 0700 is owner-only
		return nil, fmt.Errorf("store: securing %s: %w", dir, err)
	}
	path := filepath.Join(dir, "privo.db")
	// Pre-create the database and its WAL and shared-memory files as 0600, so
	// they never exist with looser permissions.
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		fh, err := os.OpenFile(f, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path under the data dir
		if err != nil {
			return nil, fmt.Errorf("store: creating %s: %w", f, err)
		}
		_ = fh.Close()
		if err := os.Chmod(f, 0o600); err != nil {
			return nil, err
		}
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	base := "file:" + filepath.ToSlash(path) + "?"

	w, err := sql.Open("sqlite", base+q.Encode()+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", base+q.Encode()+"&mode=ro")
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)

	s := &Store{w: w, r: r, holder: ids.New(), stop: make(chan struct{}), done: make(chan struct{})}
	if !lease {
		close(s.done)
		if err := s.Ping(ctx); err != nil {
			_ = s.closeDBs()
			return nil, err
		}
		return s, nil
	}
	// The lease comes first: a second process must stop before it migrates.
	if err := s.acquireLease(ctx); err != nil {
		_ = s.closeDBs()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		s.releaseLease(ctx)
		_ = s.closeDBs()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = s.closeDBs()
		return nil, err
	}
	go s.heartbeat(context.WithoutCancel(ctx))
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.w, mustSub(migrations, "migrations"))
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("store: migrating: %w", err)
	}
	return nil
}

// Ping checks the database for readiness probes.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.r.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// Close stops the heartbeat, releases the lease and closes the database.
// It is safe to call more than once.
func (s *Store) Close() error {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		if s.holder != "" {
			s.releaseLease(context.Background())
		}
		s.err = s.closeDBs()
	})
	return s.err
}

func (s *Store) releaseLease(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	_, _ = s.w.ExecContext(ctx, `DELETE FROM leases WHERE name = 'writer' AND holder = ?`, s.holder)
}

func (s *Store) closeDBs() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

// ErrLeaseHeld means another process owns this database.
var ErrLeaseHeld = errors.New("store: database is in use by another privo process")

func (s *Store) acquireLease(ctx context.Context) error {
	// The lease table predates migrations so the lease can guard them.
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS leases (
		name TEXT PRIMARY KEY, holder TEXT NOT NULL, expires_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("store: lease table: %w", err)
	}
	now := time.Now().UTC()
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO leases (name, holder, expires_at) VALUES ('writer', ?, ?)
		ON CONFLICT (name) DO UPDATE SET holder = excluded.holder, expires_at = excluded.expires_at
		WHERE leases.expires_at < ?`,
		s.holder, ts(now.Add(LeaseTTL)), ts(now))
	if err != nil {
		return fmt.Errorf("store: lease: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseHeld
	}
	return nil
}

// heartbeat renews the lease until Close. base carries values but not cancellation.
func (s *Store) heartbeat(base context.Context) {
	defer close(s.done)
	t := time.NewTicker(LeaseTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(base, LeaseTTL/3)
			_, _ = s.w.ExecContext(ctx, `UPDATE leases SET expires_at = ? WHERE name = 'writer' AND holder = ?`,
				ts(time.Now().UTC().Add(LeaseTTL)), s.holder)
			cancel()
		}
	}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// tx runs fn in a write transaction.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
