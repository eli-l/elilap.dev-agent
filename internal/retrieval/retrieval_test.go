package retrieval_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// unitVector is a one-hot vector in the embedding space.
func unitVector(axis int) []float32 {
	v := make([]float32, store.EmbeddingDims)
	v[axis] = 1
	return v
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedIndex writes n one-hot chunks and embeds chunk i with unitVector(i).
func seedIndex(t *testing.T, n int) *store.Store {
	t.Helper()
	s := newTestStore(t)
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	chunks := make([]store.Chunk, n)
	rows := make([]store.ChunkEmbedding, n)
	for i := 0; i < n; i++ {
		id := "c" + string(rune('0'+i))
		chunks[i] = store.Chunk{
			ChunkID: id, SourceKind: "ghost_post", SourceID: "uuid-1",
			SourceURL: "https://read.elilap.dev/" + id, Author: "Eli",
			PublishedAt: at, UpdatedAt: at, ContentHash: "hash-" + id,
			Title: "title " + id, Section: "intro", Body: "body " + id, BodyTokens: 2,
		}
		rows[i] = store.ChunkEmbedding{ChunkID: id, ContentHash: "hash-" + id, Vector: unitVector(i)}
	}
	if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}, chunks); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	if _, _, err := s.WriteEmbeddings(rows, "test:model/text-v1", at); err != nil {
		t.Fatalf("seed embeddings: %v", err)
	}
	return s
}

type fakeEmbedder struct {
	vector []float32
	err    error
	model  string
	inputs []string
	calls  int
}

func (f *fakeEmbedder) Embed(_ context.Context, model string, inputs []string) ([][]float32, error) {
	f.calls++
	f.model = model
	f.inputs = inputs
	if f.err != nil {
		return nil, f.err
	}
	return [][]float32{f.vector}, nil
}

func TestSearchReturnsRankedHits(t *testing.T) {
	s := seedIndex(t, 2)
	emb := &fakeEmbedder{vector: unitVector(0)}
	searcher := &retrieval.Searcher{Store: s, Embedder: emb, Model: "test-model"}

	hits, err := searcher.Search(context.Background(), retrieval.Query{Text: "  hello  ", TopN: 2})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].ChunkID != "c0" || hits[1].ChunkID != "c1" {
		t.Errorf("hits = %s, %s; want c0, c1", hits[0].ChunkID, hits[1].ChunkID)
	}
	// Hybrid mode ranks by RRF, so the top hit only needs to outrank the
	// second; the path-native cosine values carry the similarity detail.
	if hits[0].Score <= hits[1].Score {
		t.Errorf("scores = %v, %v; want the top hit to rank higher", hits[0].Score, hits[1].Score)
	}
	if hits[0].Cosine == nil || *hits[0].Cosine < 0.999 {
		t.Errorf("self cosine = %v, want 1", hits[0].Cosine)
	}
	if hits[1].Cosine == nil || math.Abs(*hits[1].Cosine) > 1e-6 {
		t.Errorf("orthogonal cosine = %v, want 0", hits[1].Cosine)
	}
	if hits[0].Title != "title c0" || hits[0].Body != "body c0" {
		t.Errorf("hit text = %q/%q", hits[0].Title, hits[0].Body)
	}
	if hits[0].SourceURL != "https://read.elilap.dev/c0" || hits[0].Author != "Eli" {
		t.Errorf("hit metadata = %q/%q", hits[0].SourceURL, hits[0].Author)
	}
	if hits[0].PublishedAt != "2026-11-03T12:00:00Z" {
		t.Errorf("published_at = %q", hits[0].PublishedAt)
	}
	// The embedder receives the trimmed query, exactly once, with the model
	// the searcher was configured with.
	if emb.calls != 1 || emb.model != "test-model" {
		t.Errorf("embed calls/model = %d/%q, want 1/test-model", emb.calls, emb.model)
	}
	if len(emb.inputs) != 1 || emb.inputs[0] != "hello" {
		t.Errorf("embed inputs = %q, want [hello]", emb.inputs)
	}
}

func TestSearchDefaultTopN(t *testing.T) {
	s := seedIndex(t, 6)
	searcher := &retrieval.Searcher{Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m"}
	hits, err := searcher.Search(context.Background(), retrieval.Query{Text: "q"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != retrieval.DefaultTopN {
		t.Errorf("got %d hits, want the default %d", len(hits), retrieval.DefaultTopN)
	}
}

func TestSearchEmptyResultIsNotNil(t *testing.T) {
	s := newTestStore(t)
	searcher := &retrieval.Searcher{Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m"}
	hits, err := searcher.Search(context.Background(), retrieval.Query{Text: "q"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hits == nil {
		t.Error("empty result is nil; JSON callers would get null instead of []")
	}
	if len(hits) != 0 {
		t.Errorf("got %d hits on an empty index, want 0", len(hits))
	}
}

func TestSearchRejectsBadQueries(t *testing.T) {
	s := newTestStore(t)
	searcher := &retrieval.Searcher{Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m"}
	tests := []struct {
		name string
		q    retrieval.Query
		want string
	}{
		{name: "empty", q: retrieval.Query{Text: "   "}, want: "must not be empty"},
		{name: "negative top_n", q: retrieval.Query{Text: "q", TopN: -1}, want: "must not be negative"},
		{name: "top_n over max", q: retrieval.Query{Text: "q", TopN: retrieval.MaxTopN + 1}, want: "exceeds the maximum"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := searcher.Search(context.Background(), tc.q)
			if !errors.Is(err, retrieval.ErrInvalidQuery) {
				t.Fatalf("err = %v, want ErrInvalidQuery", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSearchPropagatesEmbeddingFailure(t *testing.T) {
	s := newTestStore(t)
	searcher := &retrieval.Searcher{
		Store:    s,
		Embedder: &fakeEmbedder{err: errors.New("backend down")},
		Model:    "m",
	}
	_, err := searcher.Search(context.Background(), retrieval.Query{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("err = %v, want the backend failure", err)
	}
	if errors.Is(err, retrieval.ErrInvalidQuery) {
		t.Error("a backend failure was classified as a caller error")
	}
}

func TestSearchRejectsWrongVectorDimensions(t *testing.T) {
	s := newTestStore(t)
	searcher := &retrieval.Searcher{
		Store:    s,
		Embedder: &fakeEmbedder{vector: make([]float32, 3)},
		Model:    "m",
	}
	if _, err := searcher.Search(context.Background(), retrieval.Query{Text: "q"}); err == nil {
		t.Fatal("a 3-dimension query vector was accepted")
	}
}

// spec describes one chunk for the hybrid and filter tests.
type spec struct {
	id        string
	kind      string // default ghost_post
	platform  string
	body      string
	published time.Time
	meta      map[string]any
	vector    []float32
}

// seedRetrieval writes each spec as its own item and embeds the vectors.
func seedRetrieval(t *testing.T, specs []spec) *store.Store {
	t.Helper()
	s := newTestStore(t)
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	rows := make([]store.ChunkEmbedding, 0, len(specs))
	for _, sp := range specs {
		kind := sp.kind
		if kind == "" {
			kind = "ghost_post"
		}
		itemID := "item-" + sp.id
		chunk := store.Chunk{
			ChunkID: sp.id, SourceKind: kind, SourceID: itemID, Platform: sp.platform,
			Author: "Eli", PublishedAt: sp.published, UpdatedAt: at,
			ContentHash: "hash-" + sp.id, Title: "title " + sp.id,
			Body: sp.body, BodyTokens: 1, Meta: sp.meta,
		}
		if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: kind, ID: itemID},
			[]store.Chunk{chunk}); err != nil {
			t.Fatalf("seed %s: %v", sp.id, err)
		}
		rows = append(rows, store.ChunkEmbedding{
			ChunkID: sp.id, ContentHash: chunk.ContentHash, Vector: sp.vector,
		})
	}
	if _, _, err := s.WriteEmbeddings(rows, "test:model/text-v1", at); err != nil {
		t.Fatalf("seed embeddings: %v", err)
	}
	return s
}

func scaled(v []float32, f float32) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * f
	}
	return out
}

func hitIDs(hits []retrieval.Hit) string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ChunkID
	}
	return strings.Join(ids, ",")
}

func TestSearchHybridMergesPaths(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := seedRetrieval(t, []spec{
		// Nearest vector, no text match.
		{id: "c0", body: "nothing relevant", published: at, vector: unitVector(0)},
		// Matches both query terms and is vector rank 3.
		{id: "c1", body: "alpha beta", published: at, vector: unitVector(1)},
		// Matches one term and is vector rank 2.
		{id: "c2", body: "alpha", published: at, vector: scaled(unitVector(1), 0.5)},
	})
	searcher := &retrieval.Searcher{Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m"}

	hits, err := searcher.Search(context.Background(), retrieval.Query{Text: "alpha beta", TopN: 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// c1 wins on the text rank plus a vector contribution; c2 ties the two
	// ranks but loses the second term; c0 has only the vector path.
	if got := hitIDs(hits); got != "c1,c2,c0" {
		t.Errorf("hybrid order = %s, want c1,c2,c0", got)
	}
	byID := map[string]retrieval.Hit{}
	for _, h := range hits {
		byID[h.ChunkID] = h
	}
	if byID["c1"].BM25 == nil || byID["c1"].Cosine == nil || byID["c1"].Distance == nil {
		t.Errorf("c1 should carry bm25, cosine, and distance: %+v", byID["c1"])
	}
	if byID["c0"].BM25 != nil {
		t.Errorf("c0 has no text match but carries bm25=%v", *byID["c0"].BM25)
	}
	if byID["c0"].Cosine == nil || byID["c0"].Distance == nil {
		t.Errorf("c0 should carry vector scores: %+v", byID["c0"])
	}
}

func TestSearchVectorOnlyUsesCosine(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := seedRetrieval(t, []spec{
		{id: "c0", body: "alpha", published: at, vector: unitVector(0)},
		{id: "c1", body: "alpha", published: at, vector: unitVector(1)},
	})
	searcher := &retrieval.Searcher{Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m"}

	hits, err := searcher.Search(context.Background(), retrieval.Query{
		Text: "alpha", TopN: 2, VectorOnly: true,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := hitIDs(hits); got != "c0,c1" {
		t.Fatalf("vector order = %s, want c0,c1", got)
	}
	if hits[0].Cosine == nil || hits[0].Score != *hits[0].Cosine {
		t.Errorf("vector-only score = %v, cosine = %v; want the cosine", hits[0].Score, hits[0].Cosine)
	}
	if hits[0].BM25 != nil {
		t.Error("vector-only search returned a text score")
	}
}

func TestSearchAppliesFilters(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	s := seedRetrieval(t, []spec{
		{id: "g1", body: "shared keyword ghost", published: day(1), vector: unitVector(0)},
		{id: "p1", kind: "postiz_post", platform: "linkedin", body: "shared keyword postiz", published: day(2), vector: unitVector(0)},
		{id: "p2", kind: "postiz_post", platform: "linkedin", body: "shared keyword queue", published: day(3),
			meta: map[string]any{"state": "QUEUE"}, vector: unitVector(0)},
	})
	searcher := &retrieval.Searcher{Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m"}

	tests := []struct {
		name string
		q    retrieval.Query
		want string
	}{
		{name: "source postiz", q: retrieval.Query{Source: "postiz"}, want: "p1,p2"},
		{name: "source ghost", q: retrieval.Query{Source: "ghost"}, want: "g1"},
		{name: "platform", q: retrieval.Query{Platforms: []string{"LinkedIn"}}, want: "p1,p2"},
		{name: "exclude scheduled", q: retrieval.Query{ExcludeScheduled: true}, want: "g1,p1"},
		{name: "after date", q: retrieval.Query{After: "2026-10-02"}, want: "p1,p2"},
		{name: "before date includes the day", q: retrieval.Query{Before: "2026-10-02"}, want: "g1,p1"},
		{name: "combined", q: retrieval.Query{
			Source: "postiz", Platforms: []string{"linkedin"}, After: "2026-10-02", ExcludeScheduled: true,
		}, want: "p1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.q.Text = "shared keyword"
			tc.q.TopN = 5
			hits, err := searcher.Search(context.Background(), tc.q)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := hitIDs(hits); got != tc.want {
				t.Errorf("hits = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSearchRecencyWeighting(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := seedRetrieval(t, []spec{
		{id: "a-old", body: "keyword", published: time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC), vector: unitVector(0)},
		{id: "z-new", body: "keyword", published: now.AddDate(0, 0, -1), vector: unitVector(0)},
	})
	searcher := &retrieval.Searcher{
		Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m",
		Now: func() time.Time { return now },
	}

	// Equal RRF scores: the chunk_id tie-break puts a-old first.
	hits, err := searcher.Search(context.Background(), retrieval.Query{Text: "keyword", TopN: 2})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := hitIDs(hits); got != "a-old,z-new" {
		t.Errorf("without recency = %s, want a-old,z-new", got)
	}

	// A 30-day half-life decays the six-year-old chunk far below the one-day-old.
	hits, err = searcher.Search(context.Background(), retrieval.Query{
		Text: "keyword", TopN: 2, RecencyHalflifeDays: 30,
	})
	if err != nil {
		t.Fatalf("search with recency: %v", err)
	}
	if got := hitIDs(hits); got != "z-new,a-old" {
		t.Errorf("with recency = %s, want z-new,a-old", got)
	}
}

func TestSearchRejectsBadFilters(t *testing.T) {
	s := newTestStore(t)
	searcher := &retrieval.Searcher{Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "m"}
	tests := []struct {
		name string
		q    retrieval.Query
		want string
	}{
		{name: "source", q: retrieval.Query{Text: "q", Source: "twitter"}, want: "source must be"},
		{name: "after", q: retrieval.Query{Text: "q", After: "yesterday"}, want: "after:"},
		{name: "before", q: retrieval.Query{Text: "q", Before: "2026-13-01"}, want: "before:"},
		{name: "range", q: retrieval.Query{Text: "q", After: "2026-11-02", Before: "2026-11-01"}, want: "earlier than before"},
		{name: "halflife", q: retrieval.Query{Text: "q", RecencyHalflifeDays: -1}, want: "must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := searcher.Search(context.Background(), tc.q)
			if !errors.Is(err, retrieval.ErrInvalidQuery) {
				t.Fatalf("err = %v, want ErrInvalidQuery", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
