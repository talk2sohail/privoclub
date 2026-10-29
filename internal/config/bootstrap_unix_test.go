//go:build unix

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"privo.club/privo/internal/token"
)

// R-11 (independent review, 2026-10-29): opening a FIFO that nobody writes to
// blocks for good, and startup waits for the token file. A file that is not a
// regular file is refused before it is opened.
func TestBootstrapTokenFileMustBeRegular(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": fifo}))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBootstrapUnreadable) || !strings.Contains(err.Error(), "not a regular file") || !strings.Contains(err.Error(), "PRIVO_BOOTSTRAP_TOKEN_FILE") {
			t.Fatalf("err = %v, want an unreadable-file error that says it is not a regular file", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("loading the token blocked on a FIFO")
	}

	// A directory is refused with the same words; a symlink to a regular file
	// is how secrets are mounted on some platforms, and it works.
	if _, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": dir})); !errors.Is(err, ErrBootstrapUnreadable) {
		t.Errorf("a directory: err = %v", err)
	}
	tok, _ := token.New()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte(tok.Plain+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": link}))
	if err != nil || got.Reveal() != tok.Plain {
		t.Fatalf("a symlink to a regular file: %v", err)
	}
}
