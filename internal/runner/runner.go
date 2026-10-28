// Package runner starts stdio MCP servers (ADR 0007).
//
// The production runner is a separate, isolated container reached over a
// versioned protocol (feature F13). Local is for development and tests only:
// it runs the process on the gateway host, after checking the executable's
// SHA-256 digest, with a minimal environment that never inherits the
// gateway's own variables.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Spec describes a process to start.
type Spec struct {
	Path   string // absolute path to the executable
	SHA256 string // expected hex digest of the executable
	Args   []string
	Env    map[string]string // the server's own variables (decrypted secrets included)
}

// Runner starts stdio servers.
type Runner interface {
	Command(ctx context.Context, spec Spec) (*exec.Cmd, error)
}

// ErrReservedEnv means a server tried to set a variable the runner controls
// or that changes how executables load.
var ErrReservedEnv = errors.New("runner: reserved environment variable")

// ReservedEnv reports whether name may not be set by a server's configuration.
func ReservedEnv(name string) bool {
	upper := strings.ToUpper(name)
	switch upper {
	case "PATH", "HOME", "LANG", "GCONV_PATH", "NODE_OPTIONS", "PYTHONPATH", "PYTHONSTARTUP", "PERL5OPT", "RUBYOPT", "BASH_ENV", "ENV", "IFS":
		return true
	}
	return strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") || strings.HasPrefix(upper, "PRIVO_")
}

// ErrDigestMismatch means the executable changed since it was registered.
var ErrDigestMismatch = errors.New("runner: executable digest does not match its pin")

// Local runs processes on this host. Development and tests only.
type Local struct{}

// Command verifies the executable and returns a command ready to start.
func (Local) Command(ctx context.Context, spec Spec) (*exec.Cmd, error) {
	if !filepath.IsAbs(spec.Path) {
		return nil, fmt.Errorf("runner: path %q must be absolute", spec.Path)
	}
	for k := range spec.Env {
		if ReservedEnv(k) {
			return nil, fmt.Errorf("%w: %s", ErrReservedEnv, k)
		}
	}
	digest, err := FileDigest(spec.Path)
	if err != nil {
		return nil, err
	}
	if digest != spec.SHA256 {
		return nil, ErrDigestMismatch
	}
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...) //nolint:gosec // path is pinned by digest
	cmd.Env = minimalEnv(spec.Env)
	cmd.Dir = os.TempDir()
	cmd.Stderr = io.Discard
	return cmd, nil
}

// FileDigest returns the hex SHA-256 of a file.
func FileDigest(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // caller-validated path
	if err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func minimalEnv(own map[string]string) []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + os.TempDir(), "LANG=C.UTF-8"}
	keys := make([]string, 0, len(own))
	for k := range own {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+own[k])
	}
	return env
}
