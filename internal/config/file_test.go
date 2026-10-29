package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "privo.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFileValues(t *testing.T) {
	p := writeConfig(t, `# the gateway
listen: ":9090"
data_dir: /var/lib/privo   # where the database lives
backup_keep: 3
log_format: text
insecure_local_runner: true
approval_hold: 45s
`)
	got, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := FileValues{
		"PRIVO_LISTEN":                ":9090",
		"PRIVO_DATA_DIR":              "/var/lib/privo",
		"PRIVO_BACKUP_KEEP":           "3",
		"PRIVO_LOG_FORMAT":            "text",
		"PRIVO_INSECURE_LOCAL_RUNNER": "true",
		"PRIVO_APPROVAL_HOLD":         "45s",
	}
	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestReadFileEmptyAndNullAreNotErrors(t *testing.T) {
	for name, content := range map[string]string{
		"empty file":       "",
		"only comments":    "# nothing set\n",
		"empty document":   "---\n",
		"explicit mapping": "{}\n",
	} {
		got, err := ReadFile(writeConfig(t, content))
		if err != nil || len(got) != 0 {
			t.Errorf("%s: values %v, error %v, want none", name, got, err)
		}
	}
	// A key with no value is the same as no key: it lets an operator blank a line.
	got, err := ReadFile(writeConfig(t, "listen:\nbackup_keep: ~\ndata_dir: /d\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["PRIVO_DATA_DIR"] != "/d" {
		t.Errorf("values = %v, want only data_dir", got)
	}
}

func TestReadFileMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.yaml")
	_, err := ReadFile(p)
	if err == nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v, want a not-exist error naming %s", err, p)
	}
}

func TestReadFileMustBeRegularAndSmall(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadFile(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory: err = %v, want not a regular file", err)
	}
	big := writeConfig(t, "listen: \":1\"\n"+strings.Repeat("# padding\n", 10000))
	if _, err := ReadFile(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("big file: err = %v, want a size error", err)
	}
}

func TestReadFileIsStrict(t *testing.T) {
	cases := []struct {
		name, content string
		want          []string // every one must appear in the error
	}{
		{"unknown key", "lisen: \":1\"\n", []string{`unknown setting "lisen"`}},
		{"upper case key", "LISTEN: \":1\"\n", []string{`unknown setting "LISTEN"`}},
		{"env style key", "PRIVO_LISTEN: \":1\"\n", []string{`unknown setting "PRIVO_LISTEN"`}},
		{"merge key", "<<: {listen: \":1\"}\n", []string{`unknown setting "<<"`}},
		{"duplicate key", "listen: \":1\"\nlisten: \":2\"\n", []string{`"listen"`, "twice"}},
		{"list value", "listen: [\":1\", \":2\"]\n", []string{`"listen"`, "single value"}},
		{"map value", "listen: {a: b}\n", []string{`"listen"`, "single value"}},
		{"alias", "data_dir: &d /x\nbackup_dir: *d\n", []string{`"backup_dir"`, "alias"}},
		{"two documents", "listen: \":1\"\n---\nlisten: \":2\"\n", []string{"more than one document"}},
		{"not a mapping", "- listen\n- data_dir\n", []string{"mapping"}},
		{"not yaml", "listen: [unterminated\n", []string{"yaml"}},
		{"config_file in the file", "config_file: /x.yaml\n", []string{`"config_file"`, "cannot be set in the file"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeConfig(t, c.content)
			_, err := ReadFile(p)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), p) {
				t.Errorf("error does not name the file: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestReadFileReportsEverythingAtOnce(t *testing.T) {
	_, err := ReadFile(writeConfig(t, "lisen: a\ndata_dr: b\nbackup_keep: [1]\n"))
	if err == nil {
		t.Fatal("no error")
	}
	for _, w := range []string{`"lisen"`, `"data_dr"`, `"backup_keep"`} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q lacks %s", err, w)
		}
	}
}

func TestReadFileRefusesSecretsAndNeverRepeatsValues(t *testing.T) {
	for key, hint := range map[string]string{
		"master_key":          "master_key_file",
		"master_key_previous": "master_key_previous_file",
		"bootstrap_token":     "bootstrap_token_file",
	} {
		const secret = "S3CRET-VALUE-must-not-appear"
		_, err := ReadFile(writeConfig(t, key+": "+secret+"\n"))
		if err == nil {
			t.Fatalf("%s accepted in the file", key)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error repeats the value: %v", key, err)
		}
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("%s: the error does not name %s: %v", key, hint, err)
		}
	}
	// A value on a rejected line is never echoed either.
	_, err := ReadFile(writeConfig(t, "lisen: S3CRET-VALUE-must-not-appear\n"))
	if err == nil || strings.Contains(err.Error(), "S3CRET") {
		t.Errorf("unknown key error: %v", err)
	}
}

func TestReadFileAllowsSecretFilePaths(t *testing.T) {
	got, err := ReadFile(writeConfig(t, "master_key_file: /run/secrets/k\nmaster_key_previous_file: /run/secrets/old\nbootstrap_token_file: /run/secrets/t\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got["PRIVO_MASTER_KEY_FILE"] != "/run/secrets/k" || got["PRIVO_MASTER_KEY_PREVIOUS_FILE"] != "/run/secrets/old" || got["PRIVO_BOOTSTRAP_TOKEN_FILE"] != "/run/secrets/t" {
		t.Errorf("values = %v", got)
	}
}

func TestLayerPrecedence(t *testing.T) {
	env := map[string]string{"PRIVO_LISTEN": ":1", "PRIVO_DATA_DIR": "   ", "PRIVO_LOG_FORMAT": ""}
	file := FileValues{"PRIVO_LISTEN": ":2", "PRIVO_DATA_DIR": "/f", "PRIVO_LOG_FORMAT": "text", "PRIVO_BACKUP_KEEP": "5"}
	get := Layer(func(k string) string { return env[k] }, file)
	for name, want := range map[string]string{
		"PRIVO_LISTEN":      ":1",   // the environment wins
		"PRIVO_DATA_DIR":    "/f",   // a blank variable is unset
		"PRIVO_LOG_FORMAT":  "text", // so is an empty one
		"PRIVO_BACKUP_KEEP": "5",    // only in the file
		"PRIVO_RUNNER":      "",     // nowhere
	} {
		if got := get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// Names the file cannot hold still come from the environment.
	env["PRIVO_MASTER_KEY"] = "from-env"
	if got := get("PRIVO_MASTER_KEY"); got != "from-env" {
		t.Errorf("secret from the environment = %q", got)
	}
	// With no file the lookup is the environment.
	if got := Layer(func(k string) string { return env[k] }, nil)("PRIVO_LISTEN"); got != ":1" {
		t.Errorf("no file: %q", got)
	}
}

func TestLoadReadsValuesFromTheFile(t *testing.T) {
	file := FileValues{"PRIVO_LISTEN": "127.0.0.1:9191", "PRIVO_BACKUP_KEEP": "3", "PRIVO_BACKUP_INTERVAL": "12h", "PRIVO_LOG_FORMAT": "text"}
	cfg, err := Load(Layer(func(string) string { return "" }, file))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:9191" || cfg.BackupKeep != 3 || cfg.LogFormat != "text" || cfg.BackupInterval.Hours() != 12 {
		t.Errorf("cfg = %+v", cfg)
	}
	// A bad value in the file is reported by the usual validation, naming the variable.
	_, err = Load(Layer(func(string) string { return "" }, FileValues{"PRIVO_BACKUP_KEEP": "many"}))
	if err == nil || !strings.Contains(err.Error(), "PRIVO_BACKUP_KEEP") {
		t.Errorf("err = %v, want PRIVO_BACKUP_KEEP named", err)
	}
}

func TestDescribeSources(t *testing.T) {
	env := map[string]string{"PRIVO_LISTEN": ":1", "PRIVO_MASTER_KEY": "TOPSECRET", "PRIVO_RUNNER": "  "}
	file := FileValues{"PRIVO_DATA_DIR": "/f", "PRIVO_RUNNER": "local"}
	rows := Describe(func(k string) string { return env[k] }, file)
	if len(rows) != len(Settings) {
		t.Fatalf("%d rows for %d settings", len(rows), len(Settings))
	}
	by := map[string]Entry{}
	for _, r := range rows {
		by[r.Name] = r
	}
	check := func(name, value, source string) {
		t.Helper()
		if r := by[name]; r.Value != value || r.Source != source {
			t.Errorf("%s = %+v, want value %q source %q", name, r, value, source)
		}
	}
	check("PRIVO_LISTEN", ":1", "environment")
	check("PRIVO_DATA_DIR", "/f", "file")
	check("PRIVO_RUNNER", "local", "file") // a blank variable is unset
	check("PRIVO_LOG_FORMAT", "", "default")
	check("PRIVO_MASTER_KEY", "set", "environment") // never the value
	check("PRIVO_BOOTSTRAP_TOKEN", "unset", "default")
	for _, r := range rows {
		if strings.Contains(r.Value, "TOPSECRET") {
			t.Errorf("a secret value reached the description: %+v", r)
		}
	}
}
