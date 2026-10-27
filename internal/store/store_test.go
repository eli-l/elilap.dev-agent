package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// newTestStore opens a throwaway index backed by the repo's real migrations,
// which also proves the vec0 DDL applies (sqlite-vec auto-loaded) and FTS5
// is compiled in via the sqlite_fts5 build tag.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "index.db"), "../../migrations")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testChunk(id string) store.Chunk {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	return store.Chunk{
		ChunkID: id, SourceKind: "ghost_post", SourceID: "uuid-1",
		SourceURL: "https://read.elilap.dev/post/", Author: "Eli",
		PublishedAt: now, UpdatedAt: now,
		ContentHash: "hash-" + id, Title: "A post", Section: "Intro",
		Body: "hello world content", BodyTokens: 3,
		Meta: map[string]any{"tags": []any{"go", "sqlite"}},
	}
}

func TestMigrationsApply(t *testing.T) {
	s := newTestStore(t)
	c, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if c.Chunks != 0 || c.FTSRows != 0 || c.VecRows != 0 {
		t.Errorf("fresh store stats = %+v, want all zero", c)
	}
}

func TestReplaceItemChunksRoundTrip(t *testing.T) {
	s := newTestStore(t)
	k := store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}

	added, updated, err := s.ReplaceItemChunks(k, []store.Chunk{testChunk("c1"), testChunk("c2")})
	if err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if added != 2 || updated != 0 {
		t.Errorf("first replace = %d added, %d updated; want 2, 0", added, updated)
	}

	// Identical content is a no-op: no added rows.
	added, updated, err = s.ReplaceItemChunks(k, []store.Chunk{testChunk("c1"), testChunk("c2")})
	if err != nil {
		t.Fatalf("idempotent replace: %v", err)
	}
	if added != 0 || updated != 2 {
		t.Errorf("idempotent replace = %d added, %d updated; want 0, 2", added, updated)
	}

	// Changed content rewrites.
	changed := testChunk("c1")
	changed.ContentHash = "hash-changed"
	changed.Body = "new body"
	added, _, err = s.ReplaceItemChunks(k, []store.Chunk{changed})
	if err != nil {
		t.Fatalf("changed replace: %v", err)
	}
	if added != 1 {
		t.Errorf("changed replace = %d added; want 1 (c2 dropped, c1 rewritten)", added)
	}

	// FTS stays in sync: exactly one row after the rewrite.
	c, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if c.Chunks != 1 || c.FTSRows != 1 {
		t.Errorf("stats = %+v; want 1 chunk and 1 fts row", c)
	}
}

func TestDeleteItem(t *testing.T) {
	s := newTestStore(t)
	k := store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}
	if _, _, err := s.ReplaceItemChunks(k, []store.Chunk{testChunk("c1")}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := s.DeleteItem(k); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteItem(k); !errors.Is(err, store.ErrUnknownItem) {
		t.Errorf("second delete = %v; want ErrUnknownItem", err)
	}
	c, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if c.Chunks != 0 || c.FTSRows != 0 {
		t.Errorf("stats after delete = %+v; want zero rows (fts must stay in sync)", c)
	}
}

func TestLedgerDiff(t *testing.T) {
	s := newTestStore(t)
	runID := "run-1"
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	a := store.LedgerItem{ItemID: "a", ETag: "1", LastModified: "2026-10-25T00:00:00Z"}
	b := store.LedgerItem{ItemID: "b", ETag: "1", LastModified: "2026-10-25T00:00:00Z"}
	if err := s.UpsertItem("ghost", a, runID, now); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if err := s.UpsertItem("ghost", b, runID, now); err != nil {
		t.Fatalf("upsert b: %v", err)
	}

	// a changed (etag), b unchanged, c new; b2 (formerly b) removed below by
	// not listing it.
	list := []store.LedgerItem{
		{ItemID: "a", ETag: "2", LastModified: "2026-10-26T00:00:00Z"},
		{ItemID: "b", ETag: "1", LastModified: "2026-10-25T00:00:00Z"},
		{ItemID: "c", ETag: "", LastModified: "2026-10-26T00:00:00Z"},
	}
	diff, err := s.DiffLedger("ghost", list)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff.Seen != 3 {
		t.Errorf("Seen = %d; want 3", diff.Seen)
	}
	var ids []string
	for _, it := range diff.ToFetch {
		ids = append(ids, it.ItemID)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "c" {
		t.Errorf("ToFetch = %v; want [a c]", ids)
	}
	if len(diff.ToRemove) != 0 {
		t.Errorf("ToRemove = %v; want none", diff.ToRemove)
	}

	// Removing b from the list flags it for drift cleanup.
	diff, err = s.DiffLedger("ghost", list[:1])
	if err != nil {
		t.Fatalf("diff 2: %v", err)
	}
	if len(diff.ToRemove) != 2 {
		t.Errorf("ToRemove = %d items; want 2 (b and c)", len(diff.ToRemove))
	}
}

func TestSetSourceStateWatermark(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.LastRunAt("ghost"); !errors.Is(err, store.ErrNoSourceState) {
		t.Errorf("LastRunAt before first run = %v; want ErrNoSourceState", err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if err := s.SetSourceState("ghost", "ghost", "cfg-hash", false, 3, now); err != nil {
		t.Fatalf("set state: %v", err)
	}
	got, err := s.LastRunAt("ghost")
	if err != nil {
		t.Fatalf("last run: %v", err)
	}
	if !got.Equal(now) {
		t.Errorf("LastRunAt = %v; want %v", got, now)
	}
}
