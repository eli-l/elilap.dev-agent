package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/budget"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/ratelimit"
)

const testToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRequireTokenDevModePasses(t *testing.T) {
	store, err := auth.NewStore("", filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	called := false
	h := requireToken(store, surfaceJSON, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, retrieveRoute, nil))
	if !called || rec.Code != http.StatusNoContent {
		t.Errorf("called/status = %v/%d, want the handler to run", called, rec.Code)
	}
}

func TestRequireTokenRejectsMissingAndWrongTokens(t *testing.T) {
	store, err := auth.NewStore("alice:"+testToken, "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler ran without a valid token")
	})
	for _, tc := range []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "wrong scheme", header: "Basic abc"},
		{name: "wrong token", header: "Bearer " + strings.Repeat("b", 43)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, surface := range []string{surfaceJSON, surfaceRPC} {
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				if tc.header != "" {
					req.Header.Set("Authorization", tc.header)
				}
				rec := httptest.NewRecorder()
				requireToken(store, surface, nil, next).ServeHTTP(rec, req)
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s: status = %d, want 401", surface, rec.Code)
				}
				if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
					t.Errorf("%s: WWW-Authenticate = %q", surface, got)
				}
				if surface == surfaceJSON && !strings.Contains(rec.Body.String(), `"error"`) {
					t.Errorf("json body = %q", rec.Body.String())
				}
				if surface == surfaceRPC && !strings.Contains(rec.Body.String(), `"code":-32600`) {
					t.Errorf("rpc body = %q", rec.Body.String())
				}
			}
		})
	}
}

func TestRequireTokenAttachesTokenID(t *testing.T) {
	store, err := auth.NewStore("alice:"+testToken, "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if tok, ok := auth.TokenFrom(r.Context()); ok {
			got = tok.ID
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "bearer "+testToken)
	requireToken(store, surfaceJSON, nil, next).ServeHTTP(httptest.NewRecorder(), req)
	if got != "alice" {
		t.Errorf("context token id = %q, want alice", got)
	}
}

func TestWriteBudgetErrorReturns429(t *testing.T) {
	for _, surface := range []string{surfaceRPC, surfaceJSON} {
		t.Run(surface, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeBudgetError(rec, surface, "peer", &budget.Refusal{
				Scope:      budget.ScopeGlobalDaily,
				RetryAfter: 90 * time.Second,
			})
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", rec.Code)
			}
			if got, want := rec.Header().Get("Retry-After"), "90"; got != want {
				t.Errorf("Retry-After = %q, want %q", got, want)
			}
			if surface == surfaceRPC {
				var resp a2a.Response
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.Error == nil || resp.Error.Code != a2a.CodeServerError {
					t.Errorf("error = %+v, want the documented server error code", resp.Error)
				}
				data, _ := resp.Error.Data.(map[string]any)
				if data["scope"] != string(budget.ScopeGlobalDaily) {
					t.Errorf("scope = %v, want global_daily", data["scope"])
				}
				return
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["error"] != "daily quota exhausted" {
				t.Errorf("error = %q, want the quota message", body["error"])
			}
		})
	}
}

func TestServeAgentCardUsesStaticBase(t *testing.T) {
	secured := true
	h := serveAgentCard(func() bool { return secured }, "https://agent.example")

	// A hostile Host/forwarded header must not change the advertised URL.
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "attacker.example"
	req.Header.Set("X-Forwarded-Proto", "http")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.URL != "https://agent.example/a2a" {
		t.Errorf("url = %q, want the configured base", card.URL)
	}
	if card.SecuritySchemes == nil {
		t.Error("secured card has no securitySchemes")
	}

	secured = false
	card = a2a.AgentCard{}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.SecuritySchemes != nil {
		t.Error("token-less card advertises security")
	}
}

func TestCardBaseURL(t *testing.T) {
	tests := []struct {
		name       string
		addr       string
		public     bool
		configured string
		want       string
		wantErr    string
	}{
		{name: "loopback", addr: "127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "configured", addr: "127.0.0.1:8080", configured: "https://agent.example/", want: "https://agent.example"},
		{name: "configured path", configured: "https://example.com/agent/", want: "https://example.com/agent"},
		{name: "public needs url", addr: "0.0.0.0:8080", public: true, wantErr: "AGENT_A2A_PUBLIC_URL"},
		{name: "relative", configured: "agent.example.com", wantErr: "absolute http(s) URL"},
		{name: "wrong scheme", configured: "ftp://agent.example", wantErr: "absolute http(s) URL"},
		{name: "userinfo", configured: "https://user:pass@agent.example", wantErr: "absolute http(s) URL"},
		{name: "port only", configured: "https://:8080", wantErr: "absolute http(s) URL"},
		{name: "malformed", configured: "http://host:99999", wantErr: "absolute http(s) URL"},
		{name: "bad escape", configured: "http://x%zz", wantErr: "absolute http(s) URL"},
		{name: "uppercase scheme", configured: "HTTPS://agent.example", want: "HTTPS://agent.example"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cardBaseURL(tc.addr, tc.public, tc.configured)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("cardBaseURL error = %v, want %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "pass@agent.example") {
					t.Errorf("cardBaseURL error leaked the configured URL: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("cardBaseURL: %v", err)
			}
			if got != tc.want {
				t.Errorf("cardBaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequireTokenStaysSecuredAfterFileRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(path, []byte("alice:"+testToken+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	store, err := auth.NewStore("", path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove token file: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("Len = %d after removal, want 0", store.Len())
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler ran after the token file disappeared")
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	requireToken(store, surfaceJSON, nil, next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestRunServeRejectsPublicBindWithoutQuotas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	var stderr bytes.Buffer
	code := runServe([]string{"--addr", "0.0.0.0:8080", "--bind-public", "--db", dbPath, "--ask-quota", "0"},
		envcfg.Config{
			DBPath:        dbPath,
			EmbedModel:    envcfg.DefaultEmbedModel,
			A2ATokensFile: filepath.Join(t.TempDir(), "absent"),
		}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--ask-quota, --address-quota, and --quota-global") {
		t.Errorf("stderr = %q, want the spend-ceiling requirement", stderr.String())
	}
}

// TestRunServePublicBindDoesNotRequireToken pins the token-less public mode:
// with quotas set and no token, startup gets past the bind checks and fails on
// the missing index instead of on auth.
func TestRunServePublicBindDoesNotRequireToken(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "missing.db")
	var stderr bytes.Buffer
	code := runServe([]string{"--addr", "0.0.0.0:8080", "--bind-public", "--db", dbPath},
		envcfg.Config{
			DBPath:        dbPath,
			EmbedModel:    envcfg.DefaultEmbedModel,
			A2ATokensFile: filepath.Join(dir, "absent"),
			A2APublicURL:  "https://agent.example",
		}, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 (missing index), stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no index at") {
		t.Errorf("stderr = %q, want the missing-index error, not a token requirement", stderr.String())
	}
}

func TestRunServeRejectsMalformedTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(path, []byte("alice:short\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	var stderr bytes.Buffer
	code := runServe([]string{"--db", dbPath},
		envcfg.Config{DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel, A2ATokensFile: path}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "shorter than 32") {
		t.Errorf("stderr = %q, want the token validation error", stderr.String())
	}
}

func TestRunServeRejectsPublicBindWithoutPublicURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens")
	if err := os.WriteFile(path, []byte("alice:"+testToken+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	var stderr bytes.Buffer
	code := runServe([]string{"--addr", "0.0.0.0:8080", "--bind-public", "--db", filepath.Join(dir, "missing.db"),
		"--ask-quota", "5", "--address-quota", "5", "--quota-global", "10"},
		envcfg.Config{
			DBPath:        filepath.Join(dir, "missing.db"),
			EmbedModel:    envcfg.DefaultEmbedModel,
			A2ATokensFile: path,
		}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "AGENT_A2A_PUBLIC_URL") {
		t.Errorf("stderr = %q, want the public-URL requirement", stderr.String())
	}
}

func TestRunServeRejectsNegativeRateLimit(t *testing.T) {
	var stderr bytes.Buffer
	code := runServe([]string{"--rate-limit", "-1", "--db", filepath.Join(t.TempDir(), "missing.db")},
		envcfg.Config{
			DBPath:        filepath.Join(t.TempDir(), "missing.db"),
			EmbedModel:    envcfg.DefaultEmbedModel,
			A2ATokensFile: filepath.Join(t.TempDir(), "absent"),
		}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--rate-limit must be >= 0") {
		t.Errorf("stderr = %q, want the rate-limit validation", stderr.String())
	}
}

func TestRunServeRejectsRelativePublicURL(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	code := runServe([]string{"--db", filepath.Join(dir, "missing.db")},
		envcfg.Config{
			DBPath:        filepath.Join(dir, "missing.db"),
			EmbedModel:    envcfg.DefaultEmbedModel,
			A2ATokensFile: filepath.Join(dir, "absent"),
			A2APublicURL:  "agent.example.com",
		}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "absolute http(s) URL") {
		t.Errorf("stderr = %q, want the URL validation", stderr.String())
	}
}

func TestRequireTokenRateLimitsFailedAttempts(t *testing.T) {
	store, err := auth.NewStore("alice:"+testToken, "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	failures := ratelimit.New(1)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler ran with a bad token")
	})
	h := requireToken(store, surfaceJSON, failures, next)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first status = %d, want 401", first.Code)
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("second status = %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("429 response has no Retry-After")
	}
}

func TestRequireTokenDoesNotChargeValidAttempts(t *testing.T) {
	store, err := auth.NewStore("alice:"+testToken, "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	attempts := ratelimit.New(1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := requireToken(store, surfaceJSON, attempts, next)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("valid request %d status = %d, want 204", i+1, rec.Code)
		}
	}
}

func TestRunServeRejectsNegativeAuthLimit(t *testing.T) {
	var stderr bytes.Buffer
	code := runServe([]string{"--auth-limit", "-1", "--db", filepath.Join(t.TempDir(), "missing.db")},
		envcfg.Config{
			DBPath:        filepath.Join(t.TempDir(), "missing.db"),
			EmbedModel:    envcfg.DefaultEmbedModel,
			A2ATokensFile: filepath.Join(t.TempDir(), "absent"),
		}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--auth-limit must be >= 0") {
		t.Errorf("stderr = %q, want the auth-limit validation", stderr.String())
	}
}

func TestThrottleBoundsLogs(t *testing.T) {
	throttle := logging.NewThrottle(time.Minute)
	if !throttle.Allow("alice") {
		t.Fatal("first log was suppressed")
	}
	if throttle.Allow("alice") {
		t.Fatal("second log in the same window was allowed")
	}
	if !throttle.Allow("bob") {
		t.Fatal("another key was suppressed")
	}
}

func TestRunServeRejectsNegativeQuota(t *testing.T) {
	var stderr bytes.Buffer
	code := runServe([]string{"--ask-quota", "-1", "--db", filepath.Join(t.TempDir(), "missing.db")},
		envcfg.Config{
			DBPath:        filepath.Join(t.TempDir(), "missing.db"),
			EmbedModel:    envcfg.DefaultEmbedModel,
			A2ATokensFile: filepath.Join(t.TempDir(), "absent"),
		}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--ask-quota must be >= 0") {
		t.Errorf("stderr = %q, want the quota validation", stderr.String())
	}
}
