// Package buildinfo exposes version information stamped at build time.
package buildinfo

import "runtime"

// Set with -ldflags "-X privo.club/privo/internal/buildinfo.Version=..." at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
}

// Get returns the build information of the running binary.
func Get() Info {
	return Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version()}
}
