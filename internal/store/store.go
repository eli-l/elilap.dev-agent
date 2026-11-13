// Package store owns the SQLite index: connection setup (sqlite-vec loaded
// before any vec0 DDL runs), the migration runner, chunk writes kept in sync
// with the FTS5 mirror, and the ingest ledger from migrations/0002_state.sql.
//
// Conventions enforced here, per AGENTS.md and PLAN.md section 3:
//   - chunks and chunks_fts are only ever written inside one transaction
//     (ReplaceItemChunks / DeleteItem);
//   - chunk_id is sha256(source_kind|source_id|chunk_seq) hex, computed by
//     internal/sources/shared, never assigned by callers;
//   - MaxOpenConns(1) keeps concurrent ingest and embed from tripping
//     SQLite's single-writer lock.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

func init() {
	// Load sqlite-vec into every connection opened from now on. This must
	// happen before migrations/0001_init.sql is applied: the chunks_vec vec0
	// DDL fails unless the extension is loaded first.
	vec.Auto()
}

// DefaultEmbeddingDims is the vector width chunks_vec.embedding has when
// EMBED_DIMS is unset: 768, from the local embeddinggemma-300m model. The
// schema in migrations/0001_init.sql declares that width; Open rebuilds the
// table when a run is configured for a model that returns another one.
const DefaultEmbeddingDims = 768

// Store is a handle to the single-file SQLite index.
type Store struct {
	db *sql.DB
	// dims is the width of chunks_vec.embedding in this index. A read-only
	// open takes it from the schema; a read-write open sets it from the
	// configured width (rebuilding the table when they disagree).
	dims int
}

// Dims reports the vector width this index stores; 0 means the index has no
// chunks_vec table (a database that never finished ingesting).
func (s *Store) Dims() int { return s.dims }

// Open opens (creating if necessary) the index database at path and brings
// its schema up to date by applying every migration in migrationsDir. dims is
// the configured embedding width (EMBED_DIMS); a non-zero width different from
// the one chunks_vec already has rebuilds that table, which invalidates every
// stored vector and makes the next `agent embed` re-embed the corpus.
func Open(path, migrationsDir string, dims int) (*Store, error) {
	dims = normalizeDims(dims)
	if dims > maxEmbeddingDims {
		return nil, fmt.Errorf("embedding width %d exceeds the supported maximum %d",
			dims, maxEmbeddingDims)
	}
	log := logging.DB().With().Str("db", path).Logger()
	start := time.Now()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Error().Err(err).Str("dir", dir).Msg("create database directory failed")
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := "file:" + path + "?_fk=1&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		log.Error().Err(err).Msg("open database failed")
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// SQLite is happiest with a single writer; capping the pool keeps
	// concurrent ingest and embed runs from fighting over the write lock.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dims: dims}
	if err := s.migrate(migrationsDir); err != nil {
		db.Close()
		log.Error().Err(err).Str("migrations", migrationsDir).Msg("migration failed")
		return nil, err
	}
	if err := s.ensureVecDims(dims); err != nil {
		db.Close()
		log.Error().Err(err).Int("dims", dims).Msg("vector table check failed")
		return nil, err
	}
	log.Info().
		Str("migrations", migrationsDir).
		Int("embed_dims", dims).
		Dur("duration", time.Since(start)).
		Msg("database ready")
	return s, nil
}

// ErrNoDatabase reports that the index file does not exist. Read-only commands
// surface it instead of creating an empty database.
var ErrNoDatabase = errors.New("index database does not exist")

// OpenReadOnly opens an existing index without creating it or applying
// migrations. It is the path for read-only commands (stats, ingest --dry-run):
// a missing file is an error rather than a silently created empty schema. The
// vector width comes from the index itself, so a width mismatch is reported by
// CheckDims at the call site that actually needs to compare vectors.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoDatabase, path)
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	dsn := "file:" + path + "?mode=ro&_fk=1&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s (read-only): %w", path, err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	dims, err := s.currentVecDims()
	if err != nil {
		db.Close()
		return nil, err
	}
	s.dims = dims
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	log := logging.DB()
	log.Debug().Msg("closing database")
	return s.db.Close()
}

// migrate applies every *.sql migration in migrationsDir in filename order
// that has not been applied yet, each inside its own transaction. Applied
// names are recorded in schema_migrations so re-opening is a no-op.
func (s *Store) migrate(migrationsDir string) error {
	if err := s.db.Ping(); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
  name TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := make(map[string]bool)
	rows, err := s.db.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	names, err := listMigrations(migrationsDir)
	if err != nil {
		return err
	}
	log := logging.DB()
	for _, name := range names {
		if applied[name] {
			log.Trace().Str("migration", name).Msg("migration already applied")
			continue
		}
		script, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(script)); err != nil {
			tx.Rollback()
			log.Error().Err(err).Str("migration", name).Msg("apply migration failed")
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at)
VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, name); err != nil {
			tx.Rollback()
			log.Error().Err(err).Str("migration", name).Msg("record migration failed")
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			log.Error().Err(err).Str("migration", name).Msg("commit migration failed")
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		log.Info().Str("migration", name).Int("bytes", len(script)).Msg("applied migration")
	}
	return nil
}
