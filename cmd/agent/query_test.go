package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

// runQueryTest invokes the command with a throwaway DB and returns the exit
// code plus captured output.
func runQueryTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runQuery(args, envcfg.Config{
		DBPath:       filepath.Join(t.TempDir(), "unused.db"),
		EmbedBaseURL: envcfg.DefaultModelBaseURL, ChatBaseURL: envcfg.DefaultModelBaseURL,
		EmbedModel: envcfg.DefaultEmbedModel,
	}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunQueryRejectsUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no query", args: nil, want: "provide a query"},
		{name: "format", args: []string{"--format", "yaml", "q"}, want: "--format must be text or jsonl"},
		{name: "negative top", args: []string{"--top", "-1", "q"}, want: "--top must not be negative"},
		{name: "zero top", args: []string{"--top", "0", "q"}, want: "--top must be at least 1"},
		{name: "timeout", args: []string{"--timeout", "0s", "q"}, want: "--timeout must be positive"},
		{name: "halflife", args: []string{"--recency-halflife-days", "-1", "q"}, want: "must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runQueryTest(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2 (usage error)", code)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
		})
	}
}

func TestRunQueryRequiresIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	var stdout, stderr bytes.Buffer
	code := runQuery([]string{"--db", dbPath, "q"}, envcfg.Config{
		DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel,
	}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 for a missing index", code)
	}
	if !strings.Contains(stderr.String(), "no index at") {
		t.Errorf("stderr = %q, want the missing-index hint", stderr.String())
	}
}

func TestRunQueryInvalidFilterIsUsageError(t *testing.T) {
	// A seeded index lets the command pass the model check; the filter is
	// validated before any embeddings request, so no model host is needed.
	dbPath := seedServeIndex(t, embed.Tag(envcfg.DefaultEmbedModel))
	var stdout, stderr bytes.Buffer
	code := runQuery([]string{"--db", dbPath, "--source", "twitter", "q"}, envcfg.Config{
		DBPath: dbPath, EmbedBaseURL: envcfg.DefaultModelBaseURL, ChatBaseURL: envcfg.DefaultModelBaseURL,
		EmbedModel: envcfg.DefaultEmbedModel,
	}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2 for an invalid source", code)
	}
	if !strings.Contains(stderr.String(), "source must be") {
		t.Errorf("stderr = %q, want the source validation error", stderr.String())
	}
}

func TestPrintQueryHitsJSONL(t *testing.T) {
	dist, bm25, cosine := 0.5, -3.0, 0.75
	hits := []retrieval.Hit{
		{ChunkID: "c1", Body: "one", Score: 0.02, Distance: &dist, Cosine: &cosine},
		{ChunkID: "c2", Body: "two", Score: 0.01, BM25: &bm25},
	}
	var buf bytes.Buffer
	if err := printQueryHits(&buf, "jsonl", hits); err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var got []retrieval.Hit
	for _, line := range lines {
		var h retrieval.Hit
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			t.Fatalf("decode line: %v", err)
		}
		got = append(got, h)
	}
	if !reflect.DeepEqual(got, hits) {
		t.Errorf("round-trip hits = %+v, want %+v", got, hits)
	}
}

func TestPrintQueryHitsText(t *testing.T) {
	cosine := 0.75
	hits := []retrieval.Hit{{
		ChunkID: "c1", SourceKind: "ghost_post", SourceURL: "https://x",
		Title: "A title", Section: "A section", Body: "line one\nline two",
		Score: 0.02, Cosine: &cosine,
	}}
	var buf bytes.Buffer
	if err := printQueryHits(&buf, "text", hits); err != nil {
		t.Fatalf("text: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"1. score 0.020000", "cosine 0.7500", "A title / A section", "https://x", "chunk_id: c1", "line one\n   line two"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}

	buf.Reset()
	if err := printQueryHits(&buf, "text", nil); err != nil {
		t.Fatalf("text empty: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "no results" {
		t.Errorf("empty text output = %q, want no results", buf.String())
	}
}

// TestQueryJSONLMatchesRetrieveHandler is the CLI/HTTP parity check: both
// render the same retrieval.Hit slice, so the same params produce the same
// objects.
func TestQueryJSONLMatchesRetrieveHandler(t *testing.T) {
	dist, bm25, cosine := 0.5, -3.0, 0.75
	hits := []retrieval.Hit{{
		ChunkID: "c1", SourceKind: "ghost_post", SourceID: "uuid-1",
		SourceURL: "https://x", Author: "Eli", PublishedAt: "2026-11-03T12:00:00Z",
		Title: "t", Body: "b", Score: 0.02, Distance: &dist, BM25: &bm25, Cosine: &cosine,
	}}

	var cli bytes.Buffer
	if err := printQueryHits(&cli, "jsonl", hits); err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	var fromCLI []retrieval.Hit
	dec := json.NewDecoder(&cli)
	for {
		var h retrieval.Hit
		if err := dec.Decode(&h); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode jsonl: %v", err)
		}
		fromCLI = append(fromCLI, h)
	}

	rec := postRetrieve(t, newRetrieveHandler(&stubRetriever{hits: hits}), `{"query":"q"}`)
	if rec.Code != 200 {
		t.Fatalf("handler status = %d", rec.Code)
	}
	var fromHTTP []retrieval.Hit
	if err := json.Unmarshal(rec.Body.Bytes(), &fromHTTP); err != nil {
		t.Fatalf("decode http: %v", err)
	}

	if !reflect.DeepEqual(fromCLI, fromHTTP) {
		t.Errorf("CLI jsonl = %+v\nHTTP array = %+v", fromCLI, fromHTTP)
	}
}
