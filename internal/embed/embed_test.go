package embed_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
)

// vector builds a deterministic vector of the standard width whose first two
// entries encode the input's position and length, so the test can prove the
// vectors came back in request order.
func vector(position int, length int) []float32 {
	vec := make([]float32, embed.Dimensions)
	vec[0] = float32(position)
	vec[1] = float32(length)
	return vec
}

func TestEmbedReturnsOneVectorPerInputInOrder(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	var gotInputs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		gotModel = body.Model
		gotInputs = body.Input

		data := make([]map[string]any, 0, len(body.Input))
		for i, in := range body.Input {
			data = append(data, map[string]any{"index": i, "embedding": vector(i+1, len(in))})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	client, err := embed.New(srv.URL+"/v1", "test-key", "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inputs := []string{"alpha", "bravo", "charlie"}
	vecs, err := client.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(vecs) != len(inputs) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(inputs))
	}
	for i, vec := range vecs {
		if len(vec) != embed.Dimensions {
			t.Errorf("vector %d has %d dimensions, want %d", i, len(vec), embed.Dimensions)
		}
		if vec[0] != float32(i+1) {
			t.Errorf("vector %d carries position %v, want %d", i, vec[0], i+1)
		}
		if vec[1] != float32(len(inputs[i])) {
			t.Errorf("vector %d carries length %v, want %d", i, vec[1], len(inputs[i]))
		}
	}

	if gotPath != "/v1/embeddings" {
		t.Errorf("request path = %q, want %q", gotPath, "/v1/embeddings")
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if gotModel != "test-model" {
		t.Errorf("model = %q, want %q", gotModel, "test-model")
	}
	if len(gotInputs) != len(inputs) {
		t.Errorf("request carried %d inputs, want %d", len(gotInputs), len(inputs))
	}
}

func TestEmbedOmitsAuthorizationWithoutAPIKey(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": vector(1, 1)}}})
	}))
	defer srv.Close()

	client, err := embed.New(srv.URL+"/v1", "", "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if sawAuth {
		t.Error("Authorization header was sent, want none without an API key")
	}
}

func TestEmbedErrorCarriesStatusAndBodyExcerpt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream model server exploded"))
	}))
	defer srv.Close()

	client, err := embed.New(srv.URL+"/v1", "", "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = client.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("Embed returned no error, want one")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") {
		t.Errorf("error %q does not mention the status 500", msg)
	}
	if !strings.Contains(msg, "upstream model server exploded") {
		t.Errorf("error %q does not carry the body excerpt", msg)
	}
}

func TestEmbedErrorCarriesBothVectorLengths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}))
	defer srv.Close()

	client, err := embed.New(srv.URL+"/v1", "", "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = client.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("Embed returned no error, want one")
	}
	msg := err.Error()
	if !strings.Contains(msg, "3") {
		t.Errorf("error %q does not name the received length", msg)
	}
	if !strings.Contains(msg, "768") {
		t.Errorf("error %q does not name the expected length", msg)
	}
}

func TestEmbedNeverLeaksTheAPIKey(t *testing.T) {
	const secret = "super-secret-key-123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// A server that echoes the token back must not make the client
		// print it either.
		_, _ = w.Write([]byte("invalid token: " + secret))
	}))
	defer srv.Close()

	client, err := embed.New(srv.URL+"/v1", secret, "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = client.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("Embed returned no error, want one")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaked the API key: %q", err.Error())
	}
}

func TestEmbedWithEmptyInputSkipsTheRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	defer srv.Close()

	client, err := embed.New(srv.URL+"/v1", "", "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	vecs, err := client.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("got %d vectors, want 0", len(vecs))
	}
	if calls != 0 {
		t.Errorf("server was called %d times, want 0", calls)
	}
}

func TestNewRejectsUnusableBaseURLs(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		wantIn  string
	}{
		{name: "empty", baseURL: "", wantIn: "base URL"},
		{name: "blank", baseURL: "   ", wantIn: "base URL"},
		{name: "no scheme", baseURL: "localhost:8080/v1", wantIn: "localhost:8080/v1"},
		{name: "wrong scheme", baseURL: "ftp://localhost/v1", wantIn: "ftp://localhost/v1"},
		{name: "no host", baseURL: "http:///v1", wantIn: "http:///v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := embed.New(tc.baseURL, "", "test-model")
			if err == nil {
				t.Fatalf("New(%q) returned no error, want one", tc.baseURL)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not name the base URL %q", err.Error(), tc.wantIn)
			}
		})
	}
}

func TestNewFallsBackToTheDefaultModel(t *testing.T) {
	client, err := embed.New("http://localhost:8080/v1", "", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.Model() != embed.DefaultModel {
		t.Errorf("model = %q, want %q", client.Model(), embed.DefaultModel)
	}
}
