// Package config loads and validates process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"privo.club/privo/internal/token"
)

// Secret is a configuration value that must never reach a log line, an error
// message or an API response. Every formatting path prints a placeholder; the
// value is available only through Reveal.
type Secret string

// Reveal returns the secret value.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (Secret) String() string { return "[redacted]" }

// GoString implements fmt.GoStringer, for %#v.
func (Secret) GoString() string { return "[redacted]" }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// MarshalText implements encoding.TextMarshaler, so JSON output is redacted too.
func (Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }

// Config is the validated process configuration.
type Config struct {
	// ListenAddr is the address the HTTP server binds to (PRIVO_LISTEN).
	ListenAddr string
	// DataDir holds the database and local state (PRIVO_DATA_DIR).
	DataDir string
	// PublicURL is the externally visible base URL (PRIVO_PUBLIC_URL). Optional
	// for local use; must be https unless the host is loopback.
	PublicURL *url.URL
	// LogLevel is the minimum log level (PRIVO_LOG_LEVEL: debug, info, warn, error).
	LogLevel slog.Level
	// LogFormat is json or text (PRIVO_LOG_FORMAT).
	LogFormat string
	// ShutdownTimeout bounds the graceful drain on SIGTERM (PRIVO_SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// Runner selects how stdio servers run (PRIVO_RUNNER): "" refuses them,
	// "local" runs them on this host (development and tests only, ADR 0007).
	Runner string
	// RetryBase and RetryMax bound upstream reconnect backoff
	// (PRIVO_UPSTREAM_RETRY_BASE, PRIVO_UPSTREAM_RETRY_MAX).
	RetryBase, RetryMax time.Duration
	// EgressAllow lists non-public CIDRs operators allow (PRIVO_EGRESS_ALLOW_CIDRS).
	EgressAllow string
	// TrustedProxies are the reverse proxies in front of Privo
	// (PRIVO_TRUSTED_PROXIES, CIDRs), so per-address limits count the client
	// named in X-Forwarded-For and not the proxy.
	TrustedProxies []netip.Prefix
	// TokenTTL is the default personal token lifetime (PRIVO_TOKEN_TTL).
	TokenTTL time.Duration
	// ApprovalHold is how long a call waits for a person's decision before the
	// caller is told to retry (PRIVO_APPROVAL_HOLD).
	ApprovalHold time.Duration
	// CallTimeout is how long one call to an upstream server may take
	// (PRIVO_UPSTREAM_CALL_TIMEOUT).
	CallTimeout time.Duration
	// RefreshGap is the least time between two refreshes of one server's lists,
	// however often it announces a change (PRIVO_UPSTREAM_REFRESH_GAP).
	RefreshGap time.Duration
	// MaxResponseBytes limits one JSON reply of an HTTP or SSE upstream
	// (PRIVO_UPSTREAM_MAX_RESPONSE_BYTES).
	MaxResponseBytes int64
	// BackupInterval is how often the gateway writes a snapshot of its database
	// (PRIVO_BACKUP_INTERVAL); zero, the default, means it does not.
	BackupInterval time.Duration
	// BackupKeep is how many snapshots are kept (PRIVO_BACKUP_KEEP).
	BackupKeep int
	// BackupDir is where snapshots go (PRIVO_BACKUP_DIR, default <data dir>/backups).
	BackupDir string
}

// Defaults applied when a variable is unset or empty.
const (
	DefaultListenAddr      = ":8080"
	DefaultDataDir         = "./data"
	DefaultLogFormat       = "json"
	DefaultShutdownTimeout = 30 * time.Second

	// PRIVO_UPSTREAM_MAX_RESPONSE_BYTES: the default is what the MCP SDK allows
	// for one frame on stdio and SSE; below the minimum nothing would fit, and
	// above the maximum one reply could take the memory of the whole gateway.
	DefaultMaxResponseBytes = 16 << 20
	MinMaxResponseBytes     = 1024
	MaxMaxResponseBytes     = 1 << 30

	// Scheduled backups: off unless an interval is set; a shorter interval than
	// this is a mistake, and the keep count is bounded so a typo cannot keep or
	// delete everything.
	DefaultBackupKeep = 7
	MinBackupInterval = time.Second
	MaxBackupKeep     = 1000
)

// Load reads configuration using getenv (usually os.Getenv). All problems are
// reported together so an operator can fix them in one pass.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		ListenAddr:      orDefault(getenv("PRIVO_LISTEN"), DefaultListenAddr),
		DataDir:         orDefault(getenv("PRIVO_DATA_DIR"), DefaultDataDir),
		LogFormat:       strings.ToLower(orDefault(getenv("PRIVO_LOG_FORMAT"), DefaultLogFormat)),
		ShutdownTimeout: DefaultShutdownTimeout,
	}
	var errs []error

	if _, _, err := net.SplitHostPort(cfg.ListenAddr); err != nil {
		errs = append(errs, fmt.Errorf("PRIVO_LISTEN %q: %w", cfg.ListenAddr, err))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(orDefault(getenv("PRIVO_LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("PRIVO_LOG_LEVEL: %w", err))
	}

	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("PRIVO_LOG_FORMAT %q: must be json or text", cfg.LogFormat))
	}

	if v := getenv("PRIVO_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("PRIVO_SHUTDOWN_TIMEOUT: %w", err))
		case d <= 0 || d > 5*time.Minute:
			errs = append(errs, fmt.Errorf("PRIVO_SHUTDOWN_TIMEOUT %s: must be between 0 and 5m", d))
		default:
			cfg.ShutdownTimeout = d
		}
	}

	cfg.Runner = strings.TrimSpace(getenv("PRIVO_RUNNER"))
	switch {
	case cfg.Runner != "" && cfg.Runner != "local":
		errs = append(errs, fmt.Errorf("PRIVO_RUNNER %q: must be empty or local", cfg.Runner))
	case cfg.Runner == "local" && getenv("PRIVO_INSECURE_LOCAL_RUNNER") != "i-understand":
		errs = append(errs, errors.New("PRIVO_RUNNER=local runs MCP servers unisolated as the gateway's own user, where they can read its data and keys; "+
			"it is for development only. Set PRIVO_INSECURE_LOCAL_RUNNER=i-understand to use it"))
	}
	cfg.EgressAllow = getenv("PRIVO_EGRESS_ALLOW_CIDRS")
	for _, part := range strings.Split(getenv("PRIVO_TRUSTED_PROXIES"), ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			errs = append(errs, fmt.Errorf("PRIVO_TRUSTED_PROXIES %q: each entry must be a CIDR such as 10.0.0.0/8", part))
			continue
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, p.Masked())
	}
	for _, d := range []struct {
		name string
		dst  *time.Duration
		def  time.Duration
	}{
		{"PRIVO_UPSTREAM_RETRY_BASE", &cfg.RetryBase, 2 * time.Second},
		{"PRIVO_UPSTREAM_RETRY_MAX", &cfg.RetryMax, 30 * time.Second},
		{"PRIVO_TOKEN_TTL", &cfg.TokenTTL, 90 * 24 * time.Hour},
		{"PRIVO_APPROVAL_HOLD", &cfg.ApprovalHold, 25 * time.Second},
		{"PRIVO_UPSTREAM_CALL_TIMEOUT", &cfg.CallTimeout, 10 * time.Minute},
		{"PRIVO_UPSTREAM_REFRESH_GAP", &cfg.RefreshGap, 5 * time.Second},
	} {
		*d.dst = d.def
		if v := getenv(d.name); v != "" {
			dur, err := time.ParseDuration(v)
			if err != nil || dur <= 0 {
				errs = append(errs, fmt.Errorf("%s %q: must be a positive duration", d.name, v))
				continue
			}
			*d.dst = dur
		}
	}

	cfg.MaxResponseBytes = DefaultMaxResponseBytes
	if v := getenv("PRIVO_UPSTREAM_MAX_RESPONSE_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < MinMaxResponseBytes || n > MaxMaxResponseBytes {
			errs = append(errs, fmt.Errorf("PRIVO_UPSTREAM_MAX_RESPONSE_BYTES %q: must be a number of bytes from %d to %d", v, MinMaxResponseBytes, MaxMaxResponseBytes))
		} else {
			cfg.MaxResponseBytes = n
		}
	}

	if v := getenv("PRIVO_BACKUP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < MinBackupInterval {
			errs = append(errs, fmt.Errorf("PRIVO_BACKUP_INTERVAL %q: must be a duration of at least %s, such as 24h", v, MinBackupInterval))
		} else {
			cfg.BackupInterval = d
		}
	}
	cfg.BackupKeep = DefaultBackupKeep
	if v := getenv("PRIVO_BACKUP_KEEP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxBackupKeep {
			errs = append(errs, fmt.Errorf("PRIVO_BACKUP_KEEP %q: must be a number from 1 to %d", v, MaxBackupKeep))
		} else {
			cfg.BackupKeep = n
		}
	}
	cfg.BackupDir = orDefault(getenv("PRIVO_BACKUP_DIR"), filepath.Join(cfg.DataDir, "backups"))

	if v := getenv("PRIVO_PUBLIC_URL"); v != "" {
		u, err := parsePublicURL(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("PRIVO_PUBLIC_URL: %w", err))
		} else {
			cfg.PublicURL = u
		}
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// ErrBootstrapUnreadable marks a bootstrap token file that cannot be read or is
// empty, as opposed to a value that is not a token. Whether that matters
// depends on whether the organisation still needs a bootstrap token, which
// only the store knows.
var ErrBootstrapUnreadable = errors.New("cannot read the bootstrap token")

// LoadBootstrapToken reads and checks the token made with `privo token
// generate` that the first boot installs as the owner's control token
// (PRIVO_BOOTSTRAP_TOKEN, or PRIVO_BOOTSTRAP_TOKEN_FILE, which wins; F24). It
// returns an empty Secret when neither is set. Only `serve` calls it: Load
// does not, so the healthcheck and the break-glass `token create` never depend
// on a variable that is left over once the token has been used (R-03,
// independent review 2026-10-29). Errors name the variable but never repeat its
// value.
func LoadBootstrapToken(getenv func(string) string) (Secret, error) {
	name, raw := "PRIVO_BOOTSTRAP_TOKEN", getenv("PRIVO_BOOTSTRAP_TOKEN")
	if path := strings.TrimSpace(getenv("PRIVO_BOOTSTRAP_TOKEN_FILE")); path != "" {
		name = "PRIVO_BOOTSTRAP_TOKEN_FILE"
		// A FIFO without a writer blocks Open for good, and a directory or a
		// device is not a place for a token (R-11, independent review 2026-10-29).
		// Stat follows symlinks, which is how some platforms mount secrets.
		fi, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("%s: %w: %w", name, ErrBootstrapUnreadable, err)
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s: %w: not a regular file", name, ErrBootstrapUnreadable)
		}
		f, err := os.Open(path) //nolint:gosec // operator-supplied path is the point
		if err != nil {
			return "", fmt.Errorf("%s: %w: %w", name, ErrBootstrapUnreadable, err)
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(io.LimitReader(f, 1<<10))
		if err != nil {
			return "", fmt.Errorf("%s: %w: %w", name, ErrBootstrapUnreadable, err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return "", fmt.Errorf("%s: %w: the file is empty", name, ErrBootstrapUnreadable)
		}
		raw = string(b)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	_, secret, err := token.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s: not a valid Privo token; make one with `privo token generate`", name)
	}
	if token.Weak(secret) {
		// It would be an owner credential with no entropy (R-10).
		return "", fmt.Errorf("%s: the token looks hand-made, its secret is too regular to be random; make one with `privo token generate`", name)
	}
	return Secret(raw), nil
}

// BootstrapTokenSource names the variable LoadBootstrapToken reads the token
// from: PRIVO_BOOTSTRAP_TOKEN_FILE when it is set, which wins, otherwise
// PRIVO_BOOTSTRAP_TOKEN, or "" when there is none.
func BootstrapTokenSource(getenv func(string) string) string {
	switch {
	case strings.TrimSpace(getenv("PRIVO_BOOTSTRAP_TOKEN_FILE")) != "":
		return "PRIVO_BOOTSTRAP_TOKEN_FILE"
	case strings.TrimSpace(getenv("PRIVO_BOOTSTRAP_TOKEN")) != "":
		return "PRIVO_BOOTSTRAP_TOKEN"
	}
	return ""
}

func parsePublicURL(v string) (*url.URL, error) {
	u, err := url.Parse(v)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%q has no host", v)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%q must not contain credentials, a query or a fragment", v)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, fmt.Errorf("%q must use https unless the host is loopback", v)
		}
	default:
		return nil, fmt.Errorf("%q must use https", v)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
