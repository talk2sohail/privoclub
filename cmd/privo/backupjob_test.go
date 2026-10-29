package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"privo.club/privo/internal/store"
)

type jobFixture struct {
	job *backupJob
	dir string
	st  *store.Store
}

// newJob builds a job on a real database. Its clock moves one second per call,
// so file names (which have a resolution of one second) never collide.
func newJob(t *testing.T, interval time.Duration, keep int) jobFixture {
	t.Helper()
	data := t.TempDir()
	st, err := store.Open(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(context.Background(), DefaultOrgSlug, "Default")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	dir := filepath.Join(data, "backups")
	return jobFixture{
		job: &backupJob{st: st, org: org.ID, dataDir: data, dir: dir, interval: interval, keep: keep,
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			now:    func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Second) }},
		dir: dir, st: st,
	}
}

func backupFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "privo-*.db"))
	return m
}

func startJob(t *testing.T, j *backupJob) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the backup job did not stop when its context was cancelled")
		}
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackupJobRunsAtStartWhenNoneExistsKeepsTheNewestAndAudits(t *testing.T) {
	f := newJob(t, 40*time.Millisecond, 2)
	stop := startJob(t, f.job)
	defer stop()

	eventually(t, "at least four audited backups", func() bool {
		return countActions(t, f.st, f.job.org, "backup.create") >= 4
	})
	stop()
	if got := backupFiles(t, f.dir); len(got) != 2 {
		t.Errorf("--keep 2 left %d snapshots: %v", len(got), got)
	}
	if left, _ := filepath.Glob(filepath.Join(f.dir, "*.partial")); len(left) != 0 {
		t.Errorf("partial files left behind: %v", left)
	}
	evs, _ := f.st.RecentAdminEvents(context.Background(), f.job.org, 50)
	for _, e := range evs {
		if e.Action == "backup.create" && (e.Actor != "system" || e.Outcome != "ok") {
			t.Errorf("scheduled backup audit row = %+v, want actor system and outcome ok", e)
		}
	}
}

func TestBackupJobWaitsWhenARecentBackupExists(t *testing.T) {
	f := newJob(t, time.Hour, 3)
	// A backup made "now" by the job's own clock: the next one is an hour away.
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	recent := filepath.Join(f.dir, store.BackupName(f.job.now()))
	if err := os.WriteFile(recent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := startJob(t, f.job)
	time.Sleep(300 * time.Millisecond)
	stop()
	if got := backupFiles(t, f.dir); len(got) != 1 {
		t.Errorf("a backup was taken although one is recent: %v", got)
	}
}

func TestBackupJobFailureIsAuditedAndDoesNotStopTheJob(t *testing.T) {
	f := newJob(t, 30*time.Millisecond, 2)
	// The directory cannot be created: its parent is a file.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.job.dir = filepath.Join(blocker, "sub")
	stop := startJob(t, f.job)
	defer stop()

	eventually(t, "two audited failures (the job keeps trying)", func() bool {
		n := 0
		evs, _ := f.st.RecentAdminEvents(context.Background(), f.job.org, 50)
		for _, e := range evs {
			if e.Action == "backup.create" && e.Outcome == "error" && e.Actor == "system" {
				n++
			}
		}
		return n >= 2
	})
}

func countActions(t *testing.T, st *store.Store, org store.OrgID, action string) int {
	t.Helper()
	evs, err := st.RecentAdminEvents(context.Background(), org, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Action == action {
			n++
		}
	}
	return n
}
