package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// chunkID is a deterministic chunk id for the tests below.
func chunkID(id string) string { return "chunk-" + id }

func TestListChunksReturnsTheChunksInPublicationOrder(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	older := testChunk(chunkID("older"), "the older body")
	older.PublishedAt = "2026-01-01T00:00:00Z"
	newer := testChunk(chunkID("newer"), "the newer body")
	newer.PublishedAt = "2026-02-01T00:00:00Z"
	for _, c := range []Chunk{newer, older} {
		if err := s.PutChunk(ctx, c); err != nil {
			t.Fatalf("PutChunk: %v", err)
		}
	}

	chunks, err := s.ListChunks(ctx, 0, false)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("ListChunks returned %d chunks, want 2", len(chunks))
	}
	if chunks[0].ChunkID != older.ChunkID || chunks[1].ChunkID != newer.ChunkID {
		t.Errorf("ListChunks returned %q then %q, want the older chunk first",
			chunks[0].ChunkID, chunks[1].ChunkID)
	}
	if chunks[0].Body != older.Body {
		t.Errorf("ListChunks returned body %q, want %q", chunks[0].Body, older.Body)
	}
	if chunks[0].Title != older.Title {
		t.Errorf("ListChunks returned title %q, want %q", chunks[0].Title, older.Title)
	}
}

func TestListChunksRespectsTheLimit(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		if err := s.PutChunk(ctx, testChunk(chunkID(id), "body "+id)); err != nil {
			t.Fatalf("PutChunk: %v", err)
		}
	}

	chunks, err := s.ListChunks(ctx, 2, false)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("ListChunks returned %d chunks, want 2", len(chunks))
	}

	chunks, err = s.ListChunks(ctx, 0, false)
	if err != nil {
		t.Fatalf("ListChunks without a limit: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("ListChunks without a limit returned %d chunks, want 3", len(chunks))
	}
}

func TestListChunksWithMissingVectorReturnsTheWorkList(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	withVector := testChunk(chunkID("embedded"), "an embedded chunk")
	if err := s.PutChunk(ctx, withVector); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	if err := s.PutVector(ctx, withVector.ChunkID, []float32{1, 0, 0}); err != nil {
		t.Fatalf("PutVector: %v", err)
	}
	if err := s.PutChunk(ctx, testChunk(chunkID("plain"), "a chunk without a vector")); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	chunks, err := s.ListChunks(ctx, 0, true)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("ListChunks returned %d chunks, want only the one without a vector", len(chunks))
	}
	if chunks[0].ChunkID != chunkID("plain") {
		t.Errorf("ListChunks returned chunk %q, want %q", chunks[0].ChunkID, chunkID("plain"))
	}
}

func TestPutVectorWritesAndReplacesTheVector(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	c := testChunk(chunkID("one"), "a chunk to embed")
	if err := s.PutChunk(ctx, c); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	if err := s.PutVector(ctx, c.ChunkID, []float32{1, 0, 0}); err != nil {
		t.Fatalf("PutVector: %v", err)
	}
	hits, err := s.SearchVector(ctx, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchVector returned %d hits, want 1", len(hits))
	}

	// Writing again replaces the vector instead of adding a second row.
	if err := s.PutVector(ctx, c.ChunkID, []float32{0, 1, 0}); err != nil {
		t.Fatalf("second PutVector: %v", err)
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Vectors != 1 {
		t.Errorf("vectors = %d after two writes, want 1", stats.Vectors)
	}
	hits, err = s.SearchVector(ctx, []float32{0, 1, 0}, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchVector returned %d hits, want 1", len(hits))
	}
}

func TestPutVectorDoesNotTouchTheChunkRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	c := testChunk(chunkID("one"), "the body that must not change")
	if err := s.PutChunk(ctx, c); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	if err := s.PutVector(ctx, c.ChunkID, []float32{1, 2, 3}); err != nil {
		t.Fatalf("PutVector: %v", err)
	}

	chunks, err := s.ListChunks(ctx, 0, false)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("ListChunks returned %d chunks, want 1", len(chunks))
	}
	if chunks[0].Body != c.Body {
		t.Errorf("PutVector changed the body to %q, want %q", chunks[0].Body, c.Body)
	}
	if chunks[0].ContentHash != c.ContentHash {
		t.Errorf("PutVector changed the content hash to %q, want %q", chunks[0].ContentHash, c.ContentHash)
	}

	hits, err := s.SearchBM25(ctx, "body", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("the full-text row is gone: %d hits", len(hits))
	}
}

func TestPutVectorRejectsAChunkThatIsNotInTheIndex(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	err := s.PutVector(ctx, "no-such-chunk", []float32{1, 0, 0})
	if err == nil {
		t.Fatal("PutVector accepted a chunk that is not in the index")
	}
	if got := err.Error(); !contains(got, "no-such-chunk") || !contains(got, "not in the index") {
		t.Errorf("error %q does not name the missing chunk", got)
	}
}

func TestPutVectorRejectsAnEmptyEmbedding(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.PutVector(ctx, "chunk-1", nil); err == nil {
		t.Fatal("PutVector accepted an empty embedding")
	}
}

func TestIntegrityCheckReportsAFreshIndexAsSound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	result, err := s.IntegrityCheck(ctx)
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if result != "ok" {
		t.Errorf("IntegrityCheck returned %q, want %q", result, "ok")
	}
}

// legacySchema is the chunks table as it was before the local markdown
// provider existed: the same columns, with a CHECK constraint that does not
// accept the source kind 'file_post'.
const legacySchema = `CREATE TABLE chunks (
  chunk_id       TEXT PRIMARY KEY,
  source_kind    TEXT NOT NULL CHECK (source_kind IN ('ghost_post','postiz_post')),
  source_id      TEXT NOT NULL,
  source_url     TEXT,
  platform       TEXT,
  author         TEXT NOT NULL,
  published_at   TEXT NOT NULL,
  ingested_at    TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  content_hash   TEXT NOT NULL,
  title          TEXT,
  section        TEXT,
  body           TEXT NOT NULL,
  body_tokens    INTEGER NOT NULL,
  meta_json      TEXT NOT NULL DEFAULT '{}',
  embedded_with  TEXT,
  embedded_at    TEXT
)`

// TestTheMigrationRepairsAnIndexCreatedBeforeTheFilePostKind proves the
// repair path: an index built with the old CHECK constraint keeps its rows
// and its vectors, and accepts the new source kind afterwards.
func TestTheMigrationRepairsAnIndexCreatedBeforeTheFilePostKind(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")

	db, err := sql.Open(driverName, fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", path))
	if err != nil {
		t.Fatalf("open the legacy index: %v", err)
	}
	if _, err := db.ExecContext(ctx, legacySchema); err != nil {
		t.Fatalf("create the legacy chunks table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE chunk_vectors (
  chunk_id TEXT PRIMARY KEY REFERENCES chunks(chunk_id) ON DELETE CASCADE,
  dim      INTEGER NOT NULL,
  vec      BLOB NOT NULL
)`); err != nil {
		t.Fatalf("create the legacy chunk_vectors table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE chunks_fts USING fts5(
  chunk_id UNINDEXED, title, body, tags,
  tokenize='porter unicode61'
)`); err != nil {
		t.Fatalf("create the legacy full-text table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
  name       TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create the migration ledger: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations (name, applied_at) VALUES ('0001_init.sql', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("record the applied migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO chunks (
  chunk_id, source_kind, source_id, author, published_at, ingested_at,
  updated_at, content_hash, body, body_tokens
) VALUES ('legacy-1', 'ghost_post', 'post-1', 'Eli Lap', '2026-01-01T00:00:00Z',
  '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'hash', 'the old body', 3)`,
	); err != nil {
		t.Fatalf("insert the legacy chunk: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO chunk_vectors (chunk_id, dim, vec) VALUES ('legacy-1', 3, ?)`,
		[]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	); err != nil {
		t.Fatalf("insert the legacy vector: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the legacy index: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on the legacy index: %v", err)
	}
	defer s.Close()

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Chunks != 1 {
		t.Errorf("chunks = %d after the repair, want the 1 legacy row", stats.Chunks)
	}
	if stats.Vectors != 1 {
		t.Errorf("vectors = %d after the repair, want the 1 legacy vector", stats.Vectors)
	}
	hits, err := s.SearchVector(ctx, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != "legacy-1" {
		t.Errorf("the legacy vector did not survive the repair: %d hits", len(hits))
	}

	// The repaired schema accepts the new source kind.
	file := testChunk("file-1", "a chunk from a local file")
	file.SourceKind = "file_post"
	file.SourceID = "demo.md"
	if err := s.PutChunk(ctx, file); err != nil {
		t.Fatalf("PutChunk with the file_post kind: %v", err)
	}

	stats, err = s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Chunks != 2 {
		t.Errorf("chunks = %d, want 2 (the legacy row and the new one)", stats.Chunks)
	}
}

// contains is a small helper so the assertions above read as prose.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
