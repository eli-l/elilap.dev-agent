package store

import (
	"context"
	"testing"
)

// ledgerChunk returns a chunk of the item itemID, with the given body.
func ledgerChunk(id, itemID, body string) Chunk {
	c := testChunk(id, body)
	c.SourceID = itemID
	return c
}

func TestSourceStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// A source the ledger never saw has no row, and that is not an error.
	_, ok, err := s.SourceState(ctx, "files")
	if err != nil {
		t.Fatalf("SourceState: %v", err)
	}
	if ok {
		t.Errorf("SourceState found a row for a source that was never imported")
	}

	want := SourceState{
		SourceID:   "files",
		Type:       "files",
		ConfigHash: "hash-1",
		LastRunAt:  "2026-10-25T10:00:00Z",
		LastFullAt: "",
		ItemCount:  3,
	}
	if err := s.PutSourceState(ctx, want); err != nil {
		t.Fatalf("PutSourceState: %v", err)
	}

	got, ok, err := s.SourceState(ctx, "files")
	if err != nil {
		t.Fatalf("SourceState: %v", err)
	}
	if !ok {
		t.Fatalf("SourceState did not find the row it wrote")
	}
	if got != want {
		t.Errorf("SourceState returned %+v, want %+v", got, want)
	}

	// A second write replaces the row.
	want.ConfigHash = "hash-2"
	want.LastFullAt = "2026-10-25T11:00:00Z"
	want.ItemCount = 4
	if err := s.PutSourceState(ctx, want); err != nil {
		t.Fatalf("PutSourceState: %v", err)
	}
	got, ok, err = s.SourceState(ctx, "files")
	if err != nil {
		t.Fatalf("SourceState: %v", err)
	}
	if !ok || got != want {
		t.Errorf("SourceState returned %+v (found=%v), want %+v", got, ok, want)
	}
}

func TestPutSourceItemKeepsTheFirstSeenAt(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	first := SourceItem{
		SourceID:     "files",
		ItemID:       "alpha.md",
		URL:          "/corpus/alpha.md",
		ContentHash:  "content-1",
		ETag:         "etag-1",
		LastModified: "2026-10-20T00:00:00Z",
		FirstSeenAt:  "2026-10-20T00:00:00Z",
		LastSeenAt:   "2026-10-20T00:00:00Z",
		SeenRunID:    "run-1",
	}
	if err := s.PutSourceItem(ctx, first); err != nil {
		t.Fatalf("PutSourceItem: %v", err)
	}

	// The second write carries a later first_seen_at on purpose: the
	// store must keep the earliest one.
	second := first
	second.ContentHash = "content-2"
	second.ETag = "etag-2"
	second.FirstSeenAt = "2026-10-25T00:00:00Z"
	second.LastSeenAt = "2026-10-25T00:00:00Z"
	second.SeenRunID = "run-2"
	if err := s.PutSourceItem(ctx, second); err != nil {
		t.Fatalf("PutSourceItem: %v", err)
	}

	items, err := s.SourceItems(ctx, "files")
	if err != nil {
		t.Fatalf("SourceItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("SourceItems returned %d items, want 1", len(items))
	}
	got := items[0]
	if got.FirstSeenAt != first.FirstSeenAt {
		t.Errorf("first_seen_at = %q, want the earliest value %q", got.FirstSeenAt, first.FirstSeenAt)
	}
	if got.LastSeenAt != second.LastSeenAt {
		t.Errorf("last_seen_at = %q, want %q", got.LastSeenAt, second.LastSeenAt)
	}
	if got.SeenRunID != "run-2" {
		t.Errorf("seen_run_id = %q, want %q", got.SeenRunID, "run-2")
	}
	if got.ContentHash != "content-2" {
		t.Errorf("content_hash = %q, want %q", got.ContentHash, "content-2")
	}
	if got.ETag != "etag-2" {
		t.Errorf("etag = %q, want %q", got.ETag, "etag-2")
	}
}

func TestDeleteSourceItemsKeepsTheListedIdsAndCountsTheRest(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	for _, id := range []string{"a.md", "b.md", "c.md", "d.md"} {
		err := s.PutSourceItem(ctx, SourceItem{
			SourceID:    "files",
			ItemID:      id,
			ContentHash: "hash-" + id,
			FirstSeenAt: "2026-10-20T00:00:00Z",
			LastSeenAt:  "2026-10-20T00:00:00Z",
			SeenRunID:   "run-1",
		})
		if err != nil {
			t.Fatalf("PutSourceItem(%s): %v", id, err)
		}
	}
	// An item of another source must survive: the deletion is scoped to
	// one source.
	err := s.PutSourceItem(ctx, SourceItem{
		SourceID:    "other",
		ItemID:      "a.md",
		ContentHash: "hash-other",
		FirstSeenAt: "2026-10-20T00:00:00Z",
		LastSeenAt:  "2026-10-20T00:00:00Z",
		SeenRunID:   "run-1",
	})
	if err != nil {
		t.Fatalf("PutSourceItem: %v", err)
	}

	deleted, err := s.DeleteSourceItems(ctx, "files", []string{"a.md", "c.md"})
	if err != nil {
		t.Fatalf("DeleteSourceItems: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteSourceItems deleted %d rows, want 2", deleted)
	}

	items, err := s.SourceItems(ctx, "files")
	if err != nil {
		t.Fatalf("SourceItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("SourceItems returned %d items, want 2", len(items))
	}
	for _, item := range items {
		if item.ItemID != "a.md" && item.ItemID != "c.md" {
			t.Errorf("SourceItems returned %q, want only a.md and c.md", item.ItemID)
		}
	}

	other, err := s.SourceItems(ctx, "other")
	if err != nil {
		t.Fatalf("SourceItems: %v", err)
	}
	if len(other) != 1 {
		t.Errorf("the deletion touched another source: %d rows left, want 1", len(other))
	}

	// An empty keep list removes every row of the source.
	deleted, err = s.DeleteSourceItems(ctx, "files", nil)
	if err != nil {
		t.Fatalf("DeleteSourceItems: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteSourceItems deleted %d rows, want 2", deleted)
	}
	items, err = s.SourceItems(ctx, "files")
	if err != nil {
		t.Fatalf("SourceItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("SourceItems returned %d items, want 0", len(items))
	}
}

func TestDeleteChunksForItemRemovesTheChunkItsFullTextRowAndItsVector(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	const itemID = "post-1"
	keep := ledgerChunk("chunk-keep", itemID, "the kept body mentions marmalade")
	drop := ledgerChunk("chunk-drop", itemID, "the dropped body mentions xylophone")
	drop.Embedding = []float32{0.5, 0.5}
	other := ledgerChunk("chunk-other", "post-2", "another item mentions xylophone too")

	for _, c := range []Chunk{keep, drop, other} {
		if err := s.PutChunk(ctx, c); err != nil {
			t.Fatalf("PutChunk(%s): %v", c.ChunkID, err)
		}
	}

	// The word that only the dropped chunk carries must be searchable
	// before the deletion.
	hits, err := s.SearchBM25(ctx, "xylophone", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("SearchBM25 returned %d hits before the deletion, want 2", len(hits))
	}

	deleted, err := s.DeleteChunksForItem(ctx, "ghost_post", itemID, []string{keep.ChunkID})
	if err != nil {
		t.Fatalf("DeleteChunksForItem: %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteChunksForItem deleted %d chunks, want 1", deleted)
	}

	// The chunk row is gone.
	chunks, err := s.ListChunks(ctx, 0, false)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	for _, c := range chunks {
		if c.ChunkID == drop.ChunkID {
			t.Errorf("the dropped chunk %s is still in the index", drop.ChunkID)
		}
	}
	if len(chunks) != 2 {
		t.Errorf("the index holds %d chunks, want 2 (the kept chunk and the other item)", len(chunks))
	}

	// The full-text row is gone: the word of the dropped chunk now only
	// matches the other item.
	hits, err = s.SearchBM25(ctx, "xylophone", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchBM25 returned %d hits after the deletion, want 1", len(hits))
	}
	if hits[0].ChunkID != other.ChunkID {
		t.Errorf("SearchBM25 returned %s, want %s", hits[0].ChunkID, other.ChunkID)
	}

	// The vector of the dropped chunk is gone with it.
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Vectors != 0 {
		t.Errorf("the index holds %d vectors, want 0: the vector of the dropped chunk must go with it", stats.Vectors)
	}
	if stats.FTSRows != 2 {
		t.Errorf("the index holds %d full-text rows, want 2: the full-text row of the dropped chunk must go with it", stats.FTSRows)
	}

	// The kept chunk is still searchable.
	hits, err = s.SearchBM25(ctx, "marmalade", 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != keep.ChunkID {
		t.Errorf("the kept chunk is not searchable any more: %+v", hits)
	}
}

func TestDeleteChunksForItemWithAnEmptyKeepListRemovesEveryChunkOfTheItem(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	const itemID = "post-1"
	for _, id := range []string{"chunk-1", "chunk-2"} {
		if err := s.PutChunk(ctx, ledgerChunk(id, itemID, "the body of "+id)); err != nil {
			t.Fatalf("PutChunk(%s): %v", id, err)
		}
	}
	if err := s.PutChunk(ctx, ledgerChunk("chunk-other", "post-2", "another body")); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	deleted, err := s.DeleteChunksForItem(ctx, "ghost_post", itemID, nil)
	if err != nil {
		t.Fatalf("DeleteChunksForItem: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteChunksForItem deleted %d chunks, want 2", deleted)
	}

	chunks, err := s.ListChunks(ctx, 0, false)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 1 || chunks[0].ChunkID != "chunk-other" {
		t.Errorf("the index holds %+v, want only chunk-other", chunks)
	}
}
