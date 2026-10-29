package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Scheduled backups are off unless the operator sets an interval; the keep
// count and the directory have safe defaults.
func TestBackupConfig(t *testing.T) {
	cfg, err := Load(env(map[string]string{"PRIVO_DATA_DIR": "/data"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackupInterval != 0 || cfg.BackupKeep != 7 || cfg.BackupDir != filepath.Join("/data", "backups") {
		t.Errorf("defaults: interval %s, keep %d, dir %q; want off, 7, /data/backups", cfg.BackupInterval, cfg.BackupKeep, cfg.BackupDir)
	}

	cfg, err = Load(env(map[string]string{
		"PRIVO_DATA_DIR":        "/data",
		"PRIVO_BACKUP_INTERVAL": "24h",
		"PRIVO_BACKUP_KEEP":     "14",
		"PRIVO_BACKUP_DIR":      "/data/snapshots",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackupInterval != 24*time.Hour || cfg.BackupKeep != 14 || cfg.BackupDir != "/data/snapshots" {
		t.Errorf("values: interval %s, keep %d, dir %q", cfg.BackupInterval, cfg.BackupKeep, cfg.BackupDir)
	}

	for _, tc := range []struct{ name, value string }{
		{"PRIVO_BACKUP_INTERVAL", "soon"},
		{"PRIVO_BACKUP_INTERVAL", "-1h"},
		{"PRIVO_BACKUP_INTERVAL", "500ms"}, // a backup every half second is a mistake
		{"PRIVO_BACKUP_KEEP", "0"},         // would delete every backup
		{"PRIVO_BACKUP_KEEP", "-3"},
		{"PRIVO_BACKUP_KEEP", "many"},
		{"PRIVO_BACKUP_KEEP", "100000"},
	} {
		_, err := Load(env(map[string]string{tc.name: tc.value}))
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: error %v, want one that names the variable", tc.name, tc.value, err)
		}
	}
}
