package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"privo.club/privo/internal/config"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

const backupUsage = "usage: privo backup [--to FILE|DIR] [--keep N]\n  FILE or DIR (default: <data dir>/backups). A directory gets privo-<UTC time>.db; --keep N keeps the newest N there."

const restoreUsage = "usage: privo restore --from FILE [--force]\n  Stop the gateway first. --force replaces an existing database, keeping the old files as *.replaced-<UTC time>."

// backupCmd writes a snapshot of the database. The gateway may be running.
func backupCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	to, keep, keepSet := "", 0, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--to":
			i++
			if i >= len(args) {
				_, _ = fmt.Fprintf(stderr, "privo backup: --to needs a value\n%s\n", backupUsage)
				return 2
			}
			to = args[i]
		case "--keep":
			i++
			n := 0
			if i < len(args) {
				n, _ = strconv.Atoi(args[i])
			}
			if n < 1 {
				_, _ = fmt.Fprintf(stderr, "privo backup: --keep needs a number of at least 1\n%s\n", backupUsage)
				return 2
			}
			keep, keepSet = n, true
		default:
			_, _ = fmt.Fprintf(stderr, "privo backup: unknown argument %q\n%s\n", args[i], backupUsage)
			return 2
		}
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}

	dir, dst := "", ""
	if to == "" {
		dir = filepath.Join(cfg.DataDir, "backups")
	} else if info, statErr := os.Stat(to); (statErr == nil && info.IsDir()) || strings.HasSuffix(to, "/") || strings.HasSuffix(to, string(filepath.Separator)) { //nolint:gosec // operator-supplied path is the point
		dir = to
	} else {
		dst = to
	}
	if dst != "" && keepSet {
		_, _ = fmt.Fprintf(stderr, "privo backup: --keep applies to a directory, not to a file\n%s\n", backupUsage)
		return 2
	}
	if dir != "" {
		dst = filepath.Join(dir, store.BackupName(time.Now()))
	}

	ctx := context.Background()
	if err := store.Backup(ctx, cfg.DataDir, dst); err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: backup: %v\n", err)
		return 1
	}
	size := int64(0)
	if info, err := os.Stat(dst); err == nil { //nolint:gosec // operator-supplied path is the point
		size = info.Size()
	}
	_, _ = fmt.Fprintf(stdout, "backup written: %s (%d bytes)\n", dst, size)
	auditAdmin(ctx, cfg.DataDir, stderr, "backup.create", filepath.Base(dst))

	if keepSet {
		removed, err := store.PruneBackups(dir, keep)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "privo: backup written, but removing old backups failed: %v\n", err)
			return 1
		}
		if len(removed) > 0 {
			noun := "backups"
			if len(removed) == 1 {
				noun = "backup"
			}
			_, _ = fmt.Fprintf(stdout, "removed %d old %s\n", len(removed), noun)
		}
	}
	return 0
}

// restoreCmd puts a backup in place as the database. The gateway must be stopped.
func restoreCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	from, force := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--from":
			i++
			if i >= len(args) {
				_, _ = fmt.Fprintf(stderr, "privo restore: --from needs a value\n%s\n", restoreUsage)
				return 2
			}
			from = args[i]
		case "--force":
			force = true
		default:
			_, _ = fmt.Fprintf(stderr, "privo restore: unknown argument %q\n%s\n", args[i], restoreUsage)
			return 2
		}
	}
	if from == "" {
		_, _ = fmt.Fprintf(stderr, "privo restore: --from is required\n%s\n", restoreUsage)
		return 2
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	ctx := context.Background()
	moved, err := store.Restore(ctx, cfg.DataDir, from, force)
	switch {
	case errors.Is(err, store.ErrRestoreExists):
		_, _ = fmt.Fprintf(stderr, "privo: restore: %s already holds a database; run again with --force to replace it (the old files are kept as *.replaced-<time>)\n", cfg.DataDir)
		return 1
	case errors.Is(err, store.ErrLeaseHeld):
		_, _ = fmt.Fprintf(stderr, "privo: restore: a gateway is running on %s; stop it first\n", cfg.DataDir)
		return 1
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "privo: restore: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "restored %s into %s\n", from, cfg.DataDir)
	for _, m := range moved {
		_, _ = fmt.Fprintf(stdout, "moved aside: %s\n", m)
	}
	auditAdmin(ctx, cfg.DataDir, stderr, "restore.apply", filepath.Base(from))
	return 0
}

// auditAdmin records an operator action done from the command line in the
// default org's admin audit. A failure is a warning: the backup or restore
// itself has already succeeded.
func auditAdmin(ctx context.Context, dataDir string, stderr io.Writer, action, item string) {
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := store.OpenShared(ctx, dataDir)
	if err != nil {
		logger.Warn("could not write the audit event", "action", action, "err", err)
		return
	}
	defer func() { _ = st.Close() }() //nolint:contextcheck // Close releases the lease with its own bounded context
	org, err := st.OrgBySlug(ctx, DefaultOrgSlug)
	if err != nil {
		logger.Warn("could not write the audit event", "action", action, "err", err)
		return
	}
	recordAdmin(ctx, st, org.ID, "cli", action, item, "ok", logger)
}

// recordAdmin writes one admin audit event; a failure is logged, not returned.
func recordAdmin(ctx context.Context, st *store.Store, org store.OrgID, actor, action, item, outcome string, logger *slog.Logger) {
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Phase: "admin", Time: time.Now(), Actor: actor, Item: item, Action: action, Decision: "allow", Outcome: outcome}
	if err := st.InsertAudit(ctx, org, ev); err != nil {
		logger.Warn("could not write the audit event", "action", action, "err", err)
	}
}
