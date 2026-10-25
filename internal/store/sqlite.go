// Package store owns the SQLite index: the schema, the migrations, and the
// writes and reads that keep chunks, their full-text rows, and their
// embeddings in step.
//
// The driver choice is deliberate and was settled by a spike, not by
// preference:
//
//   - github.com/ncruces/go-sqlite3 is pure Go (SQLite compiled to
//     WebAssembly). No CGO, so a build needs no C toolchain and the binary
//     cross-compiles for linux/amd64, linux/arm64, and darwin/arm64 with
//     CGO_ENABLED=0.
//   - FTS5 comes from the same project's ext/fts5 extension. It is not
//     compiled into the driver, so it is registered explicitly below.
//   - Embeddings live in a plain BLOB column, not in a sqlite-vec "vec0"
//     virtual table. sqlite-vec ships as a platform-specific loadable
//     extension, which would put a native .so next to every deployed
//     binary. The corpus this agent indexes is small (see PLAN.md
//     Decision 2), so cosine similarity over the stored vectors is
//     computed in Go instead - the same work, no native dependency.
//
// See PLAN.md Decision 2b and docs/store.md for the full rationale.
package store

import (
	"database/sql"
	"fmt"

	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" database/sql driver
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// driverName is the database/sql driver name registered by
// github.com/ncruces/go-sqlite3/driver.
const driverName = "sqlite3"

func init() {
	// AutoExtension applies the FTS5 module to every connection this
	// process opens, so no call site has to remember to register it.
	sqlite3.AutoExtension(fts5.Register)
}

// open opens the index at path with the pragmas the agent relies on:
// WAL journaling (the index is written while it is read), foreign keys on
// (the vector table references chunks), and a busy timeout so a concurrent
// writer waits instead of failing.
func open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open index %s: %w", path, err)
	}
	// SQLite tolerates a single writer; keep the pool small so two
	// processes do not fight over the write lock.
	db.SetMaxOpenConns(1)
	return db, nil
}
