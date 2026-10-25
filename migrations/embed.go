// Package migrations holds the SQL schema of the index and embeds it into
// the binary.

// Embedding the schema is what makes the index reproducible from the binary
// alone: `rm index.db` and a fresh run rebuilds it, with no schema file to
// ship beside the executable and no "the migrations live somewhere else"
// failure mode. The files are applied in file-name order, so the numeric
// prefix (0001_, 0002_, ...) is the version order.
package migrations

import (
	"embed"
	"fmt"
	"path"
	"sort"
)

//go:embed *.sql
var files embed.FS

// Migration is one schema file. Name is the file name and the migration's
// identity: it is what the store records in schema_migrations, so a file
// must never be renamed after it has been applied - a renamed file is a new
// migration, not a renamed one.
type Migration struct {
	// Name is the file name, e.g. "0001_init.sql".
	Name string
	// SQL is the file's full contents.
	SQL string
}

// All returns every embedded migration in the order it must be applied.
func All() ([]Migration, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read the embedded migrations: %w", err)
	}

	out := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		data, err := files.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read the embedded migration %s: %w", entry.Name(), err)
		}
		out = append(out, Migration{Name: entry.Name(), SQL: string(data)})
	}

	// ReadDir returns entries sorted by name already; sort explicitly so the
	// order is a property of this function and not of the embed package.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations are embedded in the binary: the schema is missing from the build")
	}
	return out, nil
}
