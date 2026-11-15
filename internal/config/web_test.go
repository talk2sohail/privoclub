package config

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Web sign-in (ADR 0016) is configured by its own loader, like the bootstrap
// token: the shared Load stays free of anything that could make the container
// healthcheck fail.

func TestWebSettingsDefaults(t *testing.T) {
	w, err := LoadWeb(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if w.BaseURL.String() != "https://github.com" || w.APIURL.String() != "https://api.github.com" {
		t.Errorf("urls %s, %s", w.BaseURL, w.APIURL)
	}
	if w.IdleTimeout != 2*time.Hour || w.MaxAge != 12*time.Hour {
		t.Errorf("idle %s, max age %s; want 2h, 12h", w.IdleTimeout, w.MaxAge)
	}
	if w.ClientID != "" || w.ClientSecret != "" {
		t.Errorf("nothing was set, got id %q secret set=%v", w.ClientID, w.ClientSecret != "")
	}
}

func TestWebSettingsValues(t *testing.T) {
	w, err := LoadWeb(env(map[string]string{
		"PRIVO_GITHUB_CLIENT_ID":     "Iv1.0123456789abcdef",
		"PRIVO_GITHUB_CLIENT_SECRET": "s3cretvaluefromtheenvironment",
		"PRIVO_GITHUB_BASE_URL":      "https://ghe.example.com/",
		"PRIVO_GITHUB_API_URL":       "https://ghe.example.com/api/v3",
		"PRIVO_SESSION_IDLE_TIMEOUT": "30m",
		"PRIVO_SESSION_MAX_AGE":      "8h",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if w.ClientID != "Iv1.0123456789abcdef" || w.ClientSecret.Reveal() != "s3cretvaluefromtheenvironment" {
		t.Errorf("id/secret not read: %q", w.ClientID)
	}
	if w.BaseURL.String() != "https://ghe.example.com" || w.APIURL.String() != "https://ghe.example.com/api/v3" {
		t.Errorf("urls %s, %s (a trailing slash is trimmed, a path stays)", w.BaseURL, w.APIURL)
	}
	if w.IdleTimeout != 30*time.Minute || w.MaxAge != 8*time.Hour {
		t.Errorf("idle %s, max age %s", w.IdleTimeout, w.MaxAge)
	}
}

func TestWebSettingsLimits(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"PRIVO_SESSION_IDLE_TIMEOUT", "soon"},
		{"PRIVO_SESSION_IDLE_TIMEOUT", "-5m"},
		{"PRIVO_SESSION_IDLE_TIMEOUT", "10s"},                  // below the minute floor
		{"PRIVO_SESSION_IDLE_TIMEOUT", "48h"},                  // above a day
		{"PRIVO_SESSION_MAX_AGE", "1m"},                        // below 5 minutes
		{"PRIVO_SESSION_MAX_AGE", "9000h"},                     // above 30 days
		{"PRIVO_SESSION_MAX_AGE", "forever"},                   // not a duration
		{"PRIVO_GITHUB_BASE_URL", "http://github.example.com"}, // http only for loopback
		{"PRIVO_GITHUB_BASE_URL", "ftp://github.example.com"},
		{"PRIVO_GITHUB_BASE_URL", "https://user:pw@github.example.com"},
		{"PRIVO_GITHUB_API_URL", "https://api.example.com/?x=1"},
		{"PRIVO_GITHUB_API_URL", "not a url"},
		{"PRIVO_GITHUB_CLIENT_ID", "has space"},
		{"PRIVO_GITHUB_CLIENT_ID", strings.Repeat("a", 101)},
		{"PRIVO_GITHUB_CLIENT_ID", "id\nwith-newline"},
		{"PRIVO_GITHUB_CLIENT_SECRET", "two words"},
		{"PRIVO_GITHUB_CLIENT_SECRET", strings.Repeat("x", 300)},
	} {
		_, err := LoadWeb(env(map[string]string{tc.name: tc.value}))
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: error %v, want one that names the variable", tc.name, tc.value, err)
			continue
		}
		// A secret never appears in its own error message.
		if tc.name == "PRIVO_GITHUB_CLIENT_SECRET" && strings.Contains(err.Error(), tc.value) {
			t.Errorf("the error for %s repeats the value: %v", tc.name, err)
		}
	}

	// The idle timeout cannot be longer than the absolute lifetime.
	_, err := LoadWeb(env(map[string]string{"PRIVO_SESSION_IDLE_TIMEOUT": "6h", "PRIVO_SESSION_MAX_AGE": "1h"}))
	if err == nil || !strings.Contains(err.Error(), "PRIVO_SESSION_IDLE_TIMEOUT") {
		t.Errorf("idle longer than max age: %v", err)
	}
}

// A loopback http URL is how tests and development point at a fake GitHub.
func TestWebURLsAllowLoopbackHTTP(t *testing.T) {
	w, err := LoadWeb(env(map[string]string{
		"PRIVO_GITHUB_BASE_URL": "http://127.0.0.1:9999",
		"PRIVO_GITHUB_API_URL":  "http://localhost:9999/api",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if w.BaseURL.Host != "127.0.0.1:9999" || w.APIURL.Path != "/api" {
		t.Errorf("urls %s, %s", w.BaseURL, w.APIURL)
	}
}

func TestWebSignInNeedsAllThree(t *testing.T) {
	pub := mustURL(t, "https://app.example.com")
	full := Web{ClientID: "id", ClientSecret: "secret"}
	for _, tc := range []struct {
		name string
		w    Web
		pub  bool
		want []string
	}{
		{"nothing", Web{}, false, []string{"PRIVO_GITHUB_CLIENT_ID", "PRIVO_GITHUB_CLIENT_SECRET", "PRIVO_PUBLIC_URL"}},
		{"only the id", Web{ClientID: "id"}, false, []string{"PRIVO_GITHUB_CLIENT_SECRET", "PRIVO_PUBLIC_URL"}},
		{"no public url", full, false, []string{"PRIVO_PUBLIC_URL"}},
		{"no secret", Web{ClientID: "id"}, true, []string{"PRIVO_GITHUB_CLIENT_SECRET"}},
		{"all three", full, true, nil},
	} {
		var u = pub
		if !tc.pub {
			u = nil
		}
		got := tc.w.Missing(u)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Missing = %v, want %v", tc.name, got, tc.want)
		}
		if tc.w.Enabled(u) != (len(tc.want) == 0) {
			t.Errorf("%s: Enabled = %v with missing %v", tc.name, tc.w.Enabled(u), tc.want)
		}
	}
}

func TestGitHubSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("the file wins over the variable and a trailing newline is trimmed", func(t *testing.T) {
		p := write("secret", "from-the-file\n")
		w, err := LoadWeb(env(map[string]string{"PRIVO_GITHUB_CLIENT_SECRET": "from-the-variable", "PRIVO_GITHUB_CLIENT_SECRET_FILE": p}))
		if err != nil || w.ClientSecret.Reveal() != "from-the-file" {
			t.Fatalf("secret = %q, err %v", w.ClientSecret.Reveal(), err)
		}
	})

	t.Run("the secret redacts itself when printed or logged", func(t *testing.T) {
		w, _ := LoadWeb(env(map[string]string{"PRIVO_GITHUB_CLIENT_SECRET": "do-not-print-me"}))
		if strings.Contains(w.ClientSecret.String(), "do-not-print-me") {
			t.Error("the secret prints itself")
		}
	})

	for _, tc := range []struct {
		name string
		path func() string
		want string
	}{
		{"missing file", func() string { return filepath.Join(dir, "nope") }, "PRIVO_GITHUB_CLIENT_SECRET_FILE"},
		{"empty file", func() string { return write("empty", " \n") }, "empty"},
		{"a directory", func() string { return dir }, "not a regular file"},
		{"too large", func() string { return write("big", strings.Repeat("x", 2000)) }, "1 KiB"},
		{"two words", func() string { return write("words", "two words") }, "PRIVO_GITHUB_CLIENT_SECRET_FILE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadWeb(env(map[string]string{"PRIVO_GITHUB_CLIENT_SECRET_FILE": tc.path()}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "two words") {
				t.Errorf("the error repeats the file's content: %v", err)
			}
		})
	}
}

// The shared Load does not read the web settings, so a bad value there cannot
// stop the healthcheck or `privo token create`.
func TestLoadIgnoresWebSettings(t *testing.T) {
	if _, err := Load(env(map[string]string{
		"PRIVO_GITHUB_CLIENT_SECRET_FILE": "/does/not/exist",
		"PRIVO_SESSION_MAX_AGE":           "nonsense",
	})); err != nil {
		t.Fatalf("Load reads the web settings: %v", err)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := parsePublicURL(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// ADR 0022: the authorization server is off unless switched on; the client
// policy defaults to known; a value that is not understood stops startup
// rather than leaving a security switch in a state nobody chose.
func TestOAuthServerSettings(t *testing.T) {
	w, err := LoadWeb(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if w.OAuthServer || w.OAuthRegistration || w.OAuthClients != OAuthClientsKnown {
		t.Errorf("defaults: server %v, registration %v, clients %q; want off, off, known", w.OAuthServer, w.OAuthRegistration, w.OAuthClients)
	}
	for _, on := range []string{"on", "ON", "true", "1", "yes", " on "} {
		w, err := LoadWeb(env(map[string]string{"PRIVO_OAUTH_SERVER": on, "PRIVO_OAUTH_DYNAMIC_REGISTRATION": on}))
		if err != nil || !w.OAuthServer || !w.OAuthRegistration {
			t.Errorf("%q: server %v, registration %v, %v", on, w.OAuthServer, w.OAuthRegistration, err)
		}
	}
	for _, off := range []string{"off", "false", "0", "no", ""} {
		w, err := LoadWeb(env(map[string]string{"PRIVO_OAUTH_SERVER": off}))
		if err != nil || w.OAuthServer {
			t.Errorf("%q: server %v, %v", off, w.OAuthServer, err)
		}
	}
	for _, policy := range []string{"known", "approved", "any", "ANY"} {
		w, err := LoadWeb(env(map[string]string{"PRIVO_OAUTH_CLIENTS": policy}))
		if err != nil || w.OAuthClients != strings.ToLower(policy) {
			t.Errorf("PRIVO_OAUTH_CLIENTS=%q: %q, %v", policy, w.OAuthClients, err)
		}
	}
	for name, bad := range map[string]map[string]string{
		"server":       {"PRIVO_OAUTH_SERVER": "enabled"},
		"registration": {"PRIVO_OAUTH_DYNAMIC_REGISTRATION": "maybe"},
		"clients":      {"PRIVO_OAUTH_CLIENTS": "all"},
	} {
		if _, err := LoadWeb(env(bad)); err == nil {
			t.Errorf("%s: an unknown value was accepted", name)
		}
	}
}
