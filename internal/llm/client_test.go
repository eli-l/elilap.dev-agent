package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbedReturnsInputOrder(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q, want /v1/embeddings", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotAuth = r.Header.Get("Authorization")
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "m" || len(req.Input) != 2 {
			t.Errorf("request = %+v, want model m and two inputs", req)
		}
		// Answer in reverse index order: the client must restore input order.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{2, 2}},
				{"index": 0, "embedding": []float32{1, 1}},
			},
		})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL+"/v1/", "secret", srv.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.BaseURL != srv.URL+"/v1" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	vecs, err := c.Embed(context.Background(), "m", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][0] != 2 {
		t.Errorf("vectors = %v, want input order [1,1] then [2,2]", vecs)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want bearer token", gotAuth)
	}
}

func TestEmbedRejectsBadResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "count mismatch",
			body: `{"data":[{"index":0,"embedding":[1]}]}`,
			want: "1 vectors for 2 inputs",
		},
		{
			name: "index out of range",
			body: `{"data":[{"index":0,"embedding":[1]},{"index":5,"embedding":[1]}]}`,
			want: "out of range",
		},
		{
			name: "empty vector",
			body: `{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[1]}]}`,
			want: "empty vector",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := NewClient(srv.URL, "", srv.Client())
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			_, err = c.Embed(context.Background(), "m", []string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestEmbedHTTPErrorIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", maxErrorBody*2)))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "", srv.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Embed(context.Background(), "m", []string{"a"})
	if err == nil {
		t.Fatal("Embed succeeded on a 500, want an error")
	}
	if len(err.Error()) > maxErrorBody+200 {
		t.Errorf("error is %d bytes, want the body snippet bounded", len(err.Error()))
	}
}

func TestEmbedWithoutHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]}`))
	}))
	defer srv.Close()
	// The exported field documents nil as "use a default client"; constructing
	// the struct directly (without NewClient) must not panic in Do.
	c := &Client{BaseURL: srv.URL}
	if _, err := c.Embed(context.Background(), "m", []string{"a"}); err != nil {
		t.Fatalf("Embed with nil HTTP: %v", err)
	}
}

func TestNewClientRequiresBaseURL(t *testing.T) {
	if _, err := NewClient("  ", "", nil); err == nil {
		t.Fatal("NewClient accepted an empty base URL")
	}
}
