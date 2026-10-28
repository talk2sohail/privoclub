package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr || cfg.DataDir != DefaultDataDir ||
		cfg.LogFormat != "json" || cfg.LogLevel != slog.LevelInfo ||
		cfg.ShutdownTimeout != DefaultShutdownTimeout || cfg.PublicURL != nil {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadValues(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PRIVO_LISTEN":           "127.0.0.1:9000",
		"PRIVO_DATA_DIR":         "/data",
		"PRIVO_LOG_LEVEL":        "debug",
		"PRIVO_LOG_FORMAT":       "TEXT",
		"PRIVO_SHUTDOWN_TIMEOUT": "10s",
		"PRIVO_PUBLIC_URL":       "https://acme.privo.club/",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" || cfg.DataDir != "/data" || cfg.LogLevel != slog.LevelDebug ||
		cfg.LogFormat != "text" || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if got := cfg.PublicURL.String(); got != "https://acme.privo.club" {
		t.Fatalf("PublicURL = %q", got)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PRIVO_LISTEN":           "no-port",
		"PRIVO_LOG_LEVEL":        "loud",
		"PRIVO_LOG_FORMAT":       "xml",
		"PRIVO_SHUTDOWN_TIMEOUT": "-1s",
		"PRIVO_PUBLIC_URL":       "ftp://x",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"PRIVO_LISTEN", "PRIVO_LOG_LEVEL", "PRIVO_LOG_FORMAT", "PRIVO_SHUTDOWN_TIMEOUT", "PRIVO_PUBLIC_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}

func TestLocalRunnerNeedsAcknowledgement(t *testing.T) {
	if _, err := Load(env(map[string]string{"PRIVO_RUNNER": "local"})); err == nil || !strings.Contains(err.Error(), "PRIVO_INSECURE_LOCAL_RUNNER") {
		t.Fatalf("local runner without acknowledgement: %v", err)
	}
	cfg, err := Load(env(map[string]string{"PRIVO_RUNNER": "local", "PRIVO_INSECURE_LOCAL_RUNNER": "i-understand"}))
	if err != nil || cfg.Runner != "local" {
		t.Fatalf("acknowledged local runner: %v", err)
	}
}

func TestPublicURL(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"https://acme.privo.club", true},
		{"http://localhost:8080", true},
		{"http://127.0.0.1:8080", true},
		{"http://acme.privo.club", false},
		{"https://user:pass@acme.privo.club", false},
		{"https://acme.privo.club/?x=1", false},
		{"https:///nohost", false},
	}
	for _, c := range cases {
		_, err := Load(env(map[string]string{"PRIVO_PUBLIC_URL": c.in}))
		if (err == nil) != c.ok {
			t.Errorf("%q: ok=%v, err=%v", c.in, c.ok, err)
		}
	}
}
