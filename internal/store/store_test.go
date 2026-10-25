package store

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/migrations"
)

// openTestStore opens an index in a fresh temporary directory.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// testChunk returns a valid chunk with the given id and body.
func testChunk(id, body string) Chunk {
	return Chunk{
		ChunkID:     id,
		SourceKind:  "ghost_post",
		SourceID:    "post-1",
		SourceURL:   "https://elilap.dev/" + id,
		Author:      "Eli Lap",
		PublishedAt: "2026-01-02T03:04:05Z",
		UpdatedAt:   "2026-01-02T03:04:05Z",
		ContentHash: "hash-" + id,
		Title:       "Title " + id,
		Section:     "Introduction",
		Body:        body,
		BodyTokens:  len(strings.Fields(body)),
		Tags:        []string{"work", "writing"},
	}
}

func TestMigrationsApplyOnceAndReopeningIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}

	all, err := migrations.All()
	if err != nil {
		t.Fatalf("migrations.All: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no migrations are embedded")
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		t.Fatalf("appliedMigrations: %v", err)
	}
	if len(applied) != len(all) {
		t.Errorf("schema_migrations has %d rows, want one per migration file (%d)", len(applied), len(all))
	}
	for _, m := range all {
		if !applied[m.Name] {
			t.Errorf("migration %s was not recorded in schema_migrations", m.Name)
		}
	}

	for _, table := range []string{"chunks", "chunks_fts", "chunk_vectors", "schema_migrations"} {
		var name string
		err := s.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s is missing after the migrations: %v", table, err)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-opening the same file must not re-apply anything.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	applied, err = again.appliedMigrations(ctx)
	if err != nil {
		t.Fatalf("appliedMigrations after reopen: %v", err)
	}
	if len(applied) != len(all) {
		t.Errorf("after reopening, schema_migrations has %d rows, want %d", len(applied), len(all))
	}
}

func TestPutChunkThenSearchBM25FindsIt(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.PutChunk(ctx, testChunk("chunk-1", "burnout recovery needs deliberate rest")); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	if err := s.PutChunk(ctx, testChunk("chunk-2", "a different subject entirely")); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	hits, err := s.SearchBM25(ctx, "burnout", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchBM25 returned %d hits, want 1", len(hits))
	}
	if hits[0].ChunkID != "chunk-1" {
		t.Errorf("SearchBM25 returned chunk %q, want chunk-1", hits[0].ChunkID)
	}
	if hits[0].Title != "Title chunk-1" {
		t.Errorf("SearchBM25 returned title %q, want %q", hits[0].Title, "Title chunk-1")
	}
	if hits[0].SourceURL != "https://elilap.dev/chunk-1" {
		t.Errorf("SearchBM25 returned source URL %q", hits[0].SourceURL)
	}
	if hits[0].PublishedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("SearchBM25 returned published_at %q", hits[0].PublishedAt)
	}
}

func TestPutChunkTwiceLeavesOneRowEach(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	c := testChunk("chunk-1", "the first body")
	c.Embedding = []float32{1, 0, 0}
	if err := s.PutChunk(ctx, c); err != nil {
		t.Fatalf("first PutChunk: %v", err)
	}

	c.Body = "the second body"
	if err := s.PutChunk(ctx, c); err != nil {
		t.Fatalf("second PutChunk: %v", err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Chunks != 1 {
		t.Errorf("chunks = %d, want 1", stats.Chunks)
	}
	if stats.FTSRows != 1 {
		t.Errorf("fts rows = %d, want 1", stats.FTSRows)
	}
	if stats.Vectors != 1 {
		t.Errorf("vectors = %d, want 1", stats.Vectors)
	}

	// The old text must be gone: the full-text row was replaced, not added.
	hits, err := s.SearchBM25(ctx, "first", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("the replaced body is still searchable: %d hits", len(hits))
	}
	hits, err = s.SearchBM25(ctx, "second", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("the new body is not searchable: %d hits", len(hits))
	}
}

func TestSearchVectorScoresCosineSimilarity(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	vec := []float32{1, 0, 0}
	c := testChunk("chunk-1", "a vectorized chunk")
	c.Embedding = vec
	if err := s.PutChunk(ctx, c); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	hits, err := s.SearchVector(ctx, vec, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchVector returned %d hits, want 1", len(hits))
	}
	if hits[0].ChunkID != "chunk-1" {
		t.Errorf("SearchVector returned chunk %q, want chunk-1", hits[0].ChunkID)
	}
	if math.Abs(hits[0].Score-1) > 1e-6 {
		t.Errorf("SearchVector score = %v, want ~1 for the same vector", hits[0].Score)
	}

	other := []float32{0, 1, 0}
	hits, err = s.SearchVector(ctx, other, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchVector returned %d hits, want 1", len(hits))
	}
	if hits[0].Score >= 1 {
		t.Errorf("SearchVector score = %v for an unrelated vector, want less than 1", hits[0].Score)
	}
	if math.Abs(hits[0].Score) > 1e-6 {
		t.Errorf("SearchVector score = %v for an orthogonal vector, want ~0", hits[0].Score)
	}
}

func TestSearchVectorSkipsRowsWithAnotherDimension(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	three := testChunk("chunk-3d", "three dimensions")
	three.Embedding = []float32{1, 0, 0}
	if err := s.PutChunk(ctx, three); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	two := testChunk("chunk-2d", "two dimensions")
	two.Embedding = []float32{1, 0}
	if err := s.PutChunk(ctx, two); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	hits, err := s.SearchVector(ctx, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchVector returned %d hits, want 1 (the row with another dimension is skipped)", len(hits))
	}
	if hits[0].ChunkID != "chunk-3d" {
		t.Errorf("SearchVector returned chunk %q, want chunk-3d", hits[0].ChunkID)
	}
}

func TestSearchVectorWithAnEmptyQueryReturnsNoHits(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	c := testChunk("chunk-1", "a vectorized chunk")
	c.Embedding = []float32{1, 0, 0}
	if err := s.PutChunk(ctx, c); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	hits, err := s.SearchVector(ctx, nil, 10)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("SearchVector returned %d hits for an empty query, want 0", len(hits))
	}
}

func TestPutChunkRejectsAnEmptyChunkID(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	c := testChunk("", "a body without an identity")
	err := s.PutChunk(ctx, c)
	if err == nil {
		t.Fatal("PutChunk accepted a chunk with an empty chunk id")
	}
	if !strings.Contains(err.Error(), "chunk id is empty") {
		t.Errorf("error %q does not say the chunk id is empty", err)
	}
}

func TestSearchBM25RejectsAnEmptyQuery(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, err := s.SearchBM25(ctx, "   ", 10); err == nil {
		t.Fatal("SearchBM25 accepted an empty query")
	}
}

func TestStatsCountsTheIndex(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	first := testChunk("chunk-1", "the first chunk")
	first.Embedding = []float32{1, 0, 0}
	if err := s.PutChunk(ctx, first); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	second := testChunk("chunk-2", "the second chunk")
	if err := s.PutChunk(ctx, second); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Chunks != 2 {
		t.Errorf("chunks = %d, want 2", stats.Chunks)
	}
	if stats.Vectors != 1 {
		t.Errorf("vectors = %d, want 1", stats.Vectors)
	}
	if stats.FTSRows != 2 {
		t.Errorf("fts rows = %d, want 2", stats.FTSRows)
	}
	if stats.Sources != 1 {
		t.Errorf("sources = %d, want 1 (both chunks share one upstream item)", stats.Sources)
	}
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	_, err := Open("")
	if err == nil {
		t.Fatal("Open accepted an empty path")
	}
	if !strings.Contains(err.Error(), "index path is empty") {
		t.Errorf("error %q does not say the index path is empty", err)
	}
}

func TestOpenRejectsAMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", "index.db")
	_, err := Open(missing)
	if err == nil {
		t.Fatal("Open accepted a path whose parent directory does not exist")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "missing")) {
		t.Errorf("error %q does not name the missing directory %q", err, filepath.Join(dir, "missing"))
	}
}

func TestAFailingMigrationRollsBackAndNamesTheFile(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	before, err := s.appliedMigrations(ctx)
	if err != nil {
		t.Fatalf("appliedMigrations: %v", err)
	}

	broken := []migrations.Migration{{Name: "9999_broken.sql", SQL: "CREATE TABLE broken ("}}
	err = s.applyMigrations(ctx, broken)
	if err == nil {
		t.Fatal("applyMigrations accepted a broken migration")
	}
	if !strings.Contains(err.Error(), "9999_broken.sql") {
		t.Errorf("error %q does not name the migration file", err)
	}

	after, err := s.appliedMigrations(ctx)
	if err != nil {
		t.Fatalf("appliedMigrations: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("schema_migrations has %d rows after the failed migration, want %d", len(after), len(before))
	}
	if _, ok := after["9999_broken.sql"]; ok {
		t.Error("the failed migration was recorded in schema_migrations")
	}

	var name string
	err = s.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='broken'").Scan(&name)
	if err == nil {
		t.Error("the failed migration's table survived the rollback")
	}
}
