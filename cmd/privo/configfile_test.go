package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "privo.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var cols = regexp.MustCompile(`\s{2,}`)

// showRows runs `config show` and returns name -> {value, source}.
func showRows(t *testing.T, args []string, env map[string]string) (map[string][2]string, string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append(args, "config", "show"), &out, &errb, envMap(env))
	rows := map[string][2]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		f := cols.Split(strings.TrimSpace(line), -1)
		if len(f) == 3 && strings.HasPrefix(f[0], "PRIVO_") {
			rows[f[0]] = [2]string{f[1], f[2]}
		}
	}
	return rows, out.String(), errb.String(), code
}

func TestConfigFlagSuppliesSettings(t *testing.T) {
	p := writeYAML(t, "listen: \":9090\"\nbackup_keep: 3\nlog_format: text\n")
	rows, out, errs, code := showRows(t, []string{"--config", p}, map[string]string{"PRIVO_LOG_FORMAT": "json"})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, p) {
		t.Errorf("the output does not name the file %s:\n%s", p, out)
	}
	for name, want := range map[string][2]string{
		"PRIVO_LISTEN":      {":9090", "file"},
		"PRIVO_BACKUP_KEEP": {"3", "file"},
		"PRIVO_LOG_FORMAT":  {"json", "environment"}, // the environment beats the file
		"PRIVO_DATA_DIR":    {"-", "default"},
	} {
		if rows[name] != want {
			t.Errorf("%s = %v, want %v\n%s", name, rows[name], want, out)
		}
	}
}

func TestConfigFileFromTheVariableAndFlagWins(t *testing.T) {
	a := writeYAML(t, "listen: \":1111\"\n")
	b := writeYAML(t, "listen: \":2222\"\n")
	rows, _, errs, code := showRows(t, nil, map[string]string{"PRIVO_CONFIG_FILE": a})
	if code != 0 || rows["PRIVO_LISTEN"][0] != ":1111" {
		t.Fatalf("variable: exit %d, %v, %s", code, rows["PRIVO_LISTEN"], errs)
	}
	rows, _, errs, code = showRows(t, []string{"--config=" + b}, map[string]string{"PRIVO_CONFIG_FILE": a})
	if code != 0 || rows["PRIVO_LISTEN"][0] != ":2222" {
		t.Fatalf("flag: exit %d, %v, %s", code, rows["PRIVO_LISTEN"], errs)
	}
}

func TestConfigFlagNeedsAValue(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--config"}, &out, &errb, envMap(nil)); code != 2 || !strings.Contains(errb.String(), "--config") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

func TestBadConfigFileStopsTheCommandsThatReadIt(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	bad := writeYAML(t, "lisen: \":1\"\nmaster_key: SECRET-VALUE\n")
	for _, args := range [][]string{
		{"serve"},
		{"healthcheck"},
		{"config", "show"},
		{"backup"},
		{"restore", "--from", "x.db"},
		{"token", "create", "--name", "a", "--role", "owner", "--kind", "control"},
	} {
		var out, errb bytes.Buffer
		code := run(append([]string{"--config", bad}, args...), &out, &errb, envMap(map[string]string{"PRIVO_DATA_DIR": data}))
		if code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, errb.String())
		}
		for _, w := range []string{bad, `"lisen"`, "master_key_file"} {
			if !strings.Contains(errb.String(), w) {
				t.Errorf("%v: stderr lacks %q: %s", args, w, errb.String())
			}
		}
		if strings.Contains(errb.String(), "SECRET-VALUE") {
			t.Errorf("%v: the secret value was repeated: %s", args, errb.String())
		}
	}
	if _, err := os.Stat(data); err == nil {
		t.Errorf("a command touched the data directory despite the bad file")
	}
}

func TestMissingConfigFileStopsTheCommand(t *testing.T) {
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if code := run([]string{"--config", missing, "serve"}, &out, &errb, envMap(nil)); code != 2 || !strings.Contains(errb.String(), missing) {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

func TestCommandsThatNeedNoConfigIgnoreTheFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	for _, args := range [][]string{{"version"}, {"help"}, {"token", "generate"}} {
		var out, errb bytes.Buffer
		if code := run(append([]string{"--config", missing}, args...), &out, &errb, envMap(map[string]string{"PRIVO_CONFIG_FILE": missing})); code != 0 {
			t.Errorf("%v: exit %d, stderr %q; these commands never read the file", args, code, errb.String())
		}
	}
}

func TestConfigShowNeverPrintsSecrets(t *testing.T) {
	const secret = "TOPSECRET-master-key-value"
	rows, out, _, code := showRows(t, nil, map[string]string{"PRIVO_MASTER_KEY": secret, "PRIVO_BOOTSTRAP_TOKEN_FILE": "/run/secrets/t"})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("a secret value is in the output:\n%s", out)
	}
	if rows["PRIVO_MASTER_KEY"] != [2]string{"set", "environment"} || rows["PRIVO_BOOTSTRAP_TOKEN"] != [2]string{"unset", "default"} {
		t.Errorf("secret rows = %v, %v", rows["PRIVO_MASTER_KEY"], rows["PRIVO_BOOTSTRAP_TOKEN"])
	}
	if rows["PRIVO_BOOTSTRAP_TOKEN_FILE"] != [2]string{"/run/secrets/t", "environment"} {
		t.Errorf("a file path is not a secret: %v", rows["PRIVO_BOOTSTRAP_TOKEN_FILE"])
	}
}

func TestConfigShowReportsProblems(t *testing.T) {
	_, _, errs, code := showRows(t, nil, map[string]string{"PRIVO_LOG_FORMAT": "xml", "PRIVO_LISTEN": "bad"})
	if code != 2 || !strings.Contains(errs, "PRIVO_LOG_FORMAT") || !strings.Contains(errs, "PRIVO_LISTEN") {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
}

func TestHealthcheckReadsTheListenAddressFromTheFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p := writeYAML(t, "listen: \"127.0.0.1:"+port+"\"\n")
	var out, errb bytes.Buffer
	if code := run([]string{"healthcheck"}, &out, &errb, envMap(map[string]string{"PRIVO_CONFIG_FILE": p})); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}
