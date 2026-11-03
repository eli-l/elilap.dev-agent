package store_test

import (
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// unitVector is a one-hot vector in the embedding space: distances between
// different axes are exactly sqrt(2), which makes ordering assertions readable.
func unitVector(axis int) []float32 {
	v := make([]float32, store.EmbeddingDims)
	v[axis] = 1
	return v
}

// seedVectorChunks writes chunks c1..cn of one item and embeds them with the
// supplied vectors, returning the store ready for a KNN query.
func seedVectorChunks(t *testing.T, vectors map[string][]float32) *store.Store {
	t.Helper()
	s := newTestStore(t)
	chunks := make([]store.Chunk, 0, len(vectors))
	rows := make([]store.ChunkEmbedding, 0, len(vectors))
	for id, vec := range vectors {
		c := testChunk(id)
		chunks = append(chunks, c)
		rows = append(rows, store.ChunkEmbedding{
			ChunkID:     id,
			ContentHash: c.ContentHash,
			Vector:      vec,
		})
	}
	if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}, chunks); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	written, skipped, err := s.WriteEmbeddings(rows, "test:model/text-v1",
		time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("seed embeddings: %v", err)
	}
	if written != len(vectors) || skipped != 0 {
		t.Fatalf("seed wrote %d vectors, skipped %d; want %d, 0", written, skipped, len(vectors))
	}
	return s
}

func TestSearchVectorsOrdersByDistance(t *testing.T) {
	// c1 is the query itself (distance 0). c3 points the same way but is
	// twice as long (distance 1). c2 is orthogonal (distance sqrt(2)).
	s := seedVectorChunks(t, map[string][]float32{
		"c1": unitVector(0),
		"c2": unitVector(1),
		"c3": scaleVector(unitVector(0), 2),
	})

	hits, err := s.SearchVectors(unitVector(0), 3, store.Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3", len(hits))
	}
	want := []string{"c1", "c3", "c2"}
	for i, id := range want {
		if hits[i].ChunkID != id {
			t.Errorf("hit %d = %s, want %s", i, hits[i].ChunkID, id)
		}
	}
	if hits[0].Distance != 0 {
		t.Errorf("self distance = %v, want 0", hits[0].Distance)
	}
	if math.Abs(hits[1].Distance-1) > 1e-6 {
		t.Errorf("scaled distance = %v, want 1", hits[1].Distance)
	}
	if math.Abs(hits[2].Distance-math.Sqrt2) > 1e-6 {
		t.Errorf("orthogonal distance = %v, want sqrt(2)", hits[2].Distance)
	}

	// The readable metadata is hydrated from chunks, not from the vec table.
	first := hits[0]
	if first.SourceKind != "ghost_post" || first.SourceID != "uuid-1" {
		t.Errorf("source = %s/%s, want ghost_post/uuid-1", first.SourceKind, first.SourceID)
	}
	if first.Title != "A post" || first.Body != "hello world content" {
		t.Errorf("title/body = %q/%q, want the stored chunk text", first.Title, first.Body)
	}
	if first.SourceURL != "https://read.elilap.dev/post/" {
		t.Errorf("source_url = %q", first.SourceURL)
	}
	if first.Author != "Eli" {
		t.Errorf("author = %q, want Eli", first.Author)
	}
	if !first.PublishedAt.Equal(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("published_at = %v", first.PublishedAt)
	}
	if len(first.Vector) != store.EmbeddingDims {
		t.Errorf("stored vector has %d dimensions, want %d", len(first.Vector), store.EmbeddingDims)
	}
}

func TestSearchVectorsRespectsK(t *testing.T) {
	s := seedVectorChunks(t, map[string][]float32{
		"c1": unitVector(0),
		"c2": unitVector(1),
		"c3": unitVector(2),
	})
	hits, err := s.SearchVectors(unitVector(0), 2, store.Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("got %d hits for k=2, want 2", len(hits))
	}
}

func TestSearchVectorsEmptyIndex(t *testing.T) {
	s := newTestStore(t)
	hits, err := s.SearchVectors(unitVector(0), 5, store.Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("got %d hits on an empty index, want 0", len(hits))
	}
}

func TestSearchVectorsRejectsBadArguments(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SearchVectors(make([]float32, 3), 5, store.Filter{}); err == nil {
		t.Error("wrong-dimension query was accepted")
	}
	if _, err := s.SearchVectors(unitVector(0), 0, store.Filter{}); err == nil {
		t.Error("k=0 was accepted")
	}
	if _, err := s.SearchVectors(unitVector(0), -1, store.Filter{}); err == nil {
		t.Error("negative k was accepted")
	}
}

// scaleVector multiplies a vector, giving a non-normalized row to prove the
// search does not assume unit length.
func scaleVector(v []float32, f float32) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * f
	}
	return out
}

func TestEmbeddingTags(t *testing.T) {
	s := seedVectorChunks(t, map[string][]float32{
		"c1": unitVector(0),
		"c2": unitVector(1),
	})
	tags, err := s.EmbeddingTags()
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "test:model/text-v1" {
		t.Errorf("tags = %v, want [test:model/text-v1]", tags)
	}
}

// mixedSpec describes one chunk for the filter and FTS tests.
type mixedSpec struct {
	id        string
	kind      string // default ghost_post
	platform  string
	body      string
	published time.Time
	meta      map[string]any
	vector    []float32
}

// seedMixed writes each spec as its own item and embeds the vectors, so tests
// can mix Ghost and Postiz chunks with different dates and states.
func seedMixed(t *testing.T, specs []mixedSpec) *store.Store {
	t.Helper()
	s := newTestStore(t)
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	rows := make([]store.ChunkEmbedding, 0, len(specs))
	for _, spec := range specs {
		kind := spec.kind
		if kind == "" {
			kind = "ghost_post"
		}
		itemID := "item-" + spec.id
		chunk := store.Chunk{
			ChunkID: spec.id, SourceKind: kind, SourceID: itemID,
			Platform: spec.platform, Author: "Eli", PublishedAt: spec.published,
			UpdatedAt: at, ContentHash: "hash-" + spec.id, Title: "title " + spec.id,
			Body: spec.body, BodyTokens: 1, Meta: spec.meta,
		}
		if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: kind, ID: itemID},
			[]store.Chunk{chunk}); err != nil {
			t.Fatalf("seed %s: %v", spec.id, err)
		}
		if spec.vector != nil {
			rows = append(rows, store.ChunkEmbedding{
				ChunkID: spec.id, ContentHash: chunk.ContentHash, Vector: spec.vector,
			})
		}
	}
	if len(rows) > 0 {
		if _, _, err := s.WriteEmbeddings(rows, "test:model/text-v1", at); err != nil {
			t.Fatalf("seed embeddings: %v", err)
		}
	}
	return s
}

func TestSearchTextOrdersByBM25(t *testing.T) {
	s := seedMixed(t, []mixedSpec{
		{id: "c1", body: "harness vendor lock-in harness", published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{id: "c2", body: "harness vendor notes", published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{id: "c3", body: "unrelated text", published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	query, ok := store.FTSQuery("harness vendor")
	if !ok {
		t.Fatal("FTSQuery produced no query")
	}
	hits, err := s.SearchText(query, 10, store.Filter{})
	if err != nil {
		t.Fatalf("text search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2: %+v", len(hits), hits)
	}
	if hits[0].ChunkID != "c1" || hits[1].ChunkID != "c2" {
		t.Errorf("hits = %s, %s; want c1, c2 (two term hits beats one)", hits[0].ChunkID, hits[1].ChunkID)
	}
	if hits[0].BM25 >= 0 {
		t.Errorf("bm25 = %v, want a negative rank", hits[0].BM25)
	}
	if hits[0].Body != "harness vendor lock-in harness" {
		t.Errorf("body = %q, want the stored text", hits[0].Body)
	}
}

func TestSearchTextAppliesFilters(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	s := seedMixed(t, []mixedSpec{
		{id: "g1", body: "shared keyword ghost", published: day(1)},
		{id: "p1", kind: "postiz_post", platform: "linkedin", body: "shared keyword postiz", published: day(2)},
		{id: "p2", kind: "postiz_post", platform: "linkedin", body: "shared keyword scheduled", published: day(3),
			meta: map[string]any{"state": "QUEUE"}},
	})
	query, _ := store.FTSQuery("shared keyword")

	tests := []struct {
		name   string
		filter store.Filter
		want   []string
	}{
		{name: "no filter", filter: store.Filter{}, want: []string{"g1", "p1", "p2"}},
		{name: "source", filter: store.Filter{SourceKinds: []string{"postiz_post"}}, want: []string{"p1", "p2"}},
		{name: "platform", filter: store.Filter{Platforms: []string{"LinkedIn"}}, want: []string{"p1", "p2"}},
		{name: "after", filter: store.Filter{After: day(2).Format(time.RFC3339)}, want: []string{"p1", "p2"}},
		{name: "before", filter: store.Filter{Before: day(3).Format(time.RFC3339)}, want: []string{"g1", "p1"}},
		{name: "exclude scheduled", filter: store.Filter{ExcludeScheduled: true}, want: []string{"g1", "p1"}},
		{name: "combined", filter: store.Filter{
			SourceKinds: []string{"postiz_post"}, Platforms: []string{"linkedin"},
			After: day(2).Format(time.RFC3339), ExcludeScheduled: true,
		}, want: []string{"p1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := s.SearchText(query, 10, tc.filter)
			if err != nil {
				t.Fatalf("text search: %v", err)
			}
			got := make([]string, len(hits))
			for i, h := range hits {
				got[i] = h.ChunkID
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSearchVectorsAppliesFilters(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	s := seedMixed(t, []mixedSpec{
		{id: "g1", body: "ghost", published: day(1), vector: unitVector(0)},
		{id: "p1", kind: "postiz_post", platform: "linkedin", body: "postiz", published: day(2), vector: unitVector(0)},
		{id: "p2", kind: "postiz_post", platform: "linkedin", body: "scheduled", published: day(3),
			meta: map[string]any{"state": "QUEUE"}, vector: unitVector(0)},
	})
	tests := []struct {
		name   string
		filter store.Filter
		want   []string
	}{
		{name: "source", filter: store.Filter{SourceKinds: []string{"ghost_post"}}, want: []string{"g1"}},
		{name: "platform", filter: store.Filter{Platforms: []string{"linkedin"}}, want: []string{"p1", "p2"}},
		{name: "after", filter: store.Filter{After: day(3).Format(time.RFC3339)}, want: []string{"p2"}},
		{name: "before", filter: store.Filter{Before: day(2).Format(time.RFC3339)}, want: []string{"g1"}},
		{name: "exclude scheduled", filter: store.Filter{ExcludeScheduled: true}, want: []string{"g1", "p1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := s.SearchVectors(unitVector(0), 2, tc.filter)
			if err != nil {
				t.Fatalf("vector search: %v", err)
			}
			got := make([]string, len(hits))
			for i, h := range hits {
				got[i] = h.ChunkID
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("hits = %v, want %v", got, want)
			}
		})
	}
}

// TestSearchVectorsFilterIsExact proves a selective filter is not truncated by
// the KNN: the nearest k chunks overall are all ghost, but all postiz chunks
// must still be found.
func TestSearchVectorsFilterIsExact(t *testing.T) {
	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	specs := make([]mixedSpec, 0, 10)
	for i := 0; i < 6; i++ {
		specs = append(specs, mixedSpec{
			id: "ghost-" + string(rune('a'+i)), body: "ghost", published: day, vector: unitVector(0),
		})
	}
	for i := 0; i < 4; i++ {
		specs = append(specs, mixedSpec{
			id: "postiz-" + string(rune('a'+i)), kind: "postiz_post", platform: "linkedin",
			body: "postiz", published: day, vector: unitVector(1),
		})
	}
	s := seedMixed(t, specs)

	hits, err := s.SearchVectors(unitVector(0), 2, store.Filter{SourceKinds: []string{"postiz_post"}})
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d filtered hits, want 2 (KNN must over-fetch when filtering)", len(hits))
	}
	for _, h := range hits {
		if h.SourceKind != "postiz_post" {
			t.Errorf("hit %s has kind %s, want postiz_post", h.ChunkID, h.SourceKind)
		}
	}
}
