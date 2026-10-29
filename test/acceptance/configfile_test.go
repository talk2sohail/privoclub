//go:build acceptance

package acceptance

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The configuration file (F25, ADR 0015), through the real binary.

// withoutEnv returns env without the named variables.
func withoutEnv(env []string, names ...string) []string {
	var out []string
outer:
	for _, kv := range env {
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "privo.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A gateway whose address, data directory, master key file and timings come
// from the file alone serves, and the break-glass command and the status
// command read the same file.
func TestConfigFileAloneConfiguresTheGateway(t *testing.T) {
	g := newGateway(t)
	listen := strings.TrimPrefix(g.base, "http://")
	keyFile := envValue(g.env, "PRIVO_MASTER_KEY_FILE")
	file := writeConfigFile(t, strings.Join([]string{
		"# Privo, acceptance",
		"listen: \"" + listen + "\"",
		"data_dir: " + g.dataDir,
		"master_key_file: " + keyFile,
		"public_url: " + g.base,
		"log_format: text",
		"approval_hold: 300ms",
		"backup_interval: 1h",
		"",
	}, "\n"))
	g.env = withoutEnv(g.env, "PRIVO_LISTEN", "PRIVO_DATA_DIR", "PRIVO_MASTER_KEY_FILE", "PRIVO_PUBLIC_URL", "PRIVO_LOG_FORMAT", "PRIVO_APPROVAL_HOLD")
	g.env = append(g.env, "PRIVO_CONFIG_FILE="+file)

	g.issueTokens() // `token create` finds the data directory in the file
	g.start()       // so does `serve`, and so does the readiness check
	if code, _ := g.raw(http.MethodGet, "/readyz", "", nil); code != http.StatusOK {
		t.Fatalf("readyz = %d", code)
	}
	if _, err := os.Stat(filepath.Join(g.dataDir, "privo.db")); err != nil {
		t.Fatalf("the database is not in the file's data_dir: %v", err)
	}
	g.eventually("scheduled backups from the file", 5*time.Second, func() bool {
		return strings.Contains(g.logs.String(), "scheduled backups on")
	})
	if strings.Contains(g.logs.String(), "{\"time\"") {
		t.Errorf("log_format: text from the file was not used:\n%s", g.logs.String())
	}

	out, err := g.command("config", "show").CombinedOutput()
	if err != nil {
		t.Fatalf("config show: %v\n%s", err, out)
	}
	text := string(out)
	for _, w := range []string{file, "PRIVO_LISTEN", listen, "PRIVO_BACKUP_INTERVAL", "1h", "file"} {
		if !strings.Contains(text, w) {
			t.Errorf("config show lacks %q:\n%s", w, text)
		}
	}
	if keyText, _ := os.ReadFile(keyFile); strings.Contains(text, strings.TrimSpace(string(keyText))) {
		t.Errorf("config show printed the master key")
	}
}

// The environment beats the file, so one deployment can override a shared file.
func TestConfigEnvironmentBeatsTheFile(t *testing.T) {
	g := newGateway(t)
	file := writeConfigFile(t, "listen: \"127.0.0.1:1\"\napproval_hold: 1h\n") // not the real port
	g.env = append(g.env, "PRIVO_CONFIG_FILE="+file)
	g.issueTokens()
	g.start() // PRIVO_LISTEN in the environment is the real port
	if code, _ := g.raw(http.MethodGet, "/readyz", "", nil); code != http.StatusOK {
		t.Fatalf("readyz = %d", code)
	}
	out, err := g.command("config", "show").CombinedOutput()
	if err != nil {
		t.Fatalf("config show: %v\n%s", err, out)
	}
	var hold, listen string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "PRIVO_APPROVAL_HOLD" {
			hold = f[1] + " " + f[2]
		}
		if len(f) == 3 && f[0] == "PRIVO_LISTEN" {
			listen = f[1] + " " + f[2]
		}
	}
	// The file says 1h and "127.0.0.1:1"; the environment's values are in force.
	if hold != "300ms environment" || listen != strings.TrimPrefix(g.base, "http://")+" environment" {
		t.Errorf("approval_hold = %q, listen = %q; want the environment's values:\n%s", hold, listen, out)
	}
}

// A mistake in the file stops the start with a message that names the file and
// the key, and a secret in the file is refused without being repeated.
func TestConfigFileMistakesDoNotStart(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    []string
		never   []string
	}{
		"typo": {"lisen: \":1\"\n", []string{`"lisen"`}, nil},
		"secret": {"master_key: c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LTEyMzQ=\n",
			[]string{`"master_key"`, "master_key_file"}, []string{"c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LTEyMzQ="}},
		"list":      {"listen: [\":1\", \":2\"]\n", []string{`"listen"`, "single value"}, nil},
		"bad value": {"backup_keep: many\n", []string{"PRIVO_BACKUP_KEEP"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGateway(t)
			file := writeConfigFile(t, tc.content)
			g.env = append(g.env, "PRIVO_CONFIG_FILE="+file)
			out, err := runWithin(g.command("serve"), 10*time.Second)
			if exitCode(err) != 2 {
				t.Fatalf("exit %d, output %q, want exit 2", exitCode(err), out)
			}
			text := string(out)
			for _, w := range append([]string{file}, tc.want...) {
				if name == "bad value" && w == file {
					continue // a bad value is reported by variable name, as for the environment
				}
				if !strings.Contains(text, w) {
					t.Errorf("output lacks %q:\n%s", w, text)
				}
			}
			for _, n := range tc.never {
				if strings.Contains(text, n) {
					t.Errorf("output repeats %q", n)
				}
			}
			if _, err := os.Stat(g.dataDir); err == nil {
				t.Errorf("the data directory was created although the configuration was refused")
			}
		})
	}
}
