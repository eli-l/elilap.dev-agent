package store

import (
	"context"
	"fmt"

	"github.com/eli-l/elilap.dev-agent/migrations"
)

// schemaMigrationsDDL is the ledger of applied migrations. It is created by
// the store itself rather than by a migration file: the ledger has to exist
// before the first migration can be recorded in it.
const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  name       TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
)`

// applyMigrations applies every embedded migration that has not been
// applied yet, and records it in schema_migrations.
//
// All pending migrations are applied inside one transaction, so the schema
// moves from one consistent version to the next or not at all: a migration
// that fails leaves the index exactly as it was, and the error names the
// file that failed and the SQLite error that stopped it.
//
// Re-opening an index applies nothing: the ledger already names the files.
func (s *Store) applyMigrations(ctx context.Context, all []migrations.Migration) error {
	if _, err := s.db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create the schema_migrations table: %w", err)
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	var pending []migrations.Migration
	for _, m := range all {
		if applied[m.Name] {
			continue
		}
		pending = append(pending, m)
	}
	if len(pending) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the migration transaction: %w", err)
	}
	defer tx.Rollback()

	for _, m := range pending {
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			m.Name, nowUTC(),
		); err != nil {
			return fmt.Errorf("record migration %s in schema_migrations: %w", m.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the migrations: %w", err)
	}
	return nil
}

// appliedMigrations returns the set of migration files already applied to
// this index.
func (s *Store) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read schema_migrations: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}
