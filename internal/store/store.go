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
//     compiled into the driver, so it is registered explicitly in sqlite.go.
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
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/migrations"
)

// defaultSearchLimit is the number of hits a search returns when the caller
// does not ask for a specific number.
const defaultSearchLimit = 10

// source kinds accepted by the chunks table's CHECK constraint.
const (
	sourceKindGhostPost  = "ghost_post"
	sourceKindPostizPost = "postiz_post"
)

// Store is an open index. It is safe for concurrent use: the underlying
// database/sql pool is capped at a single connection, because SQLite has one
// writer and a second connection would only queue behind it.
type Store struct {
	db   *sql.DB
	path string
}

// Chunk is one indexed passage of one upstream item.
//
// The chunk id is supplied by the caller, not generated here: it is
// sha256(source_kind|source_id|chunk_seq) in hex, so the ingest pipeline
// owns chunk identity and the store never guesses it.
type Chunk struct {
	// ChunkID is the chunk's identity, sha256(source_kind|source_id|chunk_seq) hex.
	ChunkID string
	// SourceKind is "ghost_post" or "postiz_post".
	SourceKind string
	// SourceID is the upstream item id (Ghost uuid / Postiz id).
	SourceID string
	// SourceURL is the item's canonical URL.
	SourceURL string
	// Platform is the social platform for a cross-post; empty for a Ghost post.
	Platform string
	// Author is the item's author.
	Author string
	// PublishedAt is the upstream publication time, ISO-8601 UTC.
	PublishedAt string
	// UpdatedAt is the upstream modification time, ISO-8601 UTC.
	UpdatedAt string
	// ContentHash is the hash of the normalized chunk text.
	ContentHash string
	// Title is the chunk's title.
	Title string
	// Section is the heading path the chunk came from.
	Section string
	// Body is the chunk text.
	Body string
	// BodyTokens is the chunk's size in tokens.
	BodyTokens int
	// MetaJSON carries the provider's extra fields. Empty means "{}".
	MetaJSON string
	// Tags are the item's tags; they are indexed in the full-text row.
	Tags []string
	// Embedding is the chunk's vector, if it has one. A chunk may be
	// written before it is embedded, so this may be empty.
	Embedding []float32
}

// Hit is one search result.
type Hit struct {
	// ChunkID is the matching chunk.
	ChunkID string
	// Title is the chunk's title.
	Title string
	// SourceURL is the source item's URL.
	SourceURL string
	// PublishedAt is the source item's publication time, ISO-8601 UTC.
	PublishedAt string
	// Score is the ranking score: the raw BM25 value for a full-text hit
	// (lower is better) and the cosine similarity for a vector hit
	// (higher is better). The caller fuses the two.
	Score float64
}

// Stats counts what the index currently holds.
type Stats struct {
	// Chunks is the number of rows in chunks.
	Chunks int
	// Vectors is the number of rows in chunk_vectors.
	Vectors int
	// Sources is the number of distinct upstream items (source_kind, source_id).
	Sources int
	// FTSRows is the number of rows in chunks_fts.
	FTSRows int
}

// Open opens (or creates) the index at path, applies any migration that has
// not been applied yet, and returns the store.
//
// The path's parent directory must exist: the store does not create it,
// because a typo in AGENT_DB_PATH should be an error, not a new directory
// nobody asked for.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("index path is empty: set AGENT_DB_PATH or pass --db")
	}

	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("index directory %q does not exist: create it before opening the index", dir)
		}
		return nil, fmt.Errorf("stat index directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("index directory %q is not a directory: point AGENT_DB_PATH at a file inside a directory", dir)
	}

	db, err := open(path)
	if err != nil {
		return nil, err
	}

	all, err := migrations.All()
	if err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{db: db, path: path}
	if err := s.applyMigrations(context.Background(), all); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the index. The store must not be used afterwards.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close the index %s: %w", s.path, err)
	}
	return nil
}

// Path returns the path the index was opened from.
func (s *Store) Path() string {
	return s.path
}

// PutChunk writes one chunk: the chunks row, its full-text row, and - when
// the chunk carries an embedding - its vector, all in one transaction, so a
// reader never sees a chunk without its searchable text.
//
// Writing the same chunk id again replaces the previous rows: the FTS row
// and the vector are deleted and re-inserted, and the chunks row is an
// upsert. No duplicates accumulate.
//
// A chunk written without an embedding keeps the vector it already has:
// re-ingesting an unchanged chunk must not throw away an embedding that the
// embed command already produced.
//
// PutChunk does not touch embedded_with/embedded_at. That audit trail
// belongs to the embed command, which knows which model produced the vector.
func (s *Store) PutChunk(ctx context.Context, c Chunk) error {
	if strings.TrimSpace(c.ChunkID) == "" {
		return errors.New("chunk id is empty: the chunk id is sha256(source_kind|source_id|chunk_seq) and must be set by the caller")
	}
	if err := validateSourceKind(c.SourceKind); err != nil {
		return err
	}

	meta := c.MetaJSON
	if strings.TrimSpace(meta) == "" {
		meta = "{}"
	}
	tags := strings.Join(c.Tags, " ")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the write of chunk %s: %w", c.ChunkID, err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `INSERT INTO chunks (
  chunk_id, source_kind, source_id, source_url, platform, author,
  published_at, ingested_at, updated_at, content_hash, title, section,
  body, body_tokens, meta_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chunk_id) DO UPDATE SET
  source_kind  = excluded.source_kind,
  source_id    = excluded.source_id,
  source_url   = excluded.source_url,
  platform     = excluded.platform,
  author       = excluded.author,
  published_at = excluded.published_at,
  ingested_at  = excluded.ingested_at,
  updated_at   = excluded.updated_at,
  content_hash = excluded.content_hash,
  title        = excluded.title,
  section      = excluded.section,
  body         = excluded.body,
  body_tokens  = excluded.body_tokens,
  meta_json    = excluded.meta_json`,
		c.ChunkID, c.SourceKind, c.SourceID, c.SourceURL, c.Platform, c.Author,
		c.PublishedAt, nowUTC(), c.UpdatedAt, c.ContentHash, c.Title, c.Section,
		c.Body, c.BodyTokens, meta,
	)
	if err != nil {
		return fmt.Errorf("write chunk %s: %w", c.ChunkID, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id = ?`, c.ChunkID); err != nil {
		return fmt.Errorf("clear the full-text row of chunk %s: %w", c.ChunkID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks_fts (chunk_id, title, body, tags) VALUES (?, ?, ?, ?)`,
		c.ChunkID, c.Title, c.Body, tags,
	); err != nil {
		return fmt.Errorf("write the full-text row of chunk %s: %w", c.ChunkID, err)
	}

	if len(c.Embedding) > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_vectors WHERE chunk_id = ?`, c.ChunkID); err != nil {
			return fmt.Errorf("clear the vector of chunk %s: %w", c.ChunkID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunk_vectors (chunk_id, dim, vec) VALUES (?, ?, ?)`,
			c.ChunkID, len(c.Embedding), encodeVector(c.Embedding),
		); err != nil {
			return fmt.Errorf("write the vector of chunk %s: %w", c.ChunkID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the write of chunk %s: %w", c.ChunkID, err)
	}
	return nil
}

// SearchBM25 runs a full-text query and returns the best hits.
//
// The score is the raw bm25() value of the FTS5 index: lower is better, and
// the caller fuses it with the vector ranking. A limit of zero or less means
// the default of 10 hits.
func (s *Store) SearchBM25(ctx context.Context, query string, limit int) ([]Hit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("search query is empty: pass the words to look for")
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT c.chunk_id, c.title, c.source_url, c.published_at, bm25(chunks_fts) AS score
FROM chunks_fts
JOIN chunks c ON c.chunk_id = chunks_fts.chunk_id
WHERE chunks_fts MATCH ?
ORDER BY score
LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search the index for %q: %w", query, err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var hit Hit
		if err := rows.Scan(&hit.ChunkID, &hit.Title, &hit.SourceURL, &hit.PublishedAt, &hit.Score); err != nil {
			return nil, fmt.Errorf("read a search hit for %q: %w", query, err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the search hits for %q: %w", query, err)
	}
	return hits, nil
}

// SearchVector returns the chunks whose stored embedding is closest to q,
// scored by cosine similarity (higher is better).
//
// Every stored vector is compared in Go: the corpus is small enough that an
// approximate index would buy nothing, and storing the vectors as plain
// bytes keeps the index free of a native extension. Rows whose dimension
// differs from len(q) are skipped - they were produced by a different model
// and cannot be compared.
//
// An empty query returns no hits and no error.
func (s *Store) SearchVector(ctx context.Context, q []float32, limit int) ([]Hit, error) {
	if len(q) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT v.chunk_id, v.dim, v.vec, c.title, c.source_url, c.published_at
FROM chunk_vectors v
JOIN chunks c ON c.chunk_id = v.chunk_id`)
	if err != nil {
		return nil, fmt.Errorf("read the stored vectors: %w", err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var (
			chunkID string
			dim     int
			raw     []byte
			hit     Hit
		)
		if err := rows.Scan(&chunkID, &dim, &raw, &hit.Title, &hit.SourceURL, &hit.PublishedAt); err != nil {
			return nil, fmt.Errorf("read a stored vector: %w", err)
		}
		if dim != len(q) {
			// A vector from a different model: it cannot be compared to q.
			continue
		}
		vec, err := decodeVector(raw, dim)
		if err != nil {
			return nil, fmt.Errorf("read the vector of chunk %s: %w", chunkID, err)
		}
		hit.ChunkID = chunkID
		hit.Score = cosineSimilarity(q, vec)
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the stored vectors: %w", err)
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// Stats counts what the index holds.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var stats Stats

	counts := []struct {
		query string
		dest  *int
	}{
		{`SELECT count(*) FROM chunks`, &stats.Chunks},
		{`SELECT count(*) FROM chunk_vectors`, &stats.Vectors},
		{`SELECT count(DISTINCT source_kind || char(31) || source_id) FROM chunks`, &stats.Sources},
		{`SELECT count(*) FROM chunks_fts`, &stats.FTSRows},
	}
	for _, c := range counts {
		if err := s.db.QueryRowContext(ctx, c.query).Scan(c.dest); err != nil {
			return Stats{}, fmt.Errorf("count the index rows: %w", err)
		}
	}
	return stats, nil
}

// validateSourceKind rejects a source kind the chunks table's CHECK
// constraint would reject, with a message that says what is allowed.
func validateSourceKind(kind string) error {
	switch kind {
	case sourceKindGhostPost, sourceKindPostizPost:
		return nil
	default:
		return fmt.Errorf("source kind %q is not a known kind: use %q or %q", kind, sourceKindGhostPost, sourceKindPostizPost)
	}
}

// nowUTC is the timestamp format used for every time the store writes.
func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
