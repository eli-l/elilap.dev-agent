package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
