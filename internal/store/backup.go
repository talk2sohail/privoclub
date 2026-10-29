package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Snapshot backups (ADR 0010). A backup is a single SQLite file made with
// VACUUM INTO, which reads a consistent view while the gateway keeps writing
// (WAL), so it needs no downtime. It holds what the database holds: token
// hashes, policy, audit and the wrapped org keys. It does not hold the master
// key, so it is useless without it (ADR 0013) but still as sensitive as the
// database itself: 0600 in a 0700 directory.

const dbFile = "privo.db"

var (
	// ErrBackupExists means the backup target is already there. A backup never overwrites.
	ErrBackupExists = errors.New("store: backup target already exists")
	// ErrNoDatabase means the data directory has no database to back up.
	ErrNoDatabase = errors.New("store: no database in the data directory")
	// ErrRestoreExists means a restore would replace a database and force was not given.
	ErrRestoreExists = errors.New("store: a database already exists in the data directory (force replaces it)")
	// ErrBackupInvalid means a file is not a usable Privo backup.
	ErrBackupInvalid = errors.New("store: not a usable privo backup")
)

// BackupName is the file name `privo backup` gives a snapshot made at t.
func BackupName(t time.Time) string {
	return "privo-" + t.UTC().Format("20060102T150405Z") + ".db"
}

var backupNameRE = regexp.MustCompile(`^privo-\d{8}T\d{6}Z\.db$`)

// PruneBackups deletes all but the newest keep snapshots in dir, looking only at
// files named like BackupName makes them. It returns the paths it removed.
func PruneBackups(dir string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, errors.New("store: keep must be at least 1 (0 would delete every backup)")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && backupNameRE.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // the timestamp sorts as text
	if len(names) <= keep {
		return nil, nil
	}
	var removed []string
	for _, n := range names[:len(names)-keep] {
		p := filepath.Join(dir, n)
		if err := os.Remove(p); err != nil {
			return removed, err
		}
		removed = append(removed, p)
	}
	return removed, nil
}

// Backup writes a verified, consistent copy of the database in dataDir to dst.
// The gateway may be running. The copy is made as dst+".partial", checked, and
// only then given its final name, so dst is either absent or complete. An
// existing dst is never overwritten.
func Backup(ctx context.Context, dataDir, dst string) (err error) {
	restrictUmask()
	if fi, statErr := os.Stat(filepath.Join(dataDir, dbFile)); statErr != nil || fi.Size() == 0 {
		return fmt.Errorf("%w: %s", ErrNoDatabase, dataDir)
	}
	if _, statErr := os.Lstat(dst); statErr == nil {
		return fmt.Errorf("%w: %s", ErrBackupExists, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("store: creating %s: %w", filepath.Dir(dst), err)
	}
	tmp := dst + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path chosen by the operator
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s (another backup running, or a leftover to remove)", ErrBackupExists, tmp)
		}
		return err
	}
	_ = f.Close()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	st, err := OpenShared(ctx, dataDir)
	if err != nil {
		return err
	}
	_, err = st.r.ExecContext(ctx, `VACUUM INTO ?`, tmp)
	_ = st.Close() //nolint:contextcheck // Close releases the lease with its own bounded context
	if err != nil {
		return fmt.Errorf("store: snapshot: %w", err)
	}
	if err := finishCopy(ctx, tmp); err != nil {
		return err
	}
	if err := verifyBackup(ctx, tmp); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	// Link, not rename: linking fails if dst appeared meanwhile, rename would replace it.
	if err := os.Link(tmp, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrBackupExists, dst)
		}
		return err
	}
	_ = os.Remove(tmp)
	syncDir(filepath.Dir(dst))
	return nil
}

// finishCopy makes the snapshot a lease-free, single file in WAL mode like the
// live database: the copy carries the live writer's lease row, which would
// block the first start on it, and tools that open a database without the
// lease (OpenShared) need it to be in WAL mode already. Closing the last
// connection removes the -wal and -shm files again.
func finishCopy(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", fileURI(path, ""))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `DELETE FROM leases`); err != nil && !strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("store: clearing the lease in the copy: %w", err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		return fmt.Errorf("store: journal mode of the copy: %w", err)
	}
	return nil
}

// verifyBackup checks that path is an intact Privo database whose schema this
// binary knows. It opens the file read-only and immutable, so nothing is written
// next to it.
func verifyBackup(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", fileURI(path, "mode=ro&immutable=1"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrBackupInvalid, path, err)
	}
	if res != "ok" {
		return fmt.Errorf("%w: %s: integrity check says %q", ErrBackupInvalid, path, res)
	}
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied`).Scan(&v); err != nil || !v.Valid {
		return fmt.Errorf("%w: %s: no schema version, so not a privo database", ErrBackupInvalid, path)
	}
	if latest := latestMigration(); v.Int64 > latest {
		return fmt.Errorf("%w: %s: schema version %d is newer than this privo knows (%d)", ErrBackupInvalid, path, v.Int64, latest)
	}
	return nil
}

func latestMigration() int64 {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return 0
	}
	var latest int64
	for _, e := range entries {
		digits, _, _ := strings.Cut(e.Name(), "_")
		if n, err := strconv.ParseInt(digits, 10, 64); err == nil && n > latest {
			latest = n
		}
	}
	return latest
}

// Restore puts the backup src in place as the database of dataDir and returns
// the files it moved aside. It refuses a backup that does not verify, refuses
// while a gateway holds the writer lease, and replaces an existing database only
// with force, moving the old privo.db, -wal and -shm to *.replaced-<UTC time>
// (nothing is deleted). Nothing changes unless the whole restore can proceed.
func Restore(ctx context.Context, dataDir, src string, force bool) (moved []string, err error) {
	restrictUmask()
	if _, err := os.Stat(src); err != nil {
		return nil, err
	}
	if err := verifyBackup(ctx, src); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("store: creating %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, dbFile)
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > 0 {
		held, err := writerLeaseHeld(ctx, path)
		if err != nil {
			return nil, err
		}
		if held {
			return nil, ErrLeaseHeld
		}
		if !force {
			return nil, ErrRestoreExists
		}
	}

	// Stage the new file first: a failure here leaves the current database as it is.
	staged := path + ".restoring"
	if err := copyFile(src, staged); err != nil {
		_ = os.Remove(staged)
		return nil, err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		fi, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		if fi.Size() == 0 { // pre-created and empty: nothing to keep
			_ = os.Remove(p)
			continue
		}
		to := p + ".replaced-" + stamp
		if err := os.Rename(p, to); err != nil {
			rollback(moved)
			_ = os.Remove(staged)
			return nil, err
		}
		moved = append(moved, to)
	}
	if err := os.Rename(staged, path); err != nil {
		rollback(moved)
		_ = os.Remove(staged)
		return nil, err
	}
	syncDir(dataDir)
	return moved, nil
}

// rollback puts files moved aside back where they were.
func rollback(moved []string) {
	for _, to := range moved {
		if i := strings.LastIndex(to, ".replaced-"); i > 0 {
			_ = os.Rename(to, to[:i])
		}
	}
}

func writerLeaseHeld(ctx context.Context, path string) (bool, error) {
	db, err := sql.Open("sqlite", fileURI(path, "mode=ro"))
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases WHERE name = 'writer' AND expires_at > ?`, ts(time.Now().UTC())).Scan(&n)
	if err != nil {
		// No lease table or a file that is not a database: nobody is running on it.
		msg := err.Error()
		if strings.Contains(msg, "no such table") || strings.Contains(msg, "not a database") || strings.Contains(msg, "malformed") {
			return false, nil
		}
		return false, fmt.Errorf("store: checking the writer lease: %w", err)
	}
	return n > 0, nil
}

func fileURI(path, query string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query}
	return u.String()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // path chosen by the operator
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path under the data dir
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // path chosen by the operator
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// syncDir makes a rename or link durable. A failure is not fatal: some file
// systems do not support syncing a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil { //nolint:gosec // directory chosen by the operator
		_ = d.Sync()
		_ = d.Close()
	}
}
