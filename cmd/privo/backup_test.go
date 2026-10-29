package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/store"
)

func envOf(dir string) func(string) string {
	return func(k string) string {
		if k == "PRIVO_DATA_DIR" {
			return dir
		}
		return ""
	}
}

// seededDir makes a data directory with a database that has an org and one
// admin audit event, and closes it, as if the gateway had stopped.
func seededDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	org, err := st.EnsureOrg(context.Background(), DefaultOrgSlug, "Default")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAudit(context.Background(), org.ID, store.AuditEvent{ID: "e1", CallID: "c1", Phase: "admin", Time: time.Now(), Actor: "seed", Action: "seed.event", Decision: "allow", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func adminActions(t *testing.T, dir string) []string {
	t.Helper()
	st, err := store.OpenShared(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	org, err := st.OrgBySlug(context.Background(), DefaultOrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.RecentAdminEvents(context.Background(), org.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Action)
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestBackupDefaultsToABackupsDirectoryInTheDataDir(t *testing.T) {
	dir := seededDir(t)
	var out, errb bytes.Buffer
	if code := run([]string{"backup"}, &out, &errb, envOf(dir)); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "backups", "privo-*.db"))
	if len(matches) != 1 {
		t.Fatalf("backups = %v (stdout %q)", matches, out.String())
	}
	if !strings.Contains(out.String(), matches[0]) {
		t.Errorf("stdout %q does not name the file %s", out.String(), matches[0])
	}
	if info, _ := os.Stat(matches[0]); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", info.Mode().Perm())
	}
	if !has(adminActions(t, dir), "backup.create") {
		t.Errorf("no backup.create admin event in the live database")
	}
}

func TestBackupToAFileAndKeepRules(t *testing.T) {
	dir := seededDir(t)
	var out, errb bytes.Buffer
	file := filepath.Join(t.TempDir(), "mine.db")
	if code := run([]string{"backup", "--to", file}, &out, &errb, envOf(dir)); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	// Asking again for the same file is an error and leaves it alone.
	errb.Reset()
	if code := run([]string{"backup", "--to", file}, &out, &errb, envOf(dir)); code != 1 || !strings.Contains(errb.String(), "already exists") {
		t.Fatalf("second backup to the same file: exit %d, stderr %q", code, errb.String())
	}
	// --keep makes sense for a directory only, and must be at least 1.
	for _, args := range [][]string{
		{"backup", "--to", file, "--keep", "2"},
		{"backup", "--keep", "0"},
		{"backup", "--keep", "x"},
		{"backup", "--bogus"},
	} {
		errb.Reset()
		if code := run(args, &out, &errb, envOf(dir)); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, errb.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); err == nil {
		t.Error("a refused command created the default backups directory")
	}
}

func TestBackupKeepPrunesOldSnapshots(t *testing.T) {
	dir := seededDir(t)
	bdir := filepath.Join(t.TempDir(), "snaps")
	if err := os.MkdirAll(bdir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := []string{"privo-20200101T000000Z.db", "privo-20200102T000000Z.db", "privo-20200103T000000Z.db"}
	for _, n := range old {
		if err := os.WriteFile(filepath.Join(bdir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(bdir, "notes.txt")
	_ = os.WriteFile(other, []byte("mine"), 0o600)

	var out, errb bytes.Buffer
	if code := run([]string{"backup", "--to", bdir, "--keep", "2"}, &out, &errb, envOf(dir)); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	all, _ := filepath.Glob(filepath.Join(bdir, "privo-*.db"))
	if len(all) != 2 {
		t.Fatalf("after --keep 2 the directory holds %v", all)
	}
	if _, err := os.Stat(filepath.Join(bdir, old[2])); err != nil {
		t.Errorf("the newest old snapshot was removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("an unrelated file was removed")
	}
	if !strings.Contains(out.String(), "removed 2") {
		t.Errorf("stdout %q does not say two old backups were removed", out.String())
	}
}

func TestBackupNeedsADatabase(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"backup"}, &out, &errb, envOf(dir)); code != 1 || !strings.Contains(errb.String(), "no database") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("backup created %v in an empty data directory", entries)
	}
}

func TestRestoreCommand(t *testing.T) {
	src := seededDir(t)
	var out, errb bytes.Buffer
	file := filepath.Join(t.TempDir(), "b.db")
	if code := run([]string{"backup", "--to", file}, &out, &errb, envOf(src)); code != 0 {
		t.Fatalf("backup: exit %d: %s", code, errb.String())
	}

	target := t.TempDir()
	out.Reset()
	errb.Reset()
	if code := run([]string{"restore", "--from", file}, &out, &errb, envOf(target)); code != 0 {
		t.Fatalf("restore into an empty directory: exit %d: %s", code, errb.String())
	}
	acts := adminActions(t, target)
	if !has(acts, "seed.event") || !has(acts, "restore.apply") {
		t.Errorf("restored database has admin events %v, want seed.event and restore.apply", acts)
	}

	// Over an existing database: refused without --force, then replaced with it.
	out.Reset()
	errb.Reset()
	if code := run([]string{"restore", "--from", file}, &out, &errb, envOf(target)); code != 1 || !strings.Contains(errb.String(), "--force") {
		t.Fatalf("restore over a database: exit %d, stderr %q (want 1 and a hint about --force)", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"restore", "--from", file, "--force"}, &out, &errb, envOf(target)); code != 0 {
		t.Fatalf("restore --force: exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), ".replaced-") {
		t.Errorf("stdout %q does not say where the old database went", out.String())
	}
}

func TestRestoreUsage(t *testing.T) {
	var out, errb bytes.Buffer
	dir := t.TempDir()
	for _, args := range [][]string{{"restore"}, {"restore", "--from"}, {"restore", "--bogus"}} {
		errb.Reset()
		if code := run(args, &out, &errb, envOf(dir)); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	errb.Reset()
	if code := run([]string{"restore", "--from", filepath.Join(dir, "nope.db")}, &out, &errb, envOf(dir)); code != 1 {
		t.Errorf("missing file: exit %d, want 1", code)
	}
}
