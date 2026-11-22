// Package buildinfo says which version of Privo is running.
//
// The version is a semantic version (https://semver.org/) kept in the VERSION
// file next to this one and compiled into the binary, so every build reports
// it, whether it came from `make build`, the Dockerfile or a deploy on
// Coolify that has no git history and passes no build arguments. See
// docs/site/development/versioning.md for when it changes.
package buildinfo

import (
	_ "embed"
	"runtime"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version is the semantic version of this build, without a "v" prefix.
var Version = strings.TrimSpace(versionFile)

// Info describes the running binary. It is what the unauthenticated /version
// endpoint and `privo version` print, so it holds nothing an outsider could
// use to find a particular build, such as a commit hash.
type Info struct {
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
}

// Get returns the build information of the running binary.
func Get() Info {
	return Info{Version: Version, GoVersion: runtime.Version()}
}
