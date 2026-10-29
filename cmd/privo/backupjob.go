package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"privo.club/privo/internal/store"
)

// backupRetryMax bounds how long a failed scheduled backup waits before it is tried again.
const backupRetryMax = 5 * time.Minute

// backupJob writes a snapshot of the database on a schedule and keeps the
// newest few (PRIVO_BACKUP_INTERVAL, PRIVO_BACKUP_KEEP). It exists because the
// image has no shell, so Coolify's scheduled tasks, which run a command in a
// shell inside the container, cannot call `privo backup`.
type backupJob struct {
	st       *store.Store
	org      store.OrgID
	dataDir  string
	dir      string
	interval time.Duration
	keep     int
	logger   *slog.Logger
	now      func() time.Time
}

// run takes a backup when the newest one is older than the interval (at once
// if there is none), then one per interval, until ctx is done. A failure is
// logged and audited, and tried again soon; it never stops the gateway.
func (j *backupJob) run(ctx context.Context) {
	wait := j.untilNext()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := j.once(ctx); err != nil && ctx.Err() == nil {
			j.logger.Error("scheduled backup failed", "err", err)
			wait = min(j.interval, backupRetryMax)
			continue
		}
		wait = j.interval
	}
}

var snapshotTimeRE = regexp.MustCompile(`^privo-(\d{8}T\d{6}Z)\.db$`)

// untilNext is how long to wait before the first backup: the rest of the
// interval after the newest snapshot, so a restart does not skip or double one.
func (j *backupJob) untilNext() time.Duration {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return 0
	}
	var names []string
	for _, e := range entries {
		if snapshotTimeRE.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return 0
	}
	sort.Strings(names)
	m := snapshotTimeRE.FindStringSubmatch(names[len(names)-1])
	newest, err := time.Parse("20060102T150405Z", m[1])
	if err != nil {
		return 0
	}
	if d := newest.Add(j.interval).Sub(j.now()); d > 0 {
		return d
	}
	return 0
}

// once takes one backup, prunes old ones and audits the result.
func (j *backupJob) once(ctx context.Context) error {
	dst := filepath.Join(j.dir, store.BackupName(j.now()))
	err := store.Backup(ctx, j.dataDir, dst)
	if errors.Is(err, store.ErrBackupExists) {
		return nil // the same second as the last one; the next round will do
	}
	if err == nil {
		var removed []string
		if removed, err = store.PruneBackups(j.dir, j.keep); err != nil {
			j.logger.Error("removing old backups", "err", err)
			err = nil // the snapshot itself was made
		} else if len(removed) > 0 {
			j.logger.Info("removed old backups", "count", len(removed))
		}
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	} else {
		j.logger.Info("backup written", "file", filepath.Base(dst))
	}
	recordAdmin(ctx, j.st, j.org, "system", "backup.create", filepath.Base(dst), outcome, j.logger)
	return err
}
