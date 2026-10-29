package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The registry, the code and the documents must agree. These tests are what
// makes "add a setting" a three-step job that cannot be half done (ADR 0015).

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root not found at %s: %v", root, err)
	}
	return root
}

func TestSettingKey(t *testing.T) {
	for name, want := range map[string]string{
		"PRIVO_LISTEN":          "listen",
		"PRIVO_BACKUP_INTERVAL": "backup_interval",
		"PRIVO_MASTER_KEY_FILE": "master_key_file",
	} {
		if got := (Setting{Name: name}).Key(); got != want {
			t.Errorf("Key(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestRegistryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Settings {
		if !regexp.MustCompile(`^PRIVO_[A-Z0-9_]+$`).MatchString(s.Name) {
			t.Errorf("bad name %q", s.Name)
		}
		if seen[s.Name] {
			t.Errorf("%s registered twice", s.Name)
		}
		seen[s.Name] = true
		if s.Secret && !seen[s.Name+"_FILE"] {
			// The file form must exist, or the error message has nothing to point to.
			found := false
			for _, o := range Settings {
				if o.Name == s.Name+"_FILE" {
					found = true
				}
			}
			if !found {
				t.Errorf("secret %s has no %s_FILE setting", s.Name, s.Name)
			}
		}
	}
	if _, ok := Lookup("PRIVO_LISTEN"); !ok {
		t.Error("Lookup does not find PRIVO_LISTEN")
	}
	if _, ok := Lookup("PRIVO_NOPE"); ok {
		t.Error("Lookup found a setting that is not registered")
	}
}

// sourceNames returns the PRIVO_ names written as string literals in the
// non-test Go code of the module, outside the registry file itself.
func sourceNames(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	re := regexp.MustCompile(`"(PRIVO_[A-Z0-9_]+)"`)
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "site", "test", "web":
				return filepath.SkipDir
			}
			if strings.HasPrefix(d.Name(), "_backup") || strings.HasPrefix(d.Name(), "_to_delete") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || rel == filepath.Join("internal", "config", "settings.go") {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // the repository's own sources
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = rel
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestEveryVariableInTheCodeIsRegistered(t *testing.T) {
	names := sourceNames(t)
	if len(names) < 15 {
		t.Fatalf("the scan found only %d names; it is not looking in the right place", len(names))
	}
	for name, file := range names {
		if _, ok := Lookup(name); ok {
			continue
		}
		// A key read as name and name+"_FILE" appears in the code once, as the base.
		if _, ok := Lookup(name + "_FILE"); ok {
			continue
		}
		t.Errorf("%s (in %s) is read by the code but is not in config.Settings (internal/config/settings.go)", name, file)
	}
}

func TestEveryRegisteredSettingIsUsedByTheCode(t *testing.T) {
	names := sourceNames(t)
	for _, s := range Settings {
		base := strings.TrimSuffix(s.Name, "_FILE")
		if _, ok := names[s.Name]; ok {
			continue
		}
		if _, ok := names[base]; ok && base != s.Name {
			continue
		}
		t.Errorf("%s is registered but no code reads it; remove it from config.Settings", s.Name)
	}
}

func TestRegistryIsInTheREADME(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Settings {
		if !strings.Contains(string(b), "`"+s.Name+"`") {
			t.Errorf("%s is not in the README settings table", s.Name)
		}
	}
}

func exampleFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "privo.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExampleFileListsEverySetting(t *testing.T) {
	ex := exampleFile(t)
	for _, s := range Settings {
		if s.Secret || s.NoFile {
			if strings.Contains(ex, "\n#"+s.Key()+":") || strings.Contains(ex, "\n"+s.Key()+":") {
				t.Errorf("the example offers %s, which must not be set in the file", s.Key())
			}
			continue
		}
		if !strings.Contains(ex, "\n#"+s.Key()+":") {
			t.Errorf("docs/privo.example.yaml has no commented line for %s (write it as #%s: value)", s.Key(), s.Key())
		}
	}
}

func TestExampleFileParsesWhenEverySettingIsOn(t *testing.T) {
	re := regexp.MustCompile(`(?m)^#([a-z_]+:.*)$`)
	on := re.ReplaceAllString(exampleFile(t), "$1")
	p := writeConfig(t, on)
	got, err := ReadFile(p)
	if err != nil {
		t.Fatalf("the example with every setting uncommented does not parse: %v", err)
	}
	for _, s := range Settings {
		if s.Secret || s.NoFile {
			continue
		}
		if _, ok := got[s.Name]; !ok {
			t.Errorf("%s has no value in the example", s.Name)
		}
	}
	// The untouched example sets nothing: it is safe to start from.
	none, err := ReadFile(writeConfig(t, exampleFile(t)))
	if err != nil || len(none) != 0 {
		t.Errorf("the example as shipped sets %v (error %v), want nothing", none, err)
	}
}
