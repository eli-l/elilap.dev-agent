package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// stubRetriever records the query the handler built and returns canned results.
type stubRetriever struct {
	hits     []retrieval.Hit
	err      error
	gotQuery retrieval.Query
	calls    int
}

func (s *stubRetriever) Search(_ context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	s.calls++
	s.gotQuery = q
	return s.hits, s.err
}

func postRetrieve(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, retrieveRoute, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestValidateLoopbackAddr(t *testing.T) {
	valid := []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.2:1"}
	for _, addr := range valid {
		if err := validateLoopbackAddr(addr); err != nil {
			t.Errorf("validateLoopbackAddr(%q) = %v, want nil", addr, err)
		}
	}
	invalid := []struct {
		addr string
		want string
	}{
		{addr: ":8080", want: "binds every interface"},
		{addr: "0.0.0.0:8080", want: "not loopback"},
		{addr: "192.168.1.10:8080", want: "not loopback"},
		{addr: "example.com:8080", want: "not loopback"},
		{addr: "127.0.0.1:http", want: "port must be a number"},
		{addr: "127.0.0.1:0", want: "port must be a number"},
		{addr: "127.0.0.1", want: "invalid --addr"},
	}
	for _, tc := range invalid {
		err := validateLoopbackAddr(tc.addr)
		if err == nil {
			t.Errorf("validateLoopbackAddr(%q) = nil, want an error", tc.addr)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("validateLoopbackAddr(%q) = %v, want it to contain %q", tc.addr, err, tc.want)
		}
	}
}

func TestRunServeRejectsNonLoopbackBeforeOpeningIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	var stderr bytes.Buffer
	code := runServe([]string{"--addr", "0.0.0.0:8080", "--db", dbPath},
		envcfg.Config{DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel}, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2 for a non-loopback bind", code)
	}
	if !strings.Contains(stderr.String(), "must not be exposed") {
		t.Errorf("stderr = %q, want the exposure warning", stderr.String())
	}
}

func TestRunServeRequiresIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	var stderr bytes.Buffer
	code := runServe([]string{"--db", dbPath},
		envcfg.Config{DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel}, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 for a missing index", code)
	}
	if !strings.Contains(stderr.String(), "run `agent ingest`") {
		t.Errorf("stderr = %q, want the ingest hint", stderr.String())
	}
}

func TestRetrieveHandlerReturnsHits(t *testing.T) {
	stub := &stubRetriever{hits: []retrieval.Hit{{
		ChunkID: "c1", SourceKind: "ghost_post", SourceID: "uuid-1",
		SourceURL: "https://read.elilap.dev/x", Author: "Eli",
		PublishedAt: "2026-11-03T12:00:00Z", Title: "t", Body: "b",
		Score: 0.9,
	}}}
	rec := postRetrieve(t, newRetrieveHandler(stub), `{
		"query":" portion of a text ","top_n":2,"source":"postiz",
		"platform":["linkedin"],"after":"2026-01-01","before":"2026-11-03",
		"recency_halflife_days":30
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	want := retrieval.Query{
		Text: " portion of a text ", TopN: 2, Source: "postiz",
		Platforms: []string{"linkedin"}, After: "2026-01-01", Before: "2026-11-03",
		RecencyHalflifeDays: 30,
	}
	if stub.calls != 1 || !reflect.DeepEqual(stub.gotQuery, want) {
		t.Errorf("searcher got %+v (%d calls), want %+v", stub.gotQuery, stub.calls, want)
	}
	var hits []retrieval.Hit
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if len(hits) != 1 || hits[0].ChunkID != "c1" || hits[0].Score != 0.9 {
		t.Errorf("hits = %+v, want the stub hit", hits)
	}
}

func TestRetrieveHandlerEmptyResultIsJSONArray(t *testing.T) {
	rec := postRetrieve(t, newRetrieveHandler(&stubRetriever{}), `{"query":"nothing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}

func TestRetrieveHandlerRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name   string
		method string
		body   string
		status int
		want   string
	}{
		{name: "get", method: http.MethodGet, status: http.StatusMethodNotAllowed, want: "use POST"},
		{name: "malformed", method: http.MethodPost, body: `{`, status: http.StatusBadRequest, want: "invalid JSON"},
		{name: "unknown field", method: http.MethodPost, body: `{"query":"q","bogus":true}`, status: http.StatusBadRequest, want: "unknown field"},
		{name: "two objects", method: http.MethodPost, body: `{"query":"q"}{"query":"r"}`, status: http.StatusBadRequest, want: "exactly one JSON object"},
		{name: "wrong type", method: http.MethodPost, body: `{"query":5}`, status: http.StatusBadRequest, want: "invalid JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, retrieveRoute, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			newRetrieveHandler(&stubRetriever{}).ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tc.want)
			}
			if tc.status == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != http.MethodPost {
				t.Errorf("Allow = %q, want POST", rec.Header().Get("Allow"))
			}
		})
	}
}

func TestRetrieveHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{
			name:   "invalid query",
			err:    fmt.Errorf("%w: query text must not be empty", retrieval.ErrInvalidQuery),
			status: http.StatusBadRequest,
			want:   "must not be empty",
		},
		{
			name:   "timeout",
			err:    context.DeadlineExceeded,
			status: http.StatusGatewayTimeout,
			want:   "timed out",
		},
		{
			name:   "backend",
			err:    errors.New("dial tcp: connection refused"),
			status: http.StatusBadGateway,
			want:   "retrieval failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := postRetrieve(t, newRetrieveHandler(&stubRetriever{err: tc.err}), `{"query":"q"}`)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tc.want)
			}
			// Internal detail never reaches the client.
			if strings.Contains(rec.Body.String(), "dial tcp") {
				t.Errorf("body leaks the backend error: %s", rec.Body.String())
			}
		})
	}
}

// TestRetrieveHandlerBodyLimit proves an oversized body is rejected before it
// reaches the searcher.
func TestRetrieveHandlerBodyLimit(t *testing.T) {
	stub := &stubRetriever{}
	body := `{"query":"` + strings.Repeat("x", maxRequestBody) + `"}`
	rec := postRetrieve(t, newRetrieveHandler(stub), body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an oversized body", rec.Code)
	}
	if stub.calls != 0 {
		t.Error("the searcher was called for a rejected body")
	}
}

// Keep the retrieveTimeout constant honest about being a real budget.
func TestRetrieveTimeoutIsBounded(t *testing.T) {
	if retrieveTimeout <= 0 || retrieveTimeout > time.Minute {
		t.Errorf("retrieveTimeout = %v, want a positive budget under a minute", retrieveTimeout)
	}
}

// seedServeIndex writes one chunk and optionally embeds it under tag.
func seedServeIndex(t *testing.T, tag string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	st, err := store.Open(dbPath, filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	chunk := store.Chunk{
		ChunkID: "c1", SourceKind: "ghost_post", SourceID: "uuid-1", Author: "Eli",
		PublishedAt: at, UpdatedAt: at, ContentHash: "hash-c1",
		Title: "t", Body: "b", BodyTokens: 1,
	}
	if _, _, err := st.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"},
		[]store.Chunk{chunk}); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	if tag == "" {
		return dbPath
	}
	vector := make([]float32, store.EmbeddingDims)
	vector[0] = 1
	if _, _, err := st.WriteEmbeddings([]store.ChunkEmbedding{{
		ChunkID: "c1", ContentHash: "hash-c1", Vector: vector,
	}}, tag, at); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}
	return dbPath
}

func TestRunServeRequiresEmbeddings(t *testing.T) {
	dbPath := seedServeIndex(t, "")
	var stderr bytes.Buffer
	code := runServe([]string{"--db", dbPath},
		envcfg.Config{DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel}, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 for an unembedded index", code)
	}
	if !strings.Contains(stderr.String(), "run `agent embed` first") {
		t.Errorf("stderr = %q, want the embed hint", stderr.String())
	}
}

func TestRunServeRejectsModelMismatch(t *testing.T) {
	// The stored vectors came from a different model than the configured one;
	// comparing them would return noise, so startup must fail.
	dbPath := seedServeIndex(t, "localai:some-other-model/text-v1")
	var stderr bytes.Buffer
	code := runServe([]string{"--db", dbPath},
		envcfg.Config{DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel}, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 for a model mismatch", code)
	}
	if !strings.Contains(stderr.String(), "localai:some-other-model/text-v1") {
		t.Errorf("stderr = %q, want the stamped tag named", stderr.String())
	}
	if !strings.Contains(stderr.String(), "run `agent embed` first") {
		t.Errorf("stderr = %q, want the recovery hint", stderr.String())
	}
}

// stubAnswerer records the question and returns a canned loop result.
type stubAnswerer struct {
	answer      agentloop.Answer
	err         error
	gotQuestion string
	calls       int
}

func (s *stubAnswerer) Run(_ context.Context, question string) (agentloop.Answer, error) {
	s.calls++
	s.gotQuestion = question
	return s.answer, s.err
}

func postAsk(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, askRoute, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAskHandlerReturnsAnswer(t *testing.T) {
	stub := &stubAnswerer{answer: agentloop.Answer{
		Answer: "Grounded answer.", ToolIterations: 1, ElapsedMS: 1234,
		Citations: []agentloop.Citation{{ChunkID: "c1", SourceURL: "https://x"}},
	}}
	rec := postAsk(t, newAskHandler(stub), `{"question":" what about harnesses? "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if stub.gotQuestion != " what about harnesses? " {
		t.Errorf("question = %q, want the request body verbatim", stub.gotQuestion)
	}
	var got agentloop.Answer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Answer != "Grounded answer." || len(got.Citations) != 1 || got.ToolIterations != 1 {
		t.Errorf("answer = %+v", got)
	}
}

func TestAskHandlerEmptyCitationsIsArray(t *testing.T) {
	stub := &stubAnswerer{answer: agentloop.Answer{
		Answer: agentloop.Refusal, Refused: true,
	}}
	rec := postAsk(t, newAskHandler(stub), `{"question":"off corpus"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(raw["citations"]) != "[]" {
		t.Errorf("citations = %s, want []", raw["citations"])
	}
}

func TestAskHandlerRejectsBadRequests(t *testing.T) {
	tests := []struct {
		method string
		body   string
		status int
		want   string
	}{
		{method: http.MethodGet, status: http.StatusMethodNotAllowed, want: "use POST"},
		{method: http.MethodPost, body: `{`, status: http.StatusBadRequest, want: "invalid JSON"},
		{method: http.MethodPost, body: `{"question":"q","bogus":1}`, status: http.StatusBadRequest, want: "unknown field"},
		{method: http.MethodPost, body: `{"question":"q"}{"question":"r"}`, status: http.StatusBadRequest, want: "exactly one JSON object"},
		{method: http.MethodPost, body: `{"question":"   "}`, status: http.StatusBadRequest, want: "question must not be empty"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			stub := &stubAnswerer{}
			req := httptest.NewRequest(tc.method, askRoute, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			newAskHandler(stub).ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.want)
			}
			if stub.calls != 0 {
				t.Error("the loop ran for a rejected request")
			}
		})
	}
}

func TestAskHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{name: "budget", err: agentloop.ErrBudget, status: http.StatusGatewayTimeout, want: "timed out"},
		{name: "deadline", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout, want: "timed out"},
		{name: "backend", err: errors.New("chat returned 503"), status: http.StatusBadGateway, want: "answer failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := postAsk(t, newAskHandler(&stubAnswerer{err: tc.err}), `{"question":"q"}`)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.want)
			}
			if strings.Contains(rec.Body.String(), "chat returned") {
				t.Errorf("body leaks the backend error: %s", rec.Body.String())
			}
		})
	}
}
