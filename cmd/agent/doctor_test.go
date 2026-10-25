package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeModelServer serves a minimal OpenAI-compatible /v1/models catalog.
func fakeModelServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		data := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCLI runs one CLI invocation in process and returns its exit code and
// the combined output.
func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func TestDoctorPassesAgainstAFakeModelServer(t *testing.T) {
	srv := fakeModelServer(t, "embeddinggemma-300m", "spark-x2.5-4b")
	t.Setenv("LOCALAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("EMBED_MODEL", "embeddinggemma-300m")
	t.Setenv("CHAT_MODEL", "spark-x2.5-4b")
	t.Setenv("AGENT_DB_PATH", filepath.Join(t.TempDir(), "index.db"))

	cfg := writeConfig(t, goodConfig)
	code, out := runCLI(t, "doctor", "--config", cfg)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\n%s", code, out)
	}
	for _, want := range []string{
		"config: " + cfg,
		"providers: 3 provider type(s) validated",
		"model server: " + srv.URL + "/v1 is reachable",
		"embed model: embeddinggemma-300m is served",
		"chat model: spark-x2.5-4b is served",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q\n%s", want, out)
		}
	}
}

func TestDoctorFailsWithAnActionableHintWhenTheModelServerIsDown(t *testing.T) {
	// Port 1 on loopback is closed; the doctor must fail, not hang.
	t.Setenv("LOCALAI_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("AGENT_DB_PATH", filepath.Join(t.TempDir(), "index.db"))

	cfg := writeConfig(t, goodConfig)
	code, out := runCLI(t, "doctor", "--config", cfg, "--timeout", "2s")
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "model server:") {
		t.Errorf("doctor output missing the model-server failure\n%s", out)
	}
	if !strings.Contains(out, "LOCALAI_BASE_URL") {
		t.Errorf("doctor output missing the actionable hint naming LOCALAI_BASE_URL\n%s", out)
	}
}

func TestDoctorWarnsWhenAConfiguredModelIsNotServed(t *testing.T) {
	srv := fakeModelServer(t, "spark-x2.5-4b")
	t.Setenv("LOCALAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("EMBED_MODEL", "embeddinggemma-300m")
	t.Setenv("CHAT_MODEL", "spark-x2.5-4b")
	t.Setenv("AGENT_DB_PATH", filepath.Join(t.TempDir(), "index.db"))

	cfg := writeConfig(t, goodConfig)
	code, out := runCLI(t, "doctor", "--config", cfg)
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0 (a missing model is a warning, not a failure)\n%s", code, out)
	}
	if !strings.Contains(out, "embed model: embeddinggemma-300m is not in the server's catalog") {
		t.Errorf("doctor output missing the missing-model warning\n%s", out)
	}
}

func TestDoctorSkipsTheNetworkOnRequest(t *testing.T) {
	t.Setenv("LOCALAI_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("AGENT_DB_PATH", filepath.Join(t.TempDir(), "index.db"))

	cfg := writeConfig(t, goodConfig)
	code, out := runCLI(t, "doctor", "--config", cfg, "--skip-network")
	if code != 0 {
		t.Fatalf("doctor exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "model server: skipped (--skip-network)") {
		t.Errorf("doctor output missing the skip note\n%s", out)
	}
}

func TestDoctorNeverPrintsCredentialValues(t *testing.T) {
	const secret = "super-secret-value-must-not-appear"
	t.Setenv("AGENT_TEST_GHOST_URL", "http://127.0.0.1:9/ghost")
	t.Setenv("AGENT_TEST_GHOST_KEY", secret)
	t.Setenv("AGENT_TEST_POSTIZ_URL", "http://127.0.0.1:9/postiz")
	t.Setenv("AGENT_TEST_POSTIZ_KEY", secret)
	t.Setenv("LOCALAI_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("AGENT_DB_PATH", filepath.Join(t.TempDir(), "index.db"))

	cfg := writeConfig(t, goodConfig)
	_, out := runCLI(t, "doctor", "--config", cfg, "--skip-network")
	if strings.Contains(out, secret) {
		t.Fatalf("doctor output leaked a credential value:\n%s", out)
	}
}
