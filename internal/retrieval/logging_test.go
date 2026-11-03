package retrieval_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

// debugLog installs a JSON logger into a buffer for the duration of one test.
func debugLog(t *testing.T, level string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := logging.Setup(logging.Config{Level: level, Format: "json"}, &buf); err != nil {
		t.Fatalf("setup logging: %v", err)
	}
	t.Cleanup(logging.Reset)
	return &buf
}

// records decodes the buffered JSON log lines, grouped by message.
func records(t *testing.T, buf *bytes.Buffer) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		msg, _ := rec["message"].(string)
		out[msg] = append(out[msg], rec)
	}
	return out
}

func TestDebugLogsTheRetrievalTrace(t *testing.T) {
	buf := debugLog(t, "debug")
	s := seedIndex(t, 2)
	searcher := &retrieval.Searcher{
		Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "test-model",
	}
	if _, err := searcher.Search(context.Background(), retrieval.Query{
		Text: "body c0", TopN: 2, RecencyHalflifeDays: 30,
	}); err != nil {
		t.Fatalf("search: %v", err)
	}

	recs := records(t, buf)
	for _, msg := range []string{"retrieval: query", "retrieval: candidates", "retrieval: hit", "retrieval: done"} {
		if len(recs[msg]) == 0 {
			t.Errorf("no %q line", msg)
		}
	}

	// The query line names the text, the filter shape, and the embedding model
	// that turned the text into the vector.
	query := recs["retrieval: query"][0]
	if query["query"] != "body c0" || query["embed_model"] != "test-model" ||
		query["top_n"] != float64(2) || query["recency_halflife_days"] != float64(30) {
		t.Errorf("query line = %v", query)
	}

	// The hit lines carry the rank, id, and path-native scores a debug run needs
	// to explain the ordering.
	hits := recs["retrieval: hit"]
	if len(hits) != 2 {
		t.Fatalf("hit lines = %d, want 2", len(hits))
	}
	if hits[0]["chunk_id"] != "c0" || hits[0]["rank"] != float64(1) {
		t.Errorf("best hit = %v, want c0 at rank 1", hits[0])
	}
	if _, ok := hits[0]["cosine"]; !ok {
		t.Errorf("best hit has no cosine: %v", hits[0])
	}
	if preview, _ := hits[0]["preview"].(string); !strings.Contains(preview, "body c0") {
		t.Errorf("preview = %q, want the excerpt", preview)
	}
}

func TestInfoLevelLogsNoRetrievalTrace(t *testing.T) {
	buf := debugLog(t, "info")
	s := seedIndex(t, 2)
	buf.Reset() // drop the store's own info lines; only the query is under test
	searcher := &retrieval.Searcher{
		Store: s, Embedder: &fakeEmbedder{vector: unitVector(0)}, Model: "test-model",
	}
	if _, err := searcher.Search(context.Background(), retrieval.Query{Text: "body c0"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("info level wrote retrieval payload lines:\n%s", got)
	}
}
