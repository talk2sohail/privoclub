package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Defaults and limits of the admin site's sign-in (ADR 0016).
const (
	DefaultGitHubBaseURL = "https://github.com"
	DefaultGitHubAPIURL  = "https://api.github.com"

	DefaultSessionIdle   = 2 * time.Hour
	DefaultSessionMaxAge = 12 * time.Hour
	MinSessionIdle       = time.Minute
	MaxSessionIdle       = 24 * time.Hour
	MinSessionMaxAge     = 5 * time.Minute
	MaxSessionMaxAge     = 30 * 24 * time.Hour

	maxClientSecretLen = 256
	maxSecretFileBytes = 1 << 10
)

var clientIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// Web is the admin site's sign-in configuration. It is read by LoadWeb, which
// only `serve` and `admin setup-link` call, so a bad value here cannot stop the
// container healthcheck or the break-glass `token create` (the lesson of R-03).
type Web struct {
	// ClientID and ClientSecret are the GitHub OAuth App's credentials
	// (PRIVO_GITHUB_CLIENT_ID, PRIVO_GITHUB_CLIENT_SECRET or its _FILE).
	ClientID     string
	ClientSecret Secret
	// BaseURL is where people are sent to sign in and where the code is
	// exchanged (PRIVO_GITHUB_BASE_URL); APIURL serves GET /user
	// (PRIVO_GITHUB_API_URL). Both follow the public URL rule: https, except
	// loopback. GitHub Enterprise Server sets both.
	BaseURL, APIURL *url.URL
	// IdleTimeout and MaxAge bound a browser session (PRIVO_SESSION_IDLE_TIMEOUT,
	// PRIVO_SESSION_MAX_AGE).
	IdleTimeout, MaxAge time.Duration
}

// Missing lists the variables that must still be set before web sign-in can
// run, given the public URL from Load. Names only, never values.
func (w Web) Missing(publicURL *url.URL) []string {
	var out []string
	if w.ClientID == "" {
		out = append(out, "PRIVO_GITHUB_CLIENT_ID")
	}
	if w.ClientSecret == "" {
		out = append(out, "PRIVO_GITHUB_CLIENT_SECRET")
	}
	if publicURL == nil {
		out = append(out, "PRIVO_PUBLIC_URL")
	}
	return out
}

// Enabled reports whether web sign-in has everything it needs.
func (w Web) Enabled(publicURL *url.URL) bool { return len(w.Missing(publicURL)) == 0 }

// LoadWeb reads and checks the web sign-in settings. All problems are reported
// together; none repeats a secret.
func LoadWeb(getenv func(string) string) (Web, error) {
	w := Web{ClientID: strings.TrimSpace(getenv("PRIVO_GITHUB_CLIENT_ID")), IdleTimeout: DefaultSessionIdle, MaxAge: DefaultSessionMaxAge}
	var errs []error

	if w.ClientID != "" && !clientIDRe.MatchString(w.ClientID) {
		errs = append(errs, errors.New("PRIVO_GITHUB_CLIENT_ID: must be 1 to 100 letters, digits, dots, dashes or underscores (copy the Client ID from the GitHub OAuth App)"))
		w.ClientID = ""
	}

	secret, err := loadClientSecret(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	w.ClientSecret = secret

	for _, u := range []struct {
		name string
		dst  **url.URL
		def  string
	}{
		{"PRIVO_GITHUB_BASE_URL", &w.BaseURL, DefaultGitHubBaseURL},
		{"PRIVO_GITHUB_API_URL", &w.APIURL, DefaultGitHubAPIURL},
	} {
		raw := orDefault(getenv(u.name), u.def)
		parsed, err := parsePublicURL(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u.name, err))
			parsed, _ = parsePublicURL(u.def)
		}
		*u.dst = parsed
	}

	idleSet, maxSet := false, false
	if v := getenv("PRIVO_SESSION_IDLE_TIMEOUT"); strings.TrimSpace(v) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d < MinSessionIdle || d > MaxSessionIdle {
			errs = append(errs, fmt.Errorf("PRIVO_SESSION_IDLE_TIMEOUT %q: must be a duration from %s to %s", v, MinSessionIdle, MaxSessionIdle))
		} else {
			w.IdleTimeout, idleSet = d, true
		}
	}
	if v := getenv("PRIVO_SESSION_MAX_AGE"); strings.TrimSpace(v) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d < MinSessionMaxAge || d > MaxSessionMaxAge {
			errs = append(errs, fmt.Errorf("PRIVO_SESSION_MAX_AGE %q: must be a duration from %s to %s", v, MinSessionMaxAge, MaxSessionMaxAge))
		} else {
			w.MaxAge, maxSet = d, true
		}
	}
	switch {
	case idleSet && w.IdleTimeout > w.MaxAge:
		errs = append(errs, fmt.Errorf("PRIVO_SESSION_IDLE_TIMEOUT %s: must not be longer than PRIVO_SESSION_MAX_AGE (%s)", w.IdleTimeout, w.MaxAge))
	case maxSet && !idleSet && w.IdleTimeout > w.MaxAge:
		w.IdleTimeout = w.MaxAge // a short lifetime alone should not need a second setting
	}

	if len(errs) > 0 {
		return Web{}, errors.Join(errs...)
	}
	return w, nil
}

// loadClientSecret reads the GitHub client secret from its file (which wins) or
// its variable. Errors name the variable and never repeat the value.
func loadClientSecret(getenv func(string) string) (Secret, error) {
	name, raw := "PRIVO_GITHUB_CLIENT_SECRET", getenv("PRIVO_GITHUB_CLIENT_SECRET")
	if path := strings.TrimSpace(getenv("PRIVO_GITHUB_CLIENT_SECRET_FILE")); path != "" {
		name = "PRIVO_GITHUB_CLIENT_SECRET_FILE"
		b, err := readSmallFile(name, path)
		if err != nil {
			return "", err
		}
		raw = string(b)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if name == "PRIVO_GITHUB_CLIENT_SECRET_FILE" {
			return "", fmt.Errorf("%s: the file is empty", name)
		}
		return "", nil
	}
	if len(raw) > maxClientSecretLen || strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", fmt.Errorf("%s: not a client secret (up to %d printable characters, no spaces); copy it from the GitHub OAuth App", name, maxClientSecretLen)
	}
	return Secret(raw), nil
}

// readSmallFile reads a secret file of at most 1 KiB. A FIFO, directory or
// device is refused before it is opened (a FIFO without a writer blocks for
// good); a symlink to a regular file works, as on platforms that mount secrets
// that way.
func readSmallFile(name, path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s is not a regular file", name, path)
	}
	f, err := os.Open(path) //nolint:gosec // operator-supplied path is the point
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if len(b) > maxSecretFileBytes {
		return nil, fmt.Errorf("%s: the file is larger than 1 KiB", name)
	}
	return b, nil
}
