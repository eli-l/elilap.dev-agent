package store

import (
	"database/sql"
	"fmt"
	"time"

	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// PendingChunk is one chunk the embed step still has to (re-)vectorize. The
// fields are the text inputs of the embedding, so the caller composes the
// exact string without a second query.
type PendingChunk struct {
	ChunkID string
	// ContentHash is the hash of the text the vector is built from. It is
	// carried through the write so a rewrite that keeps the same positional
	// chunk_id cannot stamp a vector computed from the old body as current.
	ContentHash string
	Title       string
	Section     string
	Body        string
}

// ChunkEmbedding pairs a chunk id with its vector for one write batch.
type ChunkEmbedding struct {
	ChunkID     string
	ContentHash string
	Vector      []float32
}

// pendingEmbeddingsQuery selects chunks without a current embedding. A chunk
// is pending when it was never embedded, when it was embedded by a different
// model/text version, or when its vec row is missing (for example after a
// rebuild of chunks_vec). The LEFT JOIN is what makes the vec table the
// authority on "has a vector", not just the bookkeeping column.
const pendingEmbeddingsQuery = `
SELECT c.chunk_id, c.content_hash, COALESCE(c.title, ''), COALESCE(c.section, ''), c.body
FROM chunks c
LEFT JOIN chunks_vec v ON v.chunk_id = c.chunk_id
WHERE (c.embedded_with IS NULL OR c.embedded_with <> ? OR v.chunk_id IS NULL)
  AND c.chunk_id > ?
ORDER BY c.chunk_id`

// PendingEmbeddings returns up to limit chunks that need an embedding under
// tag, in chunk_id order, starting after the given id. The cursor lets a
// caller page through the corpus without OFFSET, and it is what keeps
// `embed --full` from selecting the same first batch forever. A non-positive
// limit means no limit. Full mode returns every chunk, ignoring the
// embedded_with tag and vec rows, which is what `embed --full` needs to
// re-vectorize the whole corpus after a model or text-template change.
func (s *Store) PendingEmbeddings(tag string, limit int, full bool, after string) ([]PendingChunk, error) {
	q := pendingEmbeddingsQuery
	args := []any{tag, after}
	if full {
		q = `
SELECT c.chunk_id, c.content_hash, COALESCE(c.title, ''), COALESCE(c.section, ''), c.body
FROM chunks c
WHERE c.chunk_id > ?
ORDER BY c.chunk_id`
		args = []any{after}
	}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list pending embeddings: %w", err)
	}
	defer rows.Close()
	var out []PendingChunk
	for rows.Next() {
		var p PendingChunk
		if err := rows.Scan(&p.ChunkID, &p.ContentHash, &p.Title, &p.Section, &p.Body); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPendingEmbeddings reports how many chunks PendingEmbeddings would
// return for the same arguments, without loading their bodies.
func (s *Store) CountPendingEmbeddings(tag string, full bool) (int, error) {
	q := `SELECT COUNT(*) FROM chunks c
LEFT JOIN chunks_vec v ON v.chunk_id = c.chunk_id
WHERE (c.embedded_with IS NULL OR c.embedded_with <> ? OR v.chunk_id IS NULL)`
	args := []any{tag}
	if full {
		q = `SELECT COUNT(*) FROM chunks`
		args = nil
	}
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending embeddings: %w", err)
	}
	return n, nil
}

// WriteEmbeddings stores one batch of vectors and stamps its chunks in one
// transaction. A chunk that disappeared between selection and writing is
// skipped rather than resurrected as an orphan vector, so a concurrent
// ingest cannot leave chunks_vec rows without a chunks row.
//
// The vec0 table rejects INSERT OR REPLACE on its primary key, so each row is
// an explicit DELETE followed by an INSERT.
func (s *Store) WriteEmbeddings(rows []ChunkEmbedding, tag string, at time.Time) (written, skipped int, err error) {
	if len(rows) == 0 {
		return 0, 0, nil
	}
	if tag == "" {
		return 0, 0, fmt.Errorf("write embeddings: tag is required")
	}
	stamp := at.UTC().Format(time.RFC3339)

	// Serialize before opening the transaction: a bad vector must fail without
	// holding SQLite's single write lock.
	serialized := make([][]byte, len(rows))
	seen := make(map[string]bool, len(rows))
	for i, row := range rows {
		if row.ChunkID == "" {
			return 0, 0, fmt.Errorf("write embeddings: empty chunk_id at position %d", i)
		}
		if seen[row.ChunkID] {
			return 0, 0, fmt.Errorf("write embeddings: duplicate chunk_id %s in batch", row.ChunkID)
		}
		seen[row.ChunkID] = true
		if row.ContentHash == "" {
			return 0, 0, fmt.Errorf("write embeddings: chunk %s has no content hash", row.ChunkID)
		}
		if s.dims <= 0 {
			return 0, 0, fmt.Errorf("write embeddings: index has no chunks_vec table")
		}
		if len(row.Vector) != s.dims {
			return 0, 0, fmt.Errorf("write embeddings: chunk %s has %d dimensions, index wants %d",
				row.ChunkID, len(row.Vector), s.dims)
		}
		b, err := vec.SerializeFloat32(row.Vector)
		if err != nil {
			return 0, 0, fmt.Errorf("serialize vector for %s: %w", row.ChunkID, err)
		}
		serialized[i] = b
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin embeddings write: %w", err)
	}
	defer tx.Rollback()

	for i, row := range rows {
		// The content hash guard is what makes a concurrent item rewrite safe:
		// chunk_id is positional, so an ingest can replace the body between
		// selection and write. The UPDATE then matches nothing and the stale
		// vector is dropped instead of stamped as current.
		res, err := tx.Exec(`UPDATE chunks SET embedded_with = ?, embedded_at = ?
WHERE chunk_id = ? AND content_hash = ?`, tag, stamp, row.ChunkID, row.ContentHash)
		if err != nil {
			return 0, 0, fmt.Errorf("stamp chunk %s: %w", row.ChunkID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, 0, fmt.Errorf("stamp chunk %s: %w", row.ChunkID, err)
		}
		if n == 0 {
			skipped++
			continue
		}
		if _, err := tx.Exec(`DELETE FROM chunks_vec WHERE chunk_id = ?`, row.ChunkID); err != nil {
			return 0, 0, fmt.Errorf("clear vector for %s: %w", row.ChunkID, err)
		}
		if _, err := tx.Exec(`INSERT INTO chunks_vec (chunk_id, embedding) VALUES (?, ?)`,
			row.ChunkID, serialized[i]); err != nil {
			return 0, 0, fmt.Errorf("insert vector for %s: %w", row.ChunkID, err)
		}
		written++
	}

	log := logging.DB()
	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("commit embeddings write failed")
		return 0, 0, fmt.Errorf("commit embeddings write: %w", err)
	}
	log.Debug().
		Int("written", written).
		Int("skipped", skipped).
		Str("tag", tag).
		Msg("wrote embedding batch")
	return written, skipped, nil
}

// clearItemVectors removes the vec rows of the given chunk ids. It is called
// inside the same transaction as the chunk rewrite: a replaced or deleted
// chunk must never leave a stale vector behind, or KNN would return an id
// with no readable row.
func clearItemVectors(tx *sql.Tx, ids []string) error {
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM chunks_vec WHERE chunk_id = ?`, id); err != nil {
			return fmt.Errorf("delete vector row %s: %w", id, err)
		}
	}
	return nil
}
