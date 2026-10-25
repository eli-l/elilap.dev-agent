package retrieval_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

// fakeBM25 is a BM25Searcher that returns a fixed list.
type fakeBM25 struct {
	hits  []retrieval.Hit
	err   error
	calls int
	query string
	limit int
}

func (f *fakeBM25) SearchBM25(ctx context.Context, query string, limit int) ([]retrieval.Hit, error) {
	f.calls++
	f.query = query
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

// fakeVector is a VectorSearcher that returns a fixed list.
type fakeVector struct {
	hits  []retrieval.Hit
	err   error
	calls int
	limit int
}

func (f *fakeVector) SearchVector(ctx context.Context, q []float32, limit int) ([]retrieval.Hit, error) {
	f.calls++
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func hit(id string) retrieval.Hit {
	return retrieval.Hit{ChunkID: id, Title: "Title " + id, SourceURL: "https://example.test/" + id}
}

func ids(hits []retrieval.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ChunkID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRetrieveFusesListsThatAgree(t *testing.T) {
	bm25 := &fakeBM25{hits: []retrieval.Hit{hit("a"), hit("b"), hit("c")}}
	vec := &fakeVector{hits: []retrieval.Hit{hit("a"), hit("b"), hit("c")}}

	got, err := retrieval.Retrieve(context.Background(), bm25, vec, []float32{0.1, 0.2}, "query", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	want := []string{"a", "b", "c"}
	if !equal(ids(got), want) {
		t.Fatalf("fused order = %v, want %v", ids(got), want)
	}
	if got[0].Score <= got[1].Score || got[1].Score <= got[2].Score {
		t.Errorf("scores are not descending: %v", []float64{got[0].Score, got[1].Score, got[2].Score})
	}
	// The hit both lists rank first carries 1/61 + 1/61.
	wantScore := 2.0 / 61.0
	if diff := got[0].Score - wantScore; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("score of the agreed top hit = %v, want %v", got[0].Score, wantScore)
	}
}

func TestRetrieveFusesListsThatDisagree(t *testing.T) {
	// Lexical: a, b, c. Semantic: c, d.
	// Scores: c = 1/63 + 1/61 = 0.032266, a = 1/61 = 0.016393,
	// b = 1/62 = 0.016129, d = 1/62 = 0.016129. b and d tie, and b was
	// seen first, so b wins the tie.
	bm25 := &fakeBM25{hits: []retrieval.Hit{hit("a"), hit("b"), hit("c")}}
	vec := &fakeVector{hits: []retrieval.Hit{hit("c"), hit("d")}}

	got, err := retrieval.Retrieve(context.Background(), bm25, vec, []float32{0.1}, "query", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	want := []string{"c", "a", "b", "d"}
	if !equal(ids(got), want) {
		t.Fatalf("fused order = %v, want %v", ids(got), want)
	}
}

func TestRetrieveRespectsTheLimit(t *testing.T) {
	bm25 := &fakeBM25{hits: []retrieval.Hit{hit("a"), hit("b"), hit("c")}}
	vec := &fakeVector{hits: []retrieval.Hit{hit("a"), hit("b"), hit("c")}}

	got, err := retrieval.Retrieve(context.Background(), bm25, vec, []float32{0.1}, "query", 2)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2", len(got))
	}
	if !equal(ids(got), []string{"a", "b"}) {
		t.Errorf("fused order = %v, want [a b]", ids(got))
	}
}

func TestRetrievePropagatesSearcherErrors(t *testing.T) {
	sentinel := errors.New("index is corrupt")
	bm25 := &fakeBM25{err: sentinel}

	_, err := retrieval.Retrieve(context.Background(), bm25, &fakeVector{}, []float32{0.1}, "query", 5)
	if err == nil {
		t.Fatal("Retrieve returned no error, want one")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap the searcher error", err)
	}
	if !strings.Contains(err.Error(), "BM25") {
		t.Errorf("error %q does not name the failing side", err.Error())
	}
}

func TestRetrievePropagatesVectorSearcherErrors(t *testing.T) {
	sentinel := errors.New("vector index is corrupt")
	vec := &fakeVector{err: sentinel}

	_, err := retrieval.Retrieve(context.Background(), &fakeBM25{}, vec, []float32{0.1}, "query", 5)
	if err == nil {
		t.Fatal("Retrieve returned no error, want one")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap the searcher error", err)
	}
}

func TestRetrieveWithOnlyOneSearcher(t *testing.T) {
	vec := &fakeVector{hits: []retrieval.Hit{hit("a"), hit("b")}}

	got, err := retrieval.Retrieve(context.Background(), nil, vec, []float32{0.1}, "query", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !equal(ids(got), []string{"a", "b"}) {
		t.Errorf("fused order = %v, want [a b]", ids(got))
	}

	bm25 := &fakeBM25{hits: []retrieval.Hit{hit("c")}}
	got, err = retrieval.Retrieve(context.Background(), bm25, nil, nil, "query", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !equal(ids(got), []string{"c"}) {
		t.Errorf("fused order = %v, want [c]", ids(got))
	}
}

func TestRetrieveSkipsTheSideWithNothingToSearch(t *testing.T) {
	bm25 := &fakeBM25{hits: []retrieval.Hit{hit("a")}}
	vec := &fakeVector{hits: []retrieval.Hit{hit("b")}}

	// An empty text query means no lexical search, even with a searcher.
	got, err := retrieval.Retrieve(context.Background(), bm25, vec, []float32{0.1}, "   ", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if bm25.calls != 0 {
		t.Errorf("BM25 was called %d times, want 0", bm25.calls)
	}
	if !equal(ids(got), []string{"b"}) {
		t.Errorf("fused order = %v, want [b]", ids(got))
	}

	// An empty vector means no semantic search.
	bm25 = &fakeBM25{hits: []retrieval.Hit{hit("a")}}
	vec = &fakeVector{hits: []retrieval.Hit{hit("b")}}
	got, err = retrieval.Retrieve(context.Background(), bm25, vec, nil, "query", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if vec.calls != 0 {
		t.Errorf("vector search was called %d times, want 0", vec.calls)
	}
	if !equal(ids(got), []string{"a"}) {
		t.Errorf("fused order = %v, want [a]", ids(got))
	}
}

func TestRetrieveWithoutAnythingToSearchIsAnError(t *testing.T) {
	_, err := retrieval.Retrieve(context.Background(), &fakeBM25{}, &fakeVector{}, nil, "  ", 10)
	if err == nil {
		t.Fatal("Retrieve returned no error, want one")
	}
	if !strings.Contains(err.Error(), "nothing to search for") {
		t.Errorf("error %q does not explain what is missing", err.Error())
	}
}

func TestRetrieveWithoutAnySearcherIsAnError(t *testing.T) {
	_, err := retrieval.Retrieve(context.Background(), nil, nil, []float32{0.1}, "query", 10)
	if err == nil {
		t.Fatal("Retrieve returned no error, want one")
	}
	if !strings.Contains(err.Error(), "no searcher") {
		t.Errorf("error %q does not explain what is missing", err.Error())
	}
}

func TestRetrieveRejectsAHitWithoutAChunkID(t *testing.T) {
	bm25 := &fakeBM25{hits: []retrieval.Hit{{Title: "no id"}}}

	_, err := retrieval.Retrieve(context.Background(), bm25, &fakeVector{}, []float32{0.1}, "query", 10)
	if err == nil {
		t.Fatal("Retrieve returned no error, want one")
	}
	if !strings.Contains(err.Error(), "empty chunk id") {
		t.Errorf("error %q does not name the problem", err.Error())
	}
}

func TestRetrieveKeepsTheFirstSeenMetadata(t *testing.T) {
	first := retrieval.Hit{ChunkID: "a", Title: "lexical title", SourceURL: "https://lexical.test/"}
	second := retrieval.Hit{ChunkID: "a", Title: "semantic title", SourceURL: "https://semantic.test/"}
	bm25 := &fakeBM25{hits: []retrieval.Hit{first}}
	vec := &fakeVector{hits: []retrieval.Hit{second}}

	got, err := retrieval.Retrieve(context.Background(), bm25, vec, []float32{0.1}, "query", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hits, want 1", len(got))
	}
	if got[0].Title != "lexical title" || got[0].SourceURL != "https://lexical.test/" {
		t.Errorf("hit kept %q / %q, want the first-seen metadata", got[0].Title, got[0].SourceURL)
	}
}
