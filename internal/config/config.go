// Package config loads and validates process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"
)

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
	// TokenTTL is the default personal token lifetime (PRIVO_TOKEN_TTL).
	TokenTTL time.Duration
	// ApprovalHold is how long a call waits for a person's decision before the
	// caller is told to retry (PRIVO_APPROVAL_HOLD).
	ApprovalHold time.Duration
}

// Defaults applied when a variable is unset or empty.
const (
	DefaultListenAddr      = ":8080"
	DefaultDataDir         = "./data"
	DefaultLogFormat       = "json"
	DefaultShutdownTimeout = 30 * time.Second
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
	for _, d := range []struct {
		name string
		dst  *time.Duration
		def  time.Duration
	}{
		{"PRIVO_UPSTREAM_RETRY_BASE", &cfg.RetryBase, 2 * time.Second},
		{"PRIVO_UPSTREAM_RETRY_MAX", &cfg.RetryMax, 30 * time.Second},
		{"PRIVO_TOKEN_TTL", &cfg.TokenTTL, 90 * 24 * time.Hour},
		{"PRIVO_APPROVAL_HOLD", &cfg.ApprovalHold, 25 * time.Second},
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
