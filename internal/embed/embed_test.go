package embed

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// fakeEmbedder records every batch and returns a deterministic 768-dim vector
// per input, so tests can assert batch boundaries and resumption without a
// model server.
type fakeEmbedder struct {
	dims   int
	calls  int
	inputs []string
	failAt int // 1-based call number to fail; 0 means never
}

func (f *fakeEmbedder) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	f.calls++
	if f.failAt > 0 && f.calls == f.failAt {
		return nil, fmt.Errorf("boom")
	}
	f.inputs = append(f.inputs, inputs...)
	out := make([][]float32, len(inputs))
	for i := range inputs {
		dims := f.dims
		if dims == 0 {
			dims = store.EmbeddingDims
		}
		v := make([]float32, dims)
		for j := range v {
			v[j] = float32(j) / float32(dims)
		}
		out[i] = v
	}
	return out, nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), "../../migrations")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedChunks(t *testing.T, s *store.Store, n int) {
	t.Helper()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	chunks := make([]store.Chunk, n)
	for i := range chunks {
		chunks[i] = store.Chunk{
			ChunkID:     fmt.Sprintf("c%03d", i),
			SourceKind:  "ghost_post",
			SourceID:    "uuid-1",
			Author:      "Eli",
			PublishedAt: now,
			UpdatedAt:   now,
			ContentHash: fmt.Sprintf("hash-%d", i),
			Title:       "A post",
			Section:     "Intro",
			Body:        fmt.Sprintf("body %d", i),
			BodyTokens:  2,
		}
	}
	if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}, chunks); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
}

func stats(t *testing.T, s *store.Store) store.Counts {
	t.Helper()
	c, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	return c
}

func TestRunEmbedsInBatches(t *testing.T) {
	s := newTestStore(t)
	seedChunks(t, s, 5)
	fake := &fakeEmbedder{}
	r := &Runner{Store: s, Embedder: fake}

	res, err := r.Run(context.Background(), Options{Model: "m", BatchSize: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Pending != 5 || res.Embedded != 5 || res.Skipped != 0 || res.Remaining != 0 {
		t.Errorf("result = %+v, want pending/embedded 5, remaining 0", res)
	}
	if res.Batches != 3 || fake.calls != 3 {
		t.Errorf("batches = %d (calls %d), want 3", res.Batches, fake.calls)
	}
	c := stats(t, s)
	if c.VecRows != 5 || c.Embedded != 5 {
		t.Errorf("stats = %+v, want 5 vec and 5 embedded rows", c)
	}
}

func TestRunIsIdempotentAndResumes(t *testing.T) {
	s := newTestStore(t)
	seedChunks(t, s, 4)
	fake := &fakeEmbedder{}
	r := &Runner{Store: s, Embedder: fake}

	if _, err := r.Run(context.Background(), Options{Model: "m", BatchSize: 2, Limit: 2}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	fakeAfterFirst := fake.calls
	fake.inputs = nil

	// Resume: only the two chunks left pending are embedded.
	res, err := r.Run(context.Background(), Options{Model: "m", BatchSize: 2})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.Pending != 2 || res.Embedded != 2 || res.Remaining != 0 {
		t.Errorf("resume result = %+v, want 2 pending, 2 embedded", res)
	}
	if fake.calls != fakeAfterFirst+1 {
		t.Errorf("calls = %d, want one more than %d", fake.calls, fakeAfterFirst)
	}

	// Third run is a no-op and makes no request.
	res, err = r.Run(context.Background(), Options{Model: "m"})
	if err != nil {
		t.Fatalf("third Run: %v", err)
	}
	if res.Pending != 0 || res.Embedded != 0 || res.Batches != 0 {
		t.Errorf("no-op result = %+v, want nothing pending", res)
	}
}

func TestRunReembedsAfterChunkChange(t *testing.T) {
	s := newTestStore(t)
	seedChunks(t, s, 3)
	fake := &fakeEmbedder{}
	r := &Runner{Store: s, Embedder: fake}
	if _, err := r.Run(context.Background(), Options{Model: "m"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Rewriting the item deletes the old vec rows and clears the stamps, so
	// exactly the rewritten chunks come back as pending.
	changed := []store.Chunk{{
		ChunkID: "c000", SourceKind: "ghost_post", SourceID: "uuid-1",
		Author: "Eli", PublishedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		ContentHash: "hash-changed", Title: "A post", Section: "Intro",
		Body: "new body", BodyTokens: 2,
	}}
	if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}, changed); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if c := stats(t, s); c.VecRows != 0 {
		t.Errorf("vec rows after rewrite = %d, want 0 (stale vectors must be cleared)", c.VecRows)
	}

	res, err := r.Run(context.Background(), Options{Model: "m"})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.Embedded != 1 || res.Remaining != 0 {
		t.Errorf("result = %+v, want only the rewritten chunk embedded", res)
	}
}

func TestRunReportsProgress(t *testing.T) {
	s := newTestStore(t)
	seedChunks(t, s, 5)
	var seen []Progress
	r := &Runner{Store: s, Embedder: &fakeEmbedder{}}
	_, err := r.Run(context.Background(), Options{
		Model:     "m",
		BatchSize: 2,
		Progress:  func(p Progress) { seen = append(seen, p) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) < 4 {
		t.Fatalf("progress reports = %d, want at least start + 3 batches", len(seen))
	}
	if first := seen[0]; first.Total != 5 || first.Done != 0 || first.InFlight != 0 {
		t.Errorf("first report = %+v, want total 5 and nothing done", first)
	}
	sawInFlight := false
	for _, p := range seen {
		if p.Total != 5 {
			t.Errorf("report %+v: total changed", p)
		}
		if p.InFlight > 0 {
			sawInFlight = true
		}
	}
	if !sawInFlight {
		t.Error("no report carried an in-flight batch")
	}
	last := seen[len(seen)-1]
	if last.Done != 5 || last.InFlight != 0 || last.Batches != 3 {
		t.Errorf("last report = %+v, want 5 done in 3 batches", last)
	}
}

func TestRunFullReembedsEverything(t *testing.T) {
	s := newTestStore(t)
	seedChunks(t, s, 3)
	fake := &fakeEmbedder{}
	r := &Runner{Store: s, Embedder: fake}
	if _, err := r.Run(context.Background(), Options{Model: "m"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	res, err := r.Run(context.Background(), Options{Model: "m", BatchSize: 2, Full: true})
	if err != nil {
		t.Fatalf("full Run: %v", err)
	}
	if res.Pending != 3 || res.Embedded != 3 || res.Remaining != 0 {
		t.Errorf("full result = %+v, want all 3 re-embedded", res)
	}
	if res.Batches != 2 {
		t.Errorf("batches = %d, want 2", res.Batches)
	}
}

func TestRunRejectsWrongDimensions(t *testing.T) {
	s := newTestStore(t)
	seedChunks(t, s, 1)
	r := &Runner{Store: s, Embedder: &fakeEmbedder{dims: 3}}
	_, err := r.Run(context.Background(), Options{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("error = %v, want a dimension error", err)
	}
	if c := stats(t, s); c.VecRows != 0 {
		t.Errorf("vec rows = %d after a rejected batch, want 0", c.VecRows)
	}
}

func TestRunFailureKeepsCompletedBatches(t *testing.T) {
	s := newTestStore(t)
	seedChunks(t, s, 4)
	fake := &fakeEmbedder{failAt: 2}
	r := &Runner{Store: s, Embedder: fake}
	res, err := r.Run(context.Background(), Options{Model: "m", BatchSize: 2})
	if err == nil {
		t.Fatal("Run succeeded, want the second batch to fail")
	}
	if c := stats(t, s); c.VecRows != 2 {
		t.Errorf("vec rows = %d, want the first batch's 2 rows committed", c.VecRows)
	}
	// The error path still reports what is left, so the CLI summary does not
	// claim "remaining: 0" after a half-finished run.
	if res.Remaining != 2 {
		t.Errorf("remaining = %d after a failed second batch, want 2", res.Remaining)
	}
}

func TestEmbedTextIncludesContext(t *testing.T) {
	got := EmbedText(store.PendingChunk{Title: "T", Section: "S", Body: "B"})
	if got != "T\n\nS\n\nB" {
		t.Errorf("EmbedText = %q", got)
	}
	if got := EmbedText(store.PendingChunk{Body: "only"}); got != "only" {
		t.Errorf("EmbedText without title/section = %q", got)
	}
}

func TestTagChangesWithModelAndVersion(t *testing.T) {
	if Tag("a") == Tag("b") {
		t.Error("Tag ignores the model")
	}
	if !strings.Contains(Tag("a"), "/text-"+TextVersion) {
		t.Errorf("Tag = %q, want it to carry the text version", Tag("a"))
	}
}
