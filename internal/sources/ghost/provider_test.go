package ghost_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/sources/ghost"
)

// contentPost is one post in the captured Content API shape: primary_author
// is an object and both content formats are present.
const contentPost = `{
  "id": "6ae88d4e6637410001003249",
  "uuid": "d5f95a48-79f7-426c-9392-09902fec81a9",
  "title": "A post",
  "slug": "a-post",
  "html": "<h2>Intro</h2><p>hello world</p>",
  "plaintext": "hello world",
  "excerpt": "hello",
  "status": "published",
  "published_at": "2026-10-28T14:34:26.000+00:00",
  "updated_at": "2026-10-29T20:23:22.000+00:00",
  "url": "https://example.com/a-post/",
  "visibility": "public",
  "primary_author": {"id": "a1", "name": "Eli Lap", "slug": "eli"},
  "tags": [{"id": "t1", "name": "go", "slug": "go"}]
}`

func TestContentClientListAndFetch(t *testing.T) {
	var listQuery, fetchPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ghost/api/content/posts/":
			listQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"posts":[` + contentPost + `],"meta":{"pagination":{"page":1,"pages":1,"next":0}}}`))
		case strings.HasPrefix(r.URL.Path, "/ghost/api/content/posts/"):
			fetchPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"posts":[` + contentPost + `]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p, err := ghost.NewProvider(&ghost.Options{Limit: 500}, srv.URL, "0123456789abcdef01234567", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	since := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	items, err := p.List(context.Background(), since)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List() returned %d items, want 1", len(items))
	}
	if items[0].ID != "d5f95a48-79f7-426c-9392-09902fec81a9" || items[0].Ref != "6ae88d4e6637410001003249" {
		t.Errorf("item = %+v, want uuid id and API id ref", items[0])
	}
	for _, want := range []string{"key=0123456789abcdef01234567", "limit=100", "filter=updated_at", "formats=html%2Cplaintext"} {
		if !strings.Contains(listQuery, want) {
			t.Errorf("list query = %q, want it to contain %q", listQuery, want)
		}
	}

	doc, err := p.Fetch(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if want := "/ghost/api/content/posts/6ae88d4e6637410001003249/"; fetchPath != want {
		t.Errorf("fetch path = %q, want %q (Ref, not the uuid)", fetchPath, want)
	}
	if doc.Title != "A post" || doc.Author != "Eli Lap" || len(doc.Tags) != 1 {
		t.Errorf("document = %+v, want title, author and tags", doc)
	}
	if doc.UpdatedAt.IsZero() || doc.PublishedAt.IsZero() {
		t.Errorf("document lost its timestamps: %+v", doc)
	}
	if !strings.Contains(doc.HTML, "<h2>Intro</h2>") {
		t.Errorf("document HTML = %q, want the upstream body", doc.HTML)
	}
}

func TestNewProviderSelectsAdminForKeyWithColon(t *testing.T) {
	// An Admin key is "<id>:<secret>"; no request is made here, only the
	// client selection, which must not reject the shape.
	if _, err := ghost.NewProvider(&ghost.Options{Limit: 15}, "https://ghost.example.com",
		"0123456789abcdef01234567:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", http.DefaultClient); err != nil {
		t.Fatalf("NewProvider(admin key) error = %v", err)
	}
}

func TestNewProviderRejectsBadContentKey(t *testing.T) {
	if _, err := ghost.NewProvider(&ghost.Options{Limit: 15}, "https://ghost.example.com", "  ", http.DefaultClient); err == nil {
		t.Fatal("NewProvider(blank key) = nil error, want an error")
	}
}

func TestListPaginatesAllPages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"posts":[` + contentPost + `],"meta":{"pagination":{"page":1,"pages":2,"next":2,"total":2}}}`))
		default:
			_, _ = w.Write([]byte(`{"posts":[` + strings.Replace(contentPost, `"A post"`, `"Another post"`, 1) + `],"meta":{"pagination":{"page":2,"pages":2,"next":0,"total":2}}}`))
		}
	}))
	defer srv.Close()

	p, err := ghost.NewProvider(&ghost.Options{Limit: 1}, srv.URL, "0123456789abcdef01234567", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List() returned %d items, want both pages (got pages %v)", len(items), pages)
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Errorf("requested pages = %v, want [1 2]", pages)
	}
}

func TestListSkipsDraftsUnlessIncluded(t *testing.T) {
	draft := strings.Replace(contentPost, `"published_at": "2026-10-28T14:34:26.000+00:00",`, "", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"posts":[` + draft + `],"meta":{"pagination":{"page":1,"pages":1}}}`))
	}))
	defer srv.Close()

	items, err := ghost.NewProvider(&ghost.Options{Limit: 10}, srv.URL, "0123456789abcdef01234567", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	got, err := items.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List() returned %d items, want drafts skipped", len(got))
	}

	withDrafts, err := ghost.NewProvider(&ghost.Options{Limit: 10, IncludeDrafts: true}, srv.URL, "0123456789abcdef01234567", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider(include_drafts) error = %v", err)
	}
	got, err = withDrafts.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List(include_drafts) error = %v", err)
	}
	if len(got) != 1 {
		t.Errorf("List(include_drafts) returned %d items, want the draft", len(got))
	}
}

func TestAdminClientAuthAndEndpoints(t *testing.T) {
	const adminKey = "0123456789abcdef01234567:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var auth, listQuery, fetchPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/ghost/api/admin/posts/":
			listQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"posts":[` + contentPost + `],"meta":{"pagination":{"page":1,"pages":1,"next":0}}}`))
		case strings.HasPrefix(r.URL.Path, "/ghost/api/admin/posts/"):
			fetchPath = r.URL.Path
			_, _ = w.Write([]byte(`{"posts":[` + contentPost + `]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p, err := ghost.NewProvider(&ghost.Options{Limit: 500}, srv.URL, adminKey, srv.Client())
	if err != nil {
		t.Fatalf("NewProvider(admin key) error = %v", err)
	}
	since := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	items, err := p.List(context.Background(), since)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !strings.HasPrefix(auth, "Ghost ") {
		t.Fatalf("Authorization = %q, want a Ghost JWT", auth)
	}
	segments := strings.Split(strings.TrimPrefix(auth, "Ghost "), ".")
	if len(segments) != 3 {
		t.Errorf("token %q has %d segments, want 3", auth, len(segments))
	}
	header, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		t.Fatalf("decode JWT header: %v", err)
	}
	if !strings.Contains(string(header), `"kid":"0123456789abcdef01234567"`) {
		t.Errorf("JWT header = %s, want the key id as kid", header)
	}
	if !strings.Contains(listQuery, "updated_since=2026-10-26T00%3A00%3A00Z") {
		t.Errorf("admin list query = %q, want RFC3339 updated_since", listQuery)
	}
	if !strings.Contains(listQuery, "limit=100") {
		t.Errorf("admin list query = %q, want the limit clamped to 100", listQuery)
	}
	if len(items) != 1 {
		t.Fatalf("List() returned %d items, want 1", len(items))
	}
	if _, err := p.Fetch(context.Background(), items[0]); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if want := "/ghost/api/admin/posts/6ae88d4e6637410001003249/"; fetchPath != want {
		t.Errorf("fetch path = %q, want %q", fetchPath, want)
	}
}
