package store

import "io/fs"

// migrationFiles is the embedded migrations directory.
func migrationFiles() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
