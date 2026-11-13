package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// TestOpenRebuildsVecTableForNewWidth pins the EMBED_DIMS behaviour: a
// read-write open at a width the index does not have rebuilds chunks_vec,
// drops the old vectors, and clears the embedded_with stamps so the next
// `agent embed` sees the whole corpus as pending. A read-only open then
// reports the new width and rejects the old one.
func TestOpenRebuildsVecTableForNewWidth(t *testing.T) {
	const migrations = "../../migrations"
	path := filepath.Join(t.TempDir(), "index.db")

	first, err := store.Open(path, migrations, store.DefaultEmbeddingDims)
	if err != nil {
		t.Fatalf("open at the default width: %v", err)
	}
	if _, _, err := first.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"},
		[]store.Chunk{testChunk("resize-1")}); err != nil {
		first.Close()
		t.Fatalf("seed chunk: %v", err)
	}
	oldVector := make([]float32, store.DefaultEmbeddingDims)
	oldVector[0] = 1
	if written, _, err := first.WriteEmbeddings([]store.ChunkEmbedding{{
		ChunkID:     "resize-1",
		ContentHash: "hash-resize-1",
		Vector:      oldVector,
	}}, "localai:embeddinggemma-300m/text-v1", time.Now()); err != nil || written != 1 {
		first.Close()
		t.Fatalf("seed embedding = %d written, err %v", written, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	const newDims = 1536
	resized, err := store.Open(path, migrations, newDims)
	if err != nil {
		t.Fatalf("open at %d dimensions: %v", newDims, err)
	}
	t.Cleanup(func() { resized.Close() })

	if got := resized.Dims(); got != newDims {
		t.Fatalf("Dims() = %d, want %d", got, newDims)
	}
	pending, err := resized.CountPendingEmbeddings("localai:embeddinggemma-300m/text-v1", false)
	if err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 1 {
		t.Errorf("pending after the rebuild = %d, want the whole corpus (1)", pending)
	}
	if err := resized.CheckDims(store.DefaultEmbeddingDims); !errors.Is(err, store.ErrVecDims) {
		t.Errorf("CheckDims(old width) = %v, want ErrVecDims", err)
	}
	if _, _, err := resized.WriteEmbeddings([]store.ChunkEmbedding{{
		ChunkID:     "resize-1",
		ContentHash: "hash-resize-1",
		Vector:      oldVector,
	}}, "localai:embeddinggemma-300m/text-v1", time.Now()); err == nil {
		t.Error("write embeddings accepted a vector from the old width")
	}

	// The rebuilt table has to actually work at the new width: write one and
	// find it through KNN.
	newVector := make([]float32, newDims)
	newVector[0] = 1
	if _, _, err := resized.WriteEmbeddings([]store.ChunkEmbedding{{
		ChunkID:     "resize-1",
		ContentHash: "hash-resize-1",
		Vector:      newVector,
	}}, "localai:embeddinggemma-300m/text-v1", time.Now()); err != nil {
		t.Fatalf("write embedding at %d dimensions: %v", newDims, err)
	}
	hits, err := resized.SearchVectors(newVector, 1, store.Filter{})
	if err != nil {
		t.Fatalf("knn search at the new width: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != "resize-1" {
		t.Fatalf("knn search returned %+v, want the re-embedded chunk", hits)
	}

	// A read-only open discovers the width from the schema instead of taking
	// it from the caller, so stats and backup work regardless of EMBED_DIMS.
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer ro.Close()
	if got := ro.Dims(); got != newDims {
		t.Errorf("read-only Dims() = %d, want %d", got, newDims)
	}
	if err := ro.CheckDims(newDims); err != nil {
		t.Errorf("CheckDims(%d) = %v, want nil", newDims, err)
	}
}

// TestOpenRejectsAbsurdWidth keeps a configuration typo from creating a vector
// table whose every KNN would be enormous.
func TestOpenRejectsAbsurdWidth(t *testing.T) {
	_, err := store.Open(filepath.Join(t.TempDir(), "index.db"), "../../migrations", 1<<20)
	if err == nil {
		t.Fatal("Open accepted an embedding width above the ceiling")
	}
}
