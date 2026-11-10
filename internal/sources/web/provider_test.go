package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// fixtureServer serves the captured sitemap, feed, and pages over httptest so
// the provider's discovery and fetch paths run exactly as they do live.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(path, name, contentType string) {
		body, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatalf("read testdata/%s: %v", name, err)
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(body)
		})
	}
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		// Relative child locs, like the pages sitemap below: a sitemap served
		// from another origin than its locs would test nothing.
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>/sitemap-pages.xml</loc><lastmod>2026-10-23T05:41:34.550Z</lastmod></sitemap>
  <sitemap><loc>/sitemap-posts.xml</loc><lastmod>2026-10-23T05:41:34.550Z</lastmod></sitemap>
</sitemapindex>`))
	})
	serve("/rss.xml", "rss.xml", "application/rss+xml")
	serve("/case-study/pwa-for-magento2", "case-study.html", "text/html; charset=utf-8")
	serve("/services", "services.html", "text/html; charset=utf-8")
	serve("/private/hidden", "services.html", "text/html; charset=utf-8")
	serve("/noindex", "noindex.html", "text/html; charset=utf-8")
	mux.HandleFunc("/sitemap-pages.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		// Relative locs exercise resolution against the site origin, which is
		// what a sitemap without absolute URLs looks like.
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>/</loc><lastmod>2026-10-23T05:41:34.983Z</lastmod></url>
  <url><loc>/services</loc><lastmod>2026-10-23T05:41:34.984Z</lastmod></url>
  <url><loc>/case-study/pwa-for-magento2</loc><lastmod>2026-11-16T00:00:00.000Z</lastmod></url>
  <url><loc>/noindex</loc><lastmod>2026-11-20T00:00:00.000Z</lastmod></url>
  <url><loc>/tag/go</loc><lastmod>2026-10-23T05:41:34.984Z</lastmod></url>
  <url><loc>/private/hidden</loc><lastmod>2026-10-23T05:41:34.984Z</lastmod></url>
  <url><loc>/_astro/default.jpg</loc></url>
</urlset>`))
	})
	mux.HandleFunc("/sitemap-posts.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://read.elilap.dev/your-stack-matters-more-than-you-think</loc><lastmod>2026-11-06T15:00:32.000Z</lastmod></url>
</urlset>`))
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	})
	mux.HandleFunc("/pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Home</title></head><body><main><h1>Home</h1><p>Home page body with enough words to be a candidate region for the extractor to pick.</p></main></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// testProvider builds a web provider pointed at the fixture server. extra is
// merged into the options block.
func testProvider(t *testing.T, srv *httptest.Server, extra string) *Provider {
	t.Helper()
	if extra == "" {
		extra = "  delay_ms: 0\n"
	}
	o, err := parse(t, `
id: site
type: web
options:
  site_url: `+srv.URL+`
`+extra)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	p, err := NewProvider(o, srv.Client(), "elilap-dev-agent/test", nil)
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	return p
}

func TestListDiscoversAndFiltersPages(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, `
  delay_ms: 0
  include_paths: ["^/$", "^/services$", "^/case-study/", "^/noindex$"]
  exclude_paths: ["^/tag/"]
`)

	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	got := map[string]shared.Item{}
	for _, it := range items {
		got[it.ID] = it
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	for _, want := range []string{
		host + "/",
		host + "/services",
		host + "/case-study/pwa-for-magento2",
		host + "/noindex",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("List() missing %q; got %v", want, keys(got))
		}
	}
	for _, unwanted := range []string{
		host + "/tag/go",             // excluded by path filter
		host + "/_astro/default.jpg", // asset extension
		"read.elilap.dev/your-stack-matters-more-than-you-think", // cross-host
	} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("List() included %q, want it filtered out", unwanted)
		}
	}
	if item := got[host+"/case-study/pwa-for-magento2"]; !item.LastModified.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("case-study LastModified = %v, want the sitemap lastmod", item.LastModified)
	}
	if len(items) != 4 {
		t.Errorf("List() returned %d items, want 4", len(items))
	}
}

func TestListAppliesSince(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, `
  delay_ms: 0
  feeds: false
  include_paths: ["^/", "^/services$", "^/case-study/"]
`)
	items, err := p.List(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	// /noindex is stamped 2026-11-20 in the fixture sitemap, so it is inside
	// the window too; everything older must be gone.
	if len(ids) != 2 {
		t.Errorf("List(since 2026-11-01) = %v, want the two pages dated on/after the bound", ids)
	}
	for _, id := range ids {
		if strings.HasSuffix(id, "/services") || strings.HasSuffix(id, "/") {
			t.Errorf("List(since 2026-11-01) included %q, want only pages updated in the window", id)
		}
	}
}

func TestListRefusesToTruncateAnUnboundedListing(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, `
  delay_ms: 0
  max_pages: 2
`)
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(unbounded, max_pages=2) = nil error, want an error")
	} else if !strings.Contains(err.Error(), "exceed options.max_pages") {
		t.Errorf("error = %q, want it to name max_pages", err)
	}

	// A bounded listing may truncate: the ingestor never deletes drift from it.
	items, err := p.List(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("List(bounded) error = %v", err)
	}
	if len(items) != 2 {
		t.Errorf("List(bounded) = %d items, want the 2-page cap", len(items))
	}
}

func TestListNeverReturnsAnEmptyListingAfterAFailure(t *testing.T) {
	// An empty listing on an unbounded run makes the ingestor delete every
	// chunk of the source, so a failed discovery source must surface as an
	// error even when it produced no candidates at all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sitemap.xml" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		// The feed probes get an HTML page with a 200, which is a parse
		// failure, not an absent feed.
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>not a feed</body></html>"))
	}))
	t.Cleanup(srv.Close)
	p := testProvider(t, srv, "  delay_ms: 0\n")
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(failing discovery) = nil error, want an error instead of an empty listing")
	}
}

func TestFetchExtractsPage(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, "")
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var target shared.Item
	for _, it := range items {
		if strings.HasSuffix(it.URL, "/case-study/pwa-for-magento2") {
			target = it
		}
	}
	if target.ID == "" {
		t.Fatal("case study not listed")
	}

	doc, err := p.Fetch(context.Background(), target)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got, want := doc.Title, "Case Study: Building PWA for Magento 2"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
	if doc.HTML == "" || doc.Body == "" {
		t.Error("Fetch() returned an empty body")
	}
	if !strings.Contains(doc.Body, "Magento") {
		t.Errorf("Body lost the article:\n%.200s", doc.Body)
	}
	if doc.Meta["url"] != target.URL {
		t.Errorf("Meta[url] = %v, want %q", doc.Meta["url"], target.URL)
	}
	if doc.Meta["strategy"] != StrategyArticle {
		t.Errorf("Meta[strategy] = %v, want %q for a host with no adapter",
			doc.Meta["strategy"], StrategyArticle)
	}
	if !doc.PublishedAt.Equal(time.Date(2024, 11, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PublishedAt = %v, want the fixture's <time datetime>", doc.PublishedAt)
	}
	// The sitemap lastmod is later than the publish date, so updated_at must
	// carry the edit time rather than falling back to the publication date.
	if !doc.UpdatedAt.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("UpdatedAt = %v, want the later sitemap lastmod", doc.UpdatedAt)
	}
}

func TestFetchRejectsNonHTML(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, "")
	_, err := p.Fetch(context.Background(), shared.Item{ID: "pdf", URL: srv.URL + "/pdf"})
	if err == nil {
		t.Fatal("Fetch(pdf) = nil error, want a content-type error")
	}
	if !strings.Contains(err.Error(), "not HTML") {
		t.Errorf("error = %q, want it to name the content type", err)
	}
}

func TestFetchHonoursRobots(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, "")
	_, err := p.Fetch(context.Background(), shared.Item{ID: "hidden", URL: srv.URL + "/private/hidden"})
	if err == nil {
		t.Fatal("Fetch(disallowed) = nil error, want a robots.txt error")
	}
	if !strings.Contains(err.Error(), "disallowed by robots.txt") {
		t.Errorf("error = %q, want it to name robots.txt", err)
	}
}

func TestFetchNoIndexWritesNoChunks(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, "")
	doc, err := p.Fetch(context.Background(), shared.Item{ID: "noindex", URL: srv.URL + "/noindex"})
	if err != nil {
		t.Fatalf("Fetch(noindex) error = %v", err)
	}
	if doc.Body != "" || doc.HTML != "" || doc.Title != "" {
		t.Errorf("Fetch(noindex) = %+v, want an empty document", doc)
	}
	if doc.Meta["noindex"] != true {
		t.Errorf("Meta[noindex] = %v, want true", doc.Meta["noindex"])
	}
}

func TestInspectReportsCandidatesAndSuggestion(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, "")
	analysis, err := p.Inspect(context.Background(), srv.URL+"/case-study/pwa-for-magento2")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if analysis.Status != http.StatusOK || analysis.Bytes == 0 {
		t.Errorf("status/bytes = %d/%d, want a fetched page", analysis.Status, analysis.Bytes)
	}
	if len(analysis.Candidates) == 0 {
		t.Fatal("Candidates is empty, want at least the article")
	}
	best := analysis.Candidates[0]
	if best.Tag != "article" {
		t.Errorf("best candidate tag = %q, want article", best.Tag)
	}
	if best.Selector == "" || best.Chars == 0 {
		t.Errorf("best candidate = %+v, want a usable selector and a char count", best)
	}
	if analysis.Suggested.Strategy != StrategyAuto {
		t.Errorf("Suggested.Strategy = %q, want auto for an <article> page", analysis.Suggested.Strategy)
	}
	if analysis.Discovery.SitemapURLs == 0 {
		t.Error("Discovery.SitemapURLs = 0, want the sitemap-index children counted")
	}
	if len(analysis.Headings) == 0 {
		t.Error("Headings is empty, want the page headings")
	}
	if analysis.Preview == nil || analysis.Preview.Chars == 0 {
		t.Errorf("Preview = %+v, want the configured strategy's extraction", analysis.Preview)
	}
	if analysis.Preview != nil && analysis.Preview.Title != "Case Study: Building PWA for Magento 2" {
		t.Errorf("Preview.Title = %q, want the page h1", analysis.Preview.Title)
	}
	if analysis.Preview != nil && !analysis.Preview.PublishedAt.Equal(time.Date(2024, 11, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Preview.PublishedAt = %v, want the page's <time datetime>", analysis.Preview.PublishedAt)
	}
	if text := analysis.Text("test"); !strings.Contains(text, "content candidates") {
		t.Errorf("Text() = %q, want the human-readable report", text)
	}
}

func TestNewFromSourceNeedsNoCredentials(t *testing.T) {
	o, err := parse(t, `
id: site
type: web
options: { site_url: https://elilap.dev, delay_ms: 0 }
`)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	if _, err := NewFromSource(config.Source{ID: "site", Type: "web"}, o, shared.BuildContext{}); err != nil {
		t.Fatalf("NewFromSource() error = %v, want a credential-free provider", err)
	}
	if _, err := NewFromSource(config.Source{ID: "site", Type: "web"}, struct{}{}, shared.BuildContext{}); err == nil {
		t.Fatal("NewFromSource(wrong options) = nil error, want an error")
	}
}

// keys returns a sorted list of map keys, for failure messages.
func keys(m map[string]shared.Item) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// newSiteProvider builds a provider against an arbitrary handler. The caller
// passes the discovery switches it wants; only site_url and the zero delay are
// implied.
func newSiteProvider(t *testing.T, srv *httptest.Server, extra string) *Provider {
	t.Helper()
	o, err := parse(t, `
id: site
type: web
options:
  site_url: `+srv.URL+`
  delay_ms: 0
`+extra)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	p, err := NewProvider(o, srv.Client(), "elilap-dev-agent/test", nil)
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	return p
}

func TestListRefusesAnEmptyUnboundedListing(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	p := newSiteProvider(t, srv, "  sitemap: true\n")
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(no pages) = nil error, want a refusal instead of an empty unbounded listing")
	} else if !strings.Contains(err.Error(), "no pages discovered") {
		t.Errorf("error = %q, want it to name the empty listing", err)
	}
	// A bounded listing with no candidates is legitimate: nothing changed in
	// the window, and it can never delete drift.
	items, err := p.List(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("List(bounded, no pages) error = %v", err)
	}
	if len(items) != 0 {
		t.Errorf("List(bounded, no pages) = %d items, want 0", len(items))
	}
}

func TestListFailsUnboundedOnAFeedFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><urlset><url><loc>/services</loc><lastmod>2026-10-23T05:41:34Z</lastmod></url></urlset>`))
	})
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>Services</h1><p>Services body long enough for the extractor to keep the region.</p></main></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n  feeds: true\n")
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(unbounded, failed feed) = nil error, want it to refuse a partial listing")
	} else if !strings.Contains(err.Error(), "incomplete page discovery") {
		t.Errorf("error = %q, want it to name the incomplete discovery", err)
	}

	items, err := p.List(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("List(bounded, failed feed) error = %v, want the partial window to be usable", err)
	}
	if len(items) != 1 {
		t.Errorf("List(bounded, failed feed) = %d items, want 1", len(items))
	}
}

func TestSitemapIndexOverTheChildCapFails(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><sitemapindex>`)
	for i := 0; i < maxChildSitemaps+1; i++ {
		fmt.Fprintf(&b, "<sitemap><loc>/sitemap-%d.xml</loc></sitemap>", i)
	}
	b.WriteString(`</sitemapindex>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n")
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(oversized sitemapindex) = nil error, want a refusal")
	} else if !strings.Contains(err.Error(), "sitemap cap") {
		t.Errorf("error = %q, want it to name the sitemap cap", err)
	}
}

func TestSitemapChildOnAnotherHostIsNotFetched(t *testing.T) {
	var externalCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalCalls.Add(1)
		http.Error(w, "should not be reached", http.StatusInternalServerError)
	}))
	t.Cleanup(external.Close)

	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><sitemapindex>
<sitemap><loc>/sitemap-pages.xml</loc></sitemap>
<sitemap><loc>` + external.URL + `/sitemap.xml</loc></sitemap>
</sitemapindex>`))
	})
	mux.HandleFunc("/sitemap-pages.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><urlset><url><loc>/services</loc></url></urlset>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n")
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Errorf("List() = %d items, want the one same-host page", len(items))
	}
	if got := externalCalls.Load(); got != 0 {
		t.Errorf("external sitemap received %d request(s), want 0: a sitemapindex must not steer the fetcher", got)
	}
}

func TestFetchRejectsOffSiteRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redirect" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body><main><h1>Landing</h1><p>Redirect target body.</p></main></body></html>`))
			return
		}
		// localhost resolves to the same listener but is a different hostname,
		// which is exactly what the redirect pin must catch.
		target := strings.Replace(srv0(r), "127.0.0.1", "localhost", 1) + "/services"
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	p := newSiteProvider(t, srv, "")
	_, err := p.Fetch(context.Background(), shared.Item{ID: "redirect", URL: srv.URL + "/redirect"})
	if err == nil {
		t.Fatal("Fetch(off-site redirect) = nil error, want it rejected")
	}
	if !strings.Contains(err.Error(), "redirected off-site") {
		t.Errorf("error = %q, want it to name the off-site redirect", err)
	}
}

// srv0 returns the test server's own origin, derived from the request.
func srv0(r *http.Request) string {
	return "http://" + r.Host
}

func TestCrawlDepthBoundsDiscovery(t *testing.T) {
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body><main><h1>Page</h1><p>` + body +
				` enough text for the extractor to keep this region as content.</p></main></body></html>`))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>A</h1><p>Seed page with enough text.</p><a href="/b">b</a></main></body></html>`))
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>B</h1><p>Level one page with enough text.</p><a href="/c">c</a></main></body></html>`))
	})
	mux.HandleFunc("/c", page("Level two page, beyond the configured crawl depth."))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: false\n  feeds: false\n  start_urls: [\"/a\"]\n  crawl_depth: 1\n")
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if !ids[host+"/a"] {
		t.Errorf("List() = %v, want the seed /a", ids)
	}
	if !ids[host+"/b"] {
		t.Errorf("List() = %v, want the level-1 link /b", ids)
	}
	if ids[host+"/c"] {
		t.Errorf("List() = %v, level-2 /c must stay outside crawl_depth: 1", ids)
	}
}

func TestCrawlThatReachesNoSeedFailsAnUnboundedListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := newSiteProvider(t, srv, "  sitemap: false\n  feeds: false\n  start_urls: [\"/down\"]\n  crawl_depth: 1\n")
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(unbounded, dead crawl) = nil error, want a refusal")
	} else if !strings.Contains(err.Error(), "incomplete page discovery") {
		t.Errorf("error = %q, want it to name the incomplete discovery", err)
	}
	if _, err := p.List(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Errorf("List(bounded, dead crawl) error = %v, want the window to stay usable", err)
	}
}

func TestRobotsIsRetriedAfterATransientFailure(t *testing.T) {
	var robotsCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		if robotsCalls.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	})
	mux.HandleFunc("/private/hidden", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>Hidden</h1><p>Body with enough text to be extracted.</p></main></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "")
	item := shared.Item{ID: "hidden", URL: srv.URL + "/private/hidden"}
	if _, err := p.Fetch(context.Background(), item); err != nil {
		t.Fatalf("Fetch() with unreadable robots.txt error = %v, want the permissive first pass", err)
	}
	if _, err := p.Fetch(context.Background(), item); err == nil {
		t.Fatal("Fetch() after robots.txt recovered = nil error, want the gate applied on the retry")
	}
}

func TestInspectTextOmitsAZeroPublishDate(t *testing.T) {
	a := &Analysis{Preview: &Preview{Title: "Undated", Strategy: StrategyArticle, Chars: 100}}
	if text := a.Text("site"); !strings.Contains(text, "published (none)") {
		t.Errorf("Text() = %q, want an undated page to report (none)", text)
	}
}

func TestRobotsDeniedMeansDisallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.Error(w, "no robots for you", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>Page</h1><p>Body with enough text.</p></main></body></html>`))
	}))
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "")
	_, err := p.Fetch(context.Background(), shared.Item{ID: "page", URL: srv.URL + "/page"})
	if err == nil {
		t.Fatal("Fetch() with a 403 robots.txt = nil error, want the site treated as disallowed")
	}
	if !strings.Contains(err.Error(), "disallowed by robots.txt") {
		t.Errorf("error = %q, want the robots.txt refusal", err)
	}
}

func TestRefusedFeedProbeIsAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><urlset><url><loc>/services</loc></url></urlset>`))
	})
	mux.HandleFunc("/rss.xml", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "auth required", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n  feeds: true\n")
	if _, err := p.List(context.Background(), time.Time{}); err != nil {
		t.Fatalf("List(unbounded, 403 feed) error = %v, want the refused probe treated as absent", err)
	}
}

func TestRedirectPinSendsNoOffSiteRequest(t *testing.T) {
	var offSiteCalls atomic.Int32
	offSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offSiteCalls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>Off-site</h1><p>Should never be reached.</p></main></body></html>`))
	}))
	t.Cleanup(offSite.Close)
	offSiteURL := strings.Replace(offSite.URL, "127.0.0.1", "localhost", 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, offSiteURL+"/page", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "")
	if _, err := p.Fetch(context.Background(), shared.Item{ID: "page", URL: srv.URL + "/page"}); err == nil {
		t.Fatal("Fetch(off-site redirect) = nil error, want it rejected")
	}
	if got := offSiteCalls.Load(); got != 0 {
		t.Errorf("off-site host received %d request(s), want 0: the redirect must be rejected before it is sent", got)
	}
}

func TestCrawlPartialFailureIsIncomplete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>A</h1><p>Seed page with enough text.</p><a href="/b">b</a></main></body></html>`))
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>B</h1><p>Level one page.</p></main></body></html>`))
	})
	mux.HandleFunc("/broken", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: false\n  feeds: false\n  start_urls: [\"/a\", \"/broken\"]\n  crawl_depth: 1\n")
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(unbounded, partially failing crawl) = nil error, want an incomplete-listing refusal")
	} else if !strings.Contains(err.Error(), "incomplete page discovery") {
		t.Errorf("error = %q, want it to name the incomplete discovery", err)
	}
	if _, err := p.List(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Errorf("List(bounded, partially failing crawl) error = %v, want the window to stay usable", err)
	}
}

func TestBoundedTruncationKeepsTheNewestPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><urlset>
<url><loc>/old</loc><lastmod>2026-01-01T00:00:00Z</lastmod></url>
<url><loc>/new</loc><lastmod>2026-11-01T00:00:00Z</lastmod></url>
</urlset>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n  max_pages: 1\n")
	items, err := p.List(context.Background(), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("List(bounded, max_pages 1) error = %v", err)
	}
	if len(items) != 1 || !strings.HasSuffix(items[0].URL, "/new") {
		t.Errorf("List(bounded, max_pages 1) = %+v, want the newest page kept", items)
	}
}

func TestNestedSitemapIndexIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><sitemapindex><sitemap><loc>/nested.xml</loc></sitemap></sitemapindex>`))
	})
	mux.HandleFunc("/nested.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><sitemapindex><sitemap><loc>/sitemap-deep.xml</loc></sitemap></sitemapindex>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n")
	if _, err := p.List(context.Background(), time.Time{}); err == nil {
		t.Fatal("List(nested sitemapindex) = nil error, want a refusal")
	} else if !strings.Contains(err.Error(), "is itself a sitemapindex") {
		t.Errorf("error = %q, want it to name the nested index", err)
	}
}

func TestInspectPreviewReportsNoIndex(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, "")
	analysis, err := p.Inspect(context.Background(), srv.URL+"/noindex")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if analysis.Preview == nil || analysis.Preview.Skipped != "noindex" {
		t.Errorf("Preview = %+v, want it to report a noindex skip", analysis.Preview)
	}
}

func TestListTreatsAnHTMLSitemapAsAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: every path answers with index.html.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><div id="app"></div></body></html>`))
	})
	mux.HandleFunc("/services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>Services</h1><p>Body.</p></main></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n  start_urls: [\"/services\"]\n")
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List(html sitemap) error = %v, want the sitemap treated as absent", err)
	}
	if len(items) != 1 {
		t.Errorf("List(html sitemap) = %d items, want the seed page", len(items))
	}
}

func TestDiscoveryAcceptsTheWWWAlias(t *testing.T) {
	host := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><urlset><url><loc>http://www.` + host + `/services</loc></url></urlset>`))
	}))
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "http://")

	p := newSiteProvider(t, srv, "  sitemap: true\n")
	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 || !strings.Contains(items[0].URL, "//www.") {
		t.Errorf("List() = %+v, want the www-hosted page kept (same site as the apex)", items)
	}
}

func TestRobotsRefusedFeedProbeIsAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /rss.xml\n"))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><urlset><url><loc>/services</loc></url></urlset>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: true\n  feeds: true\n")
	if _, err := p.List(context.Background(), time.Time{}); err != nil {
		t.Fatalf("List() error = %v, want a robots-refused feed probe treated as absent", err)
	}
}

func TestFetchRefusesAnOffSiteInitialURL(t *testing.T) {
	var calls atomic.Int32
	offSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>Off-site</h1><p>Body.</p></main></body></html>`))
	}))
	t.Cleanup(offSite.Close)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	// Same host name, different port: the pin compares names, so use localhost
	// for the off-site item against a 127.0.0.1 source.
	offSiteURL := strings.Replace(offSite.URL, "127.0.0.1", "localhost", 1)

	p := newSiteProvider(t, srv, "")
	if _, err := p.Fetch(context.Background(), shared.Item{ID: "x", URL: offSiteURL + "/page"}); err == nil {
		t.Fatal("Fetch(off-site item) = nil error, want it refused before the request")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("off-site host received %d request(s), want 0", got)
	}
}

func TestInspectSkipsTheCrawl(t *testing.T) {
	var crawlHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>Page</h1><p>Body long enough to be a candidate region for the extractor.</p></main></body></html>`))
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		crawlHits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>A</h1><p>Seed.</p></main></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := newSiteProvider(t, srv, "  sitemap: false\n  feeds: false\n  start_urls: [\"/a\"]\n  crawl_depth: 1\n")
	if _, err := p.Inspect(context.Background(), srv.URL+"/page"); err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got := crawlHits.Load(); got != 0 {
		t.Errorf("inspect fetched the crawl seed %d time(s), want 0: it must not walk the site", got)
	}
}

func TestInspectAcceptsTheWWWAlias(t *testing.T) {
	srv := fixtureServer(t)
	p := testProvider(t, srv, "  sitemap: false\n  feeds: false\n")
	// www.127.0.0.1 does not resolve, so the fetch itself may fail; what must
	// not happen is the host guard rejecting the alias as "outside" the site.
	alias := strings.Replace(srv.URL, "127.0.0.1", "www.127.0.0.1", 1)
	_, err := p.Inspect(context.Background(), alias+"/services")
	if err != nil && strings.Contains(err.Error(), "is outside") {
		t.Errorf("Inspect(www alias) error = %v, want the alias accepted as the same site", err)
	}
}

func TestInspectTextReportsIncompleteDiscovery(t *testing.T) {
	a := &Analysis{Discovery: DiscoverySummary{SitemapURLs: 3, Incomplete: "feed: boom"}}
	if text := a.Text("site"); !strings.Contains(text, "incomplete: feed: boom") {
		t.Errorf("Text() = %q, want the incomplete discovery reason", text)
	}
}
