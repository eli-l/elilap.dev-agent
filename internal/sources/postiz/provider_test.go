package postiz_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/postiz"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// fixture is the captured public-API response (2026-11-02), trimmed to the
// records these tests exercise: two published LinkedIn cross-posts, one
// published X post, one past-scheduled Bluesky post, one draft and one failed
// post. docs/postiz.md documents the full shape.
func fixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/posts.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

// serveFixture starts a fake Postiz public API that records the last request
// and answers with the captured fixture.
func serveFixture(t *testing.T) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.query = r.URL.Query()
		rec.auth = r.Header.Get("Authorization")
		rec.calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

type recorded struct {
	calls int
	path  string
	query map[string][]string
	auth  string
}

func newProvider(t *testing.T, srv *httptest.Server, opts *postiz.Options) *postiz.Provider {
	t.Helper()
	p, err := postiz.NewProvider(opts, srv.URL, "test-api-key", srv.Client())
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	return p
}

func TestListRequestsThePublicPostsEndpoint(t *testing.T) {
	srv, rec := serveFixture(t)
	p := newProvider(t, srv, &postiz.Options{Platforms: []string{"linkedin"}})

	since := time.Date(2025, 6, 26, 0, 0, 0, 0, time.UTC)
	if _, err := p.List(context.Background(), since); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if want := "/api/public/v1/posts"; rec.path != want {
		t.Errorf("path = %q, want %q", rec.path, want)
	}
	if got := rec.auth; got != "test-api-key" {
		t.Errorf("Authorization = %q, want the bare API key", got)
	}
	if got := rec.query["startDate"]; len(got) != 1 || got[0] != "2025-06-26T00:00:00Z" {
		t.Errorf("startDate = %v, want the since bound in RFC3339 UTC", got)
	}
	if got := rec.query["endDate"]; len(got) != 1 || !strings.HasSuffix(got[0], "Z") {
		t.Errorf("endDate = %v, want a RFC3339 UTC upper bound", got)
	}
}

func TestListUsesTheEpochForAnUnboundedWindow(t *testing.T) {
	srv, rec := serveFixture(t)
	p := newProvider(t, srv, &postiz.Options{Platforms: []string{"linkedin"}})

	if _, err := p.List(context.Background(), time.Time{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := rec.query["startDate"]; len(got) != 1 || got[0] != "1970-01-01T00:00:00Z" {
		t.Errorf("startDate = %v, want the Unix epoch for an unbounded listing", got)
	}
}

func TestListFiltersByPlatform(t *testing.T) {
	srv, _ := serveFixture(t)

	p := newProvider(t, srv, &postiz.Options{Platforms: []string{"linkedin"}})
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List(linkedin) returned %d items, want the two published LinkedIn posts", len(items))
	}
	for _, it := range items {
		if it.ID == "cm86ak4ji0003yic5nyjlpme6" {
			t.Errorf("List(linkedin) returned the X post %s", it.ID)
		}
		if !strings.HasPrefix(it.ETag, "sha256:") {
			t.Errorf("item %s ETag = %q, want a content fingerprint", it.ID, it.ETag)
		}
		if it.LastModified.IsZero() {
			t.Errorf("item %s lost its publish date", it.ID)
		}
		if it.URL == "" {
			t.Errorf("item %s has no canonical URL", it.ID)
		}
	}
}

func TestListNeverImportsDraftsOrFailures(t *testing.T) {
	srv, _ := serveFixture(t)
	p := newProvider(t, srv, &postiz.Options{
		Platforms:        []string{"bluesky"},
		IncludeScheduled: true,
	})

	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	// With include_scheduled only the past-scheduled post qualifies; the
	// draft and the failed post must still be skipped.
	if len(items) != 1 || items[0].ID != "cmcf9uf3n0002xa903ow1iob1" {
		t.Fatalf("List() = %+v, want only the past-scheduled post", items)
	}
}

func TestListSkipsScheduledUnlessAsked(t *testing.T) {
	srv, _ := serveFixture(t)
	p := newProvider(t, srv, &postiz.Options{Platforms: []string{"bluesky"}})

	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 0 {
		t.Errorf("List() returned %d items, want scheduled/draft/failed skipped", len(items))
	}
}

func TestListSkipsFutureScheduledEvenWhenIncluded(t *testing.T) {
	// The fixture has no future-dated post (nothing was queued ahead at
	// capture time), so this one is moved forward to exercise the bound.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := strings.Replace(string(fixture(t)),
			`"publishDate": "2025-06-27T20:30:00.000Z"`,
			`"publishDate": "2999-01-01T00:00:00.000Z"`, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	p := newProvider(t, srv, &postiz.Options{
		Platforms:        []string{"bluesky"},
		IncludeScheduled: true,
	})
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List() returned %d items, want future-scheduled posts skipped", len(items))
	}
}

func TestFetchNormalizesTheCrossPost(t *testing.T) {
	srv, _ := serveFixture(t)
	p := newProvider(t, srv, &postiz.Options{Platforms: []string{"linkedin"}})

	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var target shared.Item
	for _, it := range items {
		if it.ID == "cmcf8xhds0012nx5i70ynwyln" {
			target = it
		}
	}
	if target.ID == "" {
		t.Fatalf("fixture post missing from List(): %+v", items)
	}

	doc, err := p.Fetch(context.Background(), target)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if doc.Platform != "linkedin" {
		t.Errorf("document Platform = %q, want linkedin", doc.Platform)
	}
	if doc.Author != "Ilja Lapkovsky" {
		t.Errorf("document Author = %q, want the integration (account) name", doc.Author)
	}
	if want := "After a week of AI-buzz around me I finally sat down and spent a couple of hours building software."; doc.Title != want {
		t.Errorf("document Title = %q, want the first line %q", doc.Title, want)
	}
	if !strings.Contains(doc.Body, "But I felt emptiness") {
		t.Errorf("document Body lost the post text: %q", doc.Body)
	}
	if doc.HTML != "" {
		t.Errorf("document HTML = %q, want none (the API returns plain text)", doc.HTML)
	}
	if doc.PublishedAt.IsZero() || doc.UpdatedAt != doc.PublishedAt {
		t.Errorf("document timestamps = %v / %v, want the publish date for both", doc.PublishedAt, doc.UpdatedAt)
	}
	if got, _ := doc.Meta["state"].(string); got != "PUBLISHED" {
		t.Errorf("meta state = %q, want PUBLISHED", got)
	}
	if got, _ := doc.Meta["url"].(string); got != target.URL {
		t.Errorf("meta url = %q, want the release URL %q", got, target.URL)
	}
	if got, _ := doc.Meta["group"].(string); got == "" {
		t.Errorf("meta group is empty, want the upstream post group")
	}
}

func TestFetchWithoutAListingFailsClearly(t *testing.T) {
	srv, _ := serveFixture(t)
	p := newProvider(t, srv, &postiz.Options{Platforms: []string{"linkedin"}})

	_, err := p.Fetch(context.Background(), shared.Item{ID: "cmcf8xhds0012nx5i70ynwyln"})
	if err == nil {
		t.Fatal("Fetch(unlisted item) = nil error, want an error explaining the missing listing")
	}
	if !strings.Contains(err.Error(), "not in the last listing") {
		t.Errorf("Fetch() error = %q, want it to explain the missing listing", err)
	}
}

func TestNewProviderRejectsBadCredentials(t *testing.T) {
	if _, err := postiz.NewProvider(&postiz.Options{Platforms: []string{"linkedin"}}, "", "key", http.DefaultClient); err == nil {
		t.Error("NewProvider(blank URL) = nil error, want an error")
	}
	if _, err := postiz.NewProvider(&postiz.Options{Platforms: []string{"linkedin"}}, "https://postiz.example.com", "  ", http.DefaultClient); err == nil {
		t.Error("NewProvider(blank key) = nil error, want an error")
	}
}

func TestNewFromSourceResolvesCredentialEnvNames(t *testing.T) {
	t.Setenv("AGENT_TEST_POSTIZ_URL", "https://postiz.example.com")
	t.Setenv("AGENT_TEST_POSTIZ_KEY", "test-key")

	src := config.Source{
		ID:   "postiz",
		Type: "postiz",
		Credentials: config.Credentials{
			APIBaseURL: config.EnvRef{Env: "AGENT_TEST_POSTIZ_URL"},
			APIKey:     config.EnvRef{Env: "AGENT_TEST_POSTIZ_KEY"},
		},
	}
	p, err := postiz.NewFromSource(src, &postiz.Options{Platforms: []string{"linkedin"}}, http.DefaultClient)
	if err != nil {
		t.Fatalf("NewFromSource() error = %v", err)
	}
	if p.Type() != "postiz" {
		t.Errorf("Type() = %q, want postiz", p.Type())
	}

	missing := src
	missing.Credentials.APIKey = config.EnvRef{Env: "AGENT_TEST_POSTIZ_ABSENT"}
	_, err = postiz.NewFromSource(missing, &postiz.Options{Platforms: []string{"linkedin"}}, http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "AGENT_TEST_POSTIZ_ABSENT") {
		t.Errorf("NewFromSource(missing env) error = %v, want it to name the variable", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Errorf("NewFromSource(missing env) error leaks a credential value: %v", err)
	}
}

func TestNewFromSourceRejectsWrongOptionsType(t *testing.T) {
	src := config.Source{ID: "postiz", Type: "postiz"}
	if _, err := postiz.NewFromSource(src, struct{}{}, http.DefaultClient); err == nil {
		t.Fatal("NewFromSource(wrong options type) = nil error, want an error")
	}
}
