//go:build unix

package store

import "syscall"

// restrictUmask makes every file the process creates from now on owner-only.
// SQLite deletes and recreates its -wal and -shm files, so pre-creating them
// with 0600 is not enough. The umask is process-wide, which is what a server
// that holds credentials wants.
func restrictUmask() { syscall.Umask(0o077) }
