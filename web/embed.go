// Package webapp holds the admin app's build (ADR 0019). The source is in this
// directory (Preact, Vite, TypeScript); `make ui` rebuilds dist/, which is
// committed, so building the Go binary never needs Node.
package webapp

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built app: index.html and assets/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern above guarantees the directory
	}
	return sub
}
