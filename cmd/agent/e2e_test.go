package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
)

// TestE2EConfigToDoctor walks the CLI surface end to end against fixtures:
// a local sources.yaml, a fake OpenAI-compatible model server, and a fake
// Ghost endpoint. No live API is contacted and no credential value is
// printed.
//
// The surface it covers is exactly what exists in this phase: config
// loading and strict validation, the sources listing, and the doctor
// preflight. Ingest, embed and query land with the store layer in Phase 1;
// this test is the harness they extend.
func TestE2EConfigToDoctor(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sources.yaml")
	cfg := `version: 1

defaults:
  chunking: { target_tokens: 400, overlap_tokens: 60 }
  user_agent: "elilap-dev-agent/0.1 (+https://elilap.dev)"

sources:
  - id: ghost
    type: ghost
    enabled: true
    credentials:
      api_url: { env: AGENT_TEST_GHOST_URL }
      api_key: { env: AGENT_TEST_GHOST_KEY }
    options: { include_drafts: false, limit: 500 }

  - id: postiz
    type: postiz
    enabled: true
    credentials:
      api_url: { env: AGENT_TEST_POSTIZ_URL }
      api_key: { env: AGENT_TEST_POSTIZ_KEY }
    options: { platforms: [instagram], include_scheduled: false }
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}

	// Fake upstream: a Ghost admin endpoint that answers a posts request.
	ghost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"posts":[]}`))
	}))
	defer ghost.Close()

	// Fake model server with the two models the plan requires.
	model := fakeModelServer(t, "embeddinggemma-300m", "spark-x2.5-4b")

	const secret = "e2e-secret-value"
	t.Setenv("AGENT_TEST_GHOST_URL", ghost.URL+"/ghost")
	t.Setenv("AGENT_TEST_GHOST_KEY", secret)
	t.Setenv("AGENT_TEST_POSTIZ_URL", ghost.URL+"/postiz")
	t.Setenv("AGENT_TEST_POSTIZ_KEY", secret)
	t.Setenv("LOCALAI_BASE_URL", model.URL+"/v1")
	t.Setenv("EMBED_MODEL", "embeddinggemma-300m")
	t.Setenv("CHAT_MODEL", "spark-x2.5-4b")
	t.Setenv("AGENT_DB_PATH", filepath.Join(dir, "index.db"))

	// 1. Strict validation of the fixture config.
	code, out := runCLI(t, "sources", "validate", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("sources validate exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "ok (version 1, 2 source(s), 2 enabled") {
		t.Errorf("sources validate output unexpected\n%s", out)
	}

	// 2. Listing resolves the fixture endpoints and never prints a secret.
	code, out = runCLI(t, "sources", "list", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("sources list exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "api_url: env AGENT_TEST_GHOST_URL (set)") {
		t.Errorf("sources list did not resolve the fixture endpoint\n%s", out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("sources list leaked a credential value:\n%s", out)
	}

	// 3. The doctor passes against the fixture model server.
	code, out = runCLI(t, "doctor", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\n%s", code, out)
	}
	for _, want := range []string{
		"credentials: every referenced environment variable is set",
		"model server: " + model.URL + "/v1 is reachable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Fatalf("doctor leaked a credential value:\n%s", out)
	}

	// 4. A missing credential is reported by name, not by value.
	os.Unsetenv("AGENT_TEST_POSTIZ_KEY")
	code, out = runCLI(t, "sources", "validate", "--config", cfgPath, "--strict")
	if code != 1 {
		t.Fatalf("sources validate --strict exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "AGENT_TEST_POSTIZ_KEY is not set") {
		t.Errorf("strict validation did not name the missing variable\n%s", out)
	}
}

// fakeEmbeddingServer serves an OpenAI-compatible /v1/embeddings endpoint.
//
// The vectors are derived from the words of the text: every word hashes to
// one bucket of the 768-dimensional vector, signed by the word's hash. Two
// texts that share a word are therefore close, and two texts that share no
// word are orthogonal. That is enough to exercise the hybrid retrieval
// without a model server, and it is deterministic: the same text always
// gets the same vector.
func fakeEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, 0, len(request.Input))
		for i, text := range request.Input {
			data = append(data, map[string]any{
				"object":    "embedding",
				"index":     i,
				"embedding": fakeEmbedding(text),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeEmbedding is the deterministic vector of a text.
func fakeEmbedding(text string) []float32 {
	vector := make([]float32, embed.Dimensions)
	for _, word := range strings.Fields(strings.ToLower(text)) {
		bucket, sign := embeddingBucket(word)
		vector[bucket] += sign
	}
	return vector
}

// embeddingBucket is the bucket and the sign of a word.
func embeddingBucket(word string) (int, float32) {
	sum := sha256.Sum256([]byte(strings.ToLower(word)))
	bucket := (int(sum[0])<<8 | int(sum[1])) % embed.Dimensions
	sign := float32(1)
	if sum[2]&1 == 1 {
		sign = -1
	}
	return bucket, sign
}

// unusedWord returns a word that shares no embedding bucket with any of the
// given words, so its vector is orthogonal to the corpus. It keeps the
// "nothing matches" case free of a hash collision.
func unusedWord(t *testing.T, used []string) string {
	t.Helper()
	busy := make(map[int]bool, len(used))
	for _, word := range used {
		bucket, _ := embeddingBucket(word)
		busy[bucket] = true
	}
	for i := 0; i < 1000; i++ {
		candidate := fmt.Sprintf("nonexistentterm%d", i)
		bucket, _ := embeddingBucket(candidate)
		if !busy[bucket] {
			return candidate
		}
	}
	t.Fatal("no free embedding bucket for the test word")
	return ""
}

// TestE2EIngestEmbedQuery walks the whole pipeline: a directory of markdown
// files is ingested into a temporary index, the chunks are embedded by a
// fake model server, and a query finds the file it asks about.
//
// No network, no credential and no live API are involved: the only server
// is the httptest one above.
func TestE2EIngestEmbedQuery(t *testing.T) {
	dir := t.TempDir()
	corpus := filepath.Join(dir, "corpus")
	if err := os.MkdirAll(corpus, 0o750); err != nil {
		t.Fatalf("create the corpus directory: %v", err)
	}
	writeCorpusFile(t, filepath.Join(corpus, "alpha.md"), "# Alpha Report\n\nalpha retrieval notes\n")
	writeCorpusFile(t, filepath.Join(corpus, "beta.md"), "# Beta Report\n\nbeta caching notes\n")

	configPath := filepath.Join(dir, "sources.yaml")
	config := fmt.Sprintf(`version: 1

defaults:
  chunking: { target_tokens: 400, overlap_tokens: 60 }

sources:
  - id: files
    type: files
    enabled: true
    options: { dir: %q }
`, corpus)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write the fixture config: %v", err)
	}

	model := fakeEmbeddingServer(t)
	t.Setenv("LOCALAI_BASE_URL", model.URL+"/v1")
	t.Setenv("EMBED_MODEL", "embeddinggemma-300m")
	t.Setenv("AGENT_DB_PATH", filepath.Join(dir, "index.db"))

	// 1. The ingest imports both markdown files.
	code, out := runCLI(t, "ingest", "--config", configPath)
	if code != 0 {
		t.Fatalf("ingest exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "2 item(s) seen") {
		t.Errorf("the ingest did not report the items it saw:\n%s", out)
	}

	// 2. The index holds one chunk per file and no vector yet.
	code, out = runCLI(t, "stats")
	if code != 0 {
		t.Fatalf("stats exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "chunks: 2") {
		t.Errorf("the index does not hold the two chunks:\n%s", out)
	}
	if !strings.Contains(out, "vectors: 0") {
		t.Errorf("the index holds vectors before the embed ran:\n%s", out)
	}

	// 3. The embed gives every chunk a vector.
	code, out = runCLI(t, "embed")
	if code != 0 {
		t.Fatalf("embed exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "2 chunk(s) embedded") {
		t.Errorf("the embed did not report the chunks it embedded:\n%s", out)
	}

	code, out = runCLI(t, "stats")
	if code != 0 {
		t.Fatalf("stats exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "vectors: 2") {
		t.Errorf("the index does not hold a vector per chunk:\n%s", out)
	}

	// 4. A query about one file returns that file.
	code, out = runCLI(t, "query", "alpha")
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "Alpha Report") {
		t.Errorf("the query did not return the file it asks about:\n%s", out)
	}
	if strings.Contains(out, "Beta Report") {
		t.Errorf("the query returned the other file as well:\n%s", out)
	}

	// 5. A query about nothing returns no hits, and that is not an error.
	code, out = runCLI(t, "query", unusedWord(t, []string{"alpha", "retrieval", "notes", "beta", "caching"}))
	if code != 0 {
		t.Fatalf("a query that matches nothing exited %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "no matches") {
		t.Errorf("a query that matches nothing did not say so:\n%s", out)
	}
}

// writeCorpusFile writes one markdown file of the test corpus.
func writeCorpusFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
