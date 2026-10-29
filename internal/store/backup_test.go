package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/ids"
)

// seed puts recognisable rows in a store: an org and a few admin audit events.
func seed(t *testing.T, s *Store, n int) OrgID {
	t.Helper()
	ctx := context.Background()
	org, err := s.EnsureOrg(ctx, "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		ev := AuditEvent{ID: ids.New(), CallID: ids.New(), Phase: "admin", Time: time.Now(), Actor: "seed", Action: "seed.event", Decision: "allow", Outcome: "ok"}
		if err := s.InsertAudit(ctx, org.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	return org.ID
}

// readOnly opens a database file without touching it (no journal files appear).
func readOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func siblings(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestBackupIsConsistentSecureAndSingleFile(t *testing.T) {
	ctx := context.Background()
	s, dir := open(t)
	seed(t, s, 5)
	out := filepath.Join(t.TempDir(), "backups")
	dst := filepath.Join(out, "one.db")

	if err := Backup(ctx, dir, dst); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %o, want 600", info.Mode().Perm())
	}
	if d, _ := os.Stat(out); d.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode %o, want 700", d.Mode().Perm())
	}
	if got := siblings(t, out); len(got) != 1 || got[0] != "one.db" {
		t.Errorf("backup directory holds %v, want only one.db (no partial, -wal or -shm)", got)
	}
	db := readOnly(t, dst)
	if n := count(t, db, `SELECT COUNT(*) FROM audit_events WHERE action = 'seed.event'`); n != 5 {
		t.Errorf("backup has %d seed events, want 5", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM leases`); n != 0 {
		t.Errorf("backup carries %d writer-lease rows, want none", n)
	}
	// The live database is untouched and still leased by the running store.
	if n := count(t, s.r, `SELECT COUNT(*) FROM leases`); n != 1 {
		t.Errorf("live database has %d lease rows, want 1", n)
	}
}

func TestBackupWhileWritesAreInFlight(t *testing.T) {
	ctx := context.Background()
	s, dir := open(t)
	org := seed(t, s, 1)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ev := AuditEvent{ID: ids.New(), CallID: ids.New(), Phase: "admin", Time: time.Now(), Actor: "writer", Action: "w.event", Decision: "allow", Outcome: "ok"}
			if err := s.InsertAudit(ctx, org, ev); err != nil {
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	dst := filepath.Join(t.TempDir(), "live.db")
	err := Backup(ctx, dir, dst)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("Backup under load: %v", err)
	}
	db := readOnly(t, dst)
	var res string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil || res != "ok" {
		t.Fatalf("integrity_check = %q, %v", res, err)
	}
	inBackup := count(t, db, `SELECT COUNT(*) FROM audit_events WHERE action = 'w.event'`)
	inLive := count(t, s.r, `SELECT COUNT(*) FROM audit_events WHERE action = 'w.event'`)
	if inBackup == 0 || inBackup > inLive {
		t.Errorf("backup has %d writer events, live has %d: want 0 < backup <= live", inBackup, inLive)
	}
}

func TestBackupNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	s, dir := open(t)
	seed(t, s, 2)
	dst := filepath.Join(t.TempDir(), "keep.db")
	if err := Backup(ctx, dir, dst); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(dst)
	seed(t, s, 3)
	if err := Backup(ctx, dir, dst); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("second Backup to the same path: err = %v, want ErrBackupExists", err)
	}
	after, _ := os.ReadFile(dst)
	if string(before) != string(after) {
		t.Error("the existing backup was modified")
	}
	if got := siblings(t, filepath.Dir(dst)); len(got) != 1 {
		t.Errorf("leftover files after a refused backup: %v", got)
	}
}

func TestBackupNeedsADatabase(t *testing.T) {
	empty := t.TempDir()
	dst := filepath.Join(t.TempDir(), "x.db")
	if err := Backup(context.Background(), empty, dst); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("err = %v, want ErrNoDatabase", err)
	}
	if got := siblings(t, empty); len(got) != 0 {
		t.Errorf("Backup created %v in a directory without a database", got)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("a backup file was created")
	}
}

func backupOf(t *testing.T, s *Store, dir string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "b.db")
	if err := Backup(context.Background(), dir, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, dir := open(t)
	org := seed(t, s, 4)
	b := backupOf(t, s, dir)

	fresh := filepath.Join(t.TempDir(), "data")
	moved, err := Restore(ctx, fresh, b, false)
	if err != nil || len(moved) != 0 {
		t.Fatalf("Restore into an empty directory = %v, %v", moved, err)
	}
	if info, err := os.Stat(filepath.Join(fresh, "privo.db")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored database: %v, %v", info, err)
	}
	r, err := Open(ctx, fresh)
	if err != nil {
		t.Fatalf("Open restored database: %v", err)
	}
	defer func() { _ = r.Close() }()
	if n := count(t, r.r, `SELECT COUNT(*) FROM audit_events WHERE org_id = ? AND action = 'seed.event'`, org); n != 4 {
		t.Errorf("restored database has %d seed events, want 4", n)
	}
	if got, err := r.OrgBySlug(ctx, "default"); err != nil || got.ID != org {
		t.Errorf("OrgBySlug = %+v, %v", got, err)
	}
	if _, err := r.OrgBySlug(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("OrgBySlug(nope) err = %v, want ErrNotFound", err)
	}
}

func TestRestoreReplacesOnlyWithForceAndKeepsTheOldFiles(t *testing.T) {
	ctx := context.Background()
	a, adir := open(t)
	seed(t, a, 2)
	b := backupOf(t, a, adir)

	target := t.TempDir()
	cur, err := Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, cur, 7)
	if err := cur.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, b, false); !errors.Is(err, ErrRestoreExists) {
		t.Fatalf("Restore over a database without force: err = %v, want ErrRestoreExists", err)
	}
	if n := count(t, readOnly(t, filepath.Join(target, "privo.db")), `SELECT COUNT(*) FROM audit_events WHERE action = 'seed.event'`); n != 7 {
		t.Fatalf("a refused restore changed the database (%d events)", n)
	}

	moved, err := Restore(ctx, target, b, true)
	if err != nil {
		t.Fatalf("Restore with force: %v", err)
	}
	if len(moved) == 0 {
		t.Fatal("force replaced a database but reported nothing moved aside")
	}
	for _, m := range moved {
		if !strings.Contains(filepath.Base(m), ".replaced-") {
			t.Errorf("moved-aside name %q does not say what it is", m)
		}
	}
	if n := count(t, readOnly(t, filepath.Join(target, "privo.db")), `SELECT COUNT(*) FROM audit_events WHERE action = 'seed.event'`); n != 2 {
		t.Errorf("restored database has %d events, want 2", n)
	}
	old := ""
	for _, m := range moved {
		if strings.HasPrefix(filepath.Base(m), "privo.db.replaced-") {
			old = m
		}
	}
	if old == "" {
		t.Fatalf("the old privo.db was not kept: %v", moved)
	}
	if n := count(t, readOnly(t, old), `SELECT COUNT(*) FROM audit_events WHERE action = 'seed.event'`); n != 7 {
		t.Errorf("the kept old database has %d events, want 7", n)
	}
}

func TestRestoreRefusesALiveGateway(t *testing.T) {
	s, dir := open(t) // holds the writer lease
	seed(t, s, 1)
	b := backupOf(t, s, dir)
	if _, err := Restore(context.Background(), dir, b, true); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("Restore onto a running gateway: err = %v, want ErrLeaseHeld", err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("the running store was disturbed: %v", err)
	}
}

func TestRestoreRefusesUnusableFilesAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	src, sdir := open(t)
	seed(t, src, 1)
	good := backupOf(t, src, sdir)

	target := t.TempDir()
	cur, err := Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, cur, 3)
	_ = cur.Close()

	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("this is not a database, just some bytes that are long enough to look like a header"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.db")

	// A backup from a newer privo: one more applied migration than this binary knows.
	newer := filepath.Join(t.TempDir(), "newer.db")
	data, _ := os.ReadFile(good)
	if err := os.WriteFile(newer, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(newer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	for name, src := range map[string]string{"garbage": garbage, "missing": missing, "newer schema": newer} {
		_, err := Restore(ctx, target, src, true)
		if err == nil {
			t.Errorf("%s: Restore succeeded", name)
			continue
		}
		if name != "missing" && !errors.Is(err, ErrBackupInvalid) {
			t.Errorf("%s: err = %v, want ErrBackupInvalid", name, err)
		}
		if n := count(t, readOnly(t, filepath.Join(target, "privo.db")), `SELECT COUNT(*) FROM audit_events WHERE action = 'seed.event'`); n != 3 {
			t.Errorf("%s: the current database changed (%d events)", name, n)
		}
		for _, f := range siblings(t, target) {
			if strings.Contains(f, ".replaced-") || strings.HasSuffix(f, ".restoring") {
				t.Errorf("%s: left %s behind", name, f)
			}
		}
	}
}

func TestBackupNameAndPrune(t *testing.T) {
	at := time.Date(2026, 10, 29, 6, 15, 30, 0, time.UTC)
	if got, want := BackupName(at), "privo-20261029T061530Z.db"; got != want {
		t.Fatalf("BackupName = %q, want %q", got, want)
	}

	dir := t.TempDir()
	var names []string
	for i := 0; i < 5; i++ {
		n := BackupName(at.Add(time.Duration(i) * time.Hour))
		names = append(names, n)
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, other := range []string{"notes.txt", "privo-latest.db", "privo-20261029T061530Z.db.partial", "privo.db"} {
		if err := os.WriteFile(filepath.Join(dir, other), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := PruneBackups(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %v, want the 3 oldest", removed)
	}
	left := map[string]bool{}
	for _, n := range siblings(t, dir) {
		left[n] = true
	}
	for _, n := range names[3:] {
		if !left[n] {
			t.Errorf("newest backup %s was removed", n)
		}
	}
	for _, n := range names[:3] {
		if left[n] {
			t.Errorf("old backup %s survived", n)
		}
	}
	for _, other := range []string{"notes.txt", "privo-latest.db", "privo-20261029T061530Z.db.partial", "privo.db"} {
		if !left[other] {
			t.Errorf("unrelated file %s was removed", other)
		}
	}
	if _, err := PruneBackups(dir, 0); err == nil {
		t.Error("PruneBackups(keep=0) must be refused: it would delete every backup")
	}
}
