package buildinfo

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// semver is the grammar of https://semver.org/ (2.0.0), without a "v" prefix.
var semver = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(-((0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(\.(0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(\+([0-9a-zA-Z-]+(\.[0-9a-zA-Z-]+)*))?$`)

// The version of a running binary is the semantic version in the VERSION file,
// not a commit hash and not "dev": a build on Coolify has no git history and no
// build arguments, and /version must still say something an operator can read.
func TestVersionIsSemver(t *testing.T) {
	if !semver.MatchString(Version) {
		t.Fatalf("Version = %q, want a semantic version such as 1.2.3", Version)
	}
	raw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != Version {
		t.Errorf("Version = %q but VERSION says %q", Version, got)
	}
	if strings.Count(string(raw), "\n") > 1 || strings.TrimRight(string(raw), "\n") != strings.TrimSpace(string(raw)) {
		t.Errorf("VERSION must be one line without spaces: %q", raw)
	}
}

func TestSemverGrammar(t *testing.T) {
	for _, ok := range []string{"0.1.0", "1.0.0", "10.20.30", "1.0.0-rc.1", "1.0.0+build.5", "1.0.0-beta+exp.sha.5114f85"} {
		if !semver.MatchString(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", "dev", "v1.0.0", "1.0", "01.0.0", "1.0.0-", "a1b2c3d", "1.0.0 ", "1.2.3.4"} {
		if semver.MatchString(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// What /version and `privo version` show: the version and the Go that built it.
// A commit hash is not part of it.
func TestGetShape(t *testing.T) {
	raw, err := json.Marshal(Get())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["version"] != Version || !strings.HasPrefix(m["go_version"], "go") {
		t.Fatalf("Get() = %s, want exactly version and go_version", raw)
	}
}
