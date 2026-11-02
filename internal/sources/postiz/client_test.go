package postiz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/sources/postiz"
)

func TestNewClientAcceptsInstanceRootAndAPISuffix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"posts":[]}`))
	}))
	defer srv.Close()

	// The upstream docs quote the API root as "<host>/api", while an instance
	// URL is a bare host; both must address the same endpoint exactly once.
	for _, base := range []string{srv.URL, srv.URL + "/", srv.URL + "/api", srv.URL + "/api/"} {
		t.Run(base, func(t *testing.T) {
			gotPath = ""
			c, err := postiz.NewClient(base, "key", srv.Client())
			if err != nil {
				t.Fatalf("NewClient(%q) error = %v", base, err)
			}
			if _, err := c.ListPosts(context.Background(), time.Unix(0, 0), time.Now()); err != nil {
				t.Fatalf("ListPosts() error = %v", err)
			}
			if want := "/api/public/v1/posts"; gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
		})
	}
}

func TestClientRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		key     string
		wantErr string
	}{
		{name: "blank url", baseURL: "  ", key: "k", wantErr: "base URL is required"},
		{name: "url without scheme", baseURL: "postiz.example.com", key: "k", wantErr: "invalid base URL"},
		{name: "unsupported scheme", baseURL: "ftp://postiz.example.com", key: "k", wantErr: "invalid base URL"},
		{name: "blank key", baseURL: "https://postiz.example.com", key: " ", wantErr: "API key is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := postiz.NewClient(tc.baseURL, tc.key, http.DefaultClient)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("NewClient() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestClientReportsUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"msg":"Invalid API key"}`))
	}))
	defer srv.Close()

	c, err := postiz.NewClient(srv.URL, "bad-key", srv.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c.ListPosts(context.Background(), time.Unix(0, 0), time.Now())
	if err == nil {
		t.Fatal("ListPosts() = nil error, want the upstream status error")
	}
	for _, want := range []string{"unexpected status 401", "Invalid API key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ListPosts() error = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "bad-key") {
		t.Errorf("ListPosts() error leaks the API key: %v", err)
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"posts":`))
	}))
	defer srv.Close()

	c, err := postiz.NewClient(srv.URL, "key", srv.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := c.ListPosts(context.Background(), time.Unix(0, 0), time.Now()); err == nil {
		t.Fatal("ListPosts(truncated body) = nil error, want a decode error")
	}
}
