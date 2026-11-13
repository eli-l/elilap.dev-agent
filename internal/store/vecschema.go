package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// maxEmbeddingDims bounds the accepted vector width. sqlite-vec stores the
// vector inline, so a typo such as EMBED_DIMS=76800 would make every KNN scan
// enormous rather than fail; the ceiling keeps a configuration mistake a
// startup error.
const maxEmbeddingDims = 16384

// ErrVecDims reports that the index's vector width and the configured
// EMBED_DIMS disagree. Vectors from two models are not comparable, so the
// index has to be re-embedded (which rebuilds chunks_vec) instead of queried.
var ErrVecDims = errors.New("index vector dimensions do not match EMBED_DIMS")

// vecDimsPattern extracts N from the `embedding float[N]` column in the
// chunks_vec DDL that sqlite_master records.
var vecDimsPattern = regexp.MustCompile(`(?i)embedding\s+float\s*\[\s*(\d+)\s*\]`)

// normalizeDims treats the zero value as the documented width, so a caller
// with no opinion (a test, or a configuration that never set EMBED_DIMS) gets
// DefaultEmbeddingDims instead of a store that refuses every vector.
func normalizeDims(dims int) int {
	if dims <= 0 {
		return DefaultEmbeddingDims
	}
	return dims
}

// CheckDims fails with ErrVecDims when this index does not store vectors of
// the configured width. Read-only commands call it before serving a query, so
// a stale embed is a clear startup error instead of a dimension mismatch deep
// inside retrieval.
func (s *Store) CheckDims(dims int) error {
	want := normalizeDims(dims)
	if s.dims == want {
		return nil
	}
	return fmt.Errorf("%w: index has %d-dimensional vectors, EMBED_DIMS=%d; run `agent embed` to rebuild and re-embed",
		ErrVecDims, s.dims, want)
}

// currentVecDims reads the width of chunks_vec from the schema, or 0 when the
// table does not exist yet.
func (s *Store) currentVecDims() (int, error) {
	var ddl sql.NullString
	err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chunks_vec'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read chunks_vec schema: %w", err)
	}
	m := vecDimsPattern.FindStringSubmatch(ddl.String)
	if m == nil {
		return 0, fmt.Errorf("read chunks_vec schema: no embedding width in %q", ddl.String)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("read chunks_vec schema: bad embedding width %q", m[1])
	}
	return n, nil
}

// ensureVecDims makes chunks_vec store vectors of exactly dims values,
// rebuilding the table when the width changed. Rebuilding is destructive by
// design: a vector from another model is not comparable, so the stored ones
// are dropped along with their embedded_with stamps and the next
// `agent embed` treats every chunk as pending.
func (s *Store) ensureVecDims(dims int) error {
	current, err := s.currentVecDims()
	if err != nil {
		return err
	}
	if current == dims {
		return nil
	}

	log := logging.DB().With().Int("from", current).Int("to", dims).Logger()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("rebuild chunks_vec: begin: %w", err)
	}
	defer tx.Rollback()

	// A vec0 table owns shadow tables; dropping the virtual table drops them
	// with it, so the CREATE below starts from nothing.
	if _, err := tx.Exec(`DROP TABLE IF EXISTS chunks_vec`); err != nil {
		return fmt.Errorf("rebuild chunks_vec: drop: %w", err)
	}
	if _, err := tx.Exec(createVecTable(dims)); err != nil {
		return fmt.Errorf("rebuild chunks_vec: create: %w", err)
	}
	// The vectors are gone, so the stamps describe rows that no longer have
	// one. Clearing them keeps "pending" honest and stops a stale tag from
	// looking current under the new width.
	if _, err := tx.Exec(`UPDATE chunks SET embedded_with = NULL, embedded_at = NULL`); err != nil {
		return fmt.Errorf("rebuild chunks_vec: clear stamps: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rebuild chunks_vec: commit: %w", err)
	}
	log.Warn().Msg("rebuilt chunks_vec for the configured embedding width; run `agent embed` to re-embed")
	return nil
}

// createVecTable is the vec0 DDL for one embedding width. It mirrors the
// default-width table in migrations/0001_init.sql; the store rebuilds it here
// because a SQL migration file cannot carry a runtime width.
func createVecTable(dims int) string {
	return fmt.Sprintf(`CREATE VIRTUAL TABLE chunks_vec USING vec0(
  chunk_id TEXT PRIMARY KEY,
  embedding float[%d]
)`, dims)
}
