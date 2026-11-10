package web

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// extractorFor builds an extractor over fixture-friendly options.
func extractorFor(t *testing.T, doc string) *Extractor {
	t.Helper()
	o, err := parse(t, doc)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	return newExtractor(o, nil)
}

// extractFixture parses one testdata page and runs the extractor on it.
func extractFixture(t *testing.T, e *Extractor, page, rawURL string) (Page, error) {
	t.Helper()
	root, err := parseHTML(readTestdata(t, page))
	if err != nil {
		t.Fatalf("parseHTML(%s) error = %v", page, err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%s) error = %v", rawURL, err)
	}
	return e.Extract(u, root)
}

func TestExtractCaseStudyThroughElilapAdapter(t *testing.T) {
	e := extractorFor(t, `
id: site
type: web
options: { site_url: https://elilap.dev }
`)
	page, err := extractFixture(t, e, "case-study.html", "https://elilap.dev/case-study/pwa-for-magento2")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if page.Strategy != "elilap" {
		t.Errorf("Strategy = %q, want the host adapter to claim elilap.dev", page.Strategy)
	}
	if got, want := page.Title, "Case Study: Building PWA for Magento 2"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
	if got, want := page.Author, "Eli Lap"; got != want {
		t.Errorf("Author = %q, want %q", got, want)
	}
	if len(page.Tags) != 1 || page.Tags[0] != "case-study" {
		t.Errorf("Tags = %v, want [case-study] from the URL section", page.Tags)
	}
	want := time.Date(2024, 11, 16, 0, 0, 0, 0, time.UTC)
	if !page.PublishedAt.Equal(want) {
		t.Errorf("PublishedAt = %v, want the <time datetime> value %v", page.PublishedAt, want)
	}
	if !strings.Contains(page.Body, "Magento") || !strings.Contains(page.Body, "## ") {
		t.Errorf("Body does not look like the case study Markdown:\n%.300s", page.Body)
	}
	for _, chrome := range []string{"NAVIGATION-FOOTER-SENTINEL", "TICKER-SENTINEL", "Privacy"} {
		if strings.Contains(page.Body, chrome) {
			t.Errorf("Body carries site chrome %q", chrome)
		}
	}
	if page.NoIndex {
		t.Error("NoIndex = true, want false for an indexable page")
	}
}

func TestExtractServicesPageWithoutArticle(t *testing.T) {
	e := extractorFor(t, `
id: site
type: web
options: { site_url: https://elilap.dev }
`)
	page, err := extractFixture(t, e, "services.html", "https://elilap.dev/services")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got, want := page.Title, "Two ways to work with me."; got != want {
		t.Errorf("Title = %q, want the page h1 %q", got, want)
	}
	if page.Strategy != "elilap" {
		t.Errorf("Strategy = %q, want elilap", page.Strategy)
	}
	if !strings.Contains(page.Body, "AI product review") {
		t.Errorf("Body does not carry the services copy:\n%.300s", page.Body)
	}
}

func TestExtractJSONLDMetadata(t *testing.T) {
	e := extractorFor(t, `
id: site
type: web
options: { site_url: https://example.com }
`)
	page, err := extractFixture(t, e, "article-jsonld.html", "https://example.com/writing/noise-tax")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got, want := page.Title, "The MCP noise tax"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
	if got, want := page.Author, "Eli Lap"; got != want {
		t.Errorf("Author = %q, want the JSON-LD author %q", got, want)
	}
	if !page.PublishedAt.Equal(time.Date(2026, 9, 9, 10, 24, 45, 0, time.UTC)) {
		t.Errorf("PublishedAt = %v, want the JSON-LD datePublished", page.PublishedAt)
	}
	if !page.UpdatedAt.Equal(time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("UpdatedAt = %v, want the JSON-LD dateModified", page.UpdatedAt)
	}
	if len(page.JSONLDTypes) == 0 || page.JSONLDTypes[0] != "BlogPosting" {
		t.Errorf("JSONLDTypes = %v, want BlogPosting first", page.JSONLDTypes)
	}
	// article:tag is repeatable: every occurrence must survive, not just the
	// first, and JSON-LD keywords are merged in too.
	for _, want := range []string{"mcp", "agents", "tooling"} {
		if !containsString(page.Tags, want) {
			t.Errorf("Tags = %v, want %q from the repeated article:tag meta tags", page.Tags, want)
		}
	}
	for _, chrome := range []string{"NAV-SENTINEL", "SIDEBAR-SENTINEL", "FOOTER-SENTINEL"} {
		if strings.Contains(page.Body, chrome) {
			t.Errorf("Body carries chrome %q", chrome)
		}
	}
	if !strings.Contains(page.Body, "Where the tax hides") {
		t.Errorf("Body lost an h2 section:\n%.400s", page.Body)
	}
}

func TestExtractSunnySelectorStrategy(t *testing.T) {
	e := extractorFor(t, `
id: site
type: web
options:
  site_url: https://elilap.dev
  strategy: selectors
  selectors:
    content: "main > section:nth-of-type(1)"
    title: h1
    remove: ["p.kicker"]
`)
	page, err := extractFixture(t, e, "services.html", "https://elilap.dev/services")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if page.Strategy != StrategySelectors {
		t.Errorf("Strategy = %q, want selectors", page.Strategy)
	}
	if page.Title == "" {
		t.Error("Title is empty, want the configured h1")
	}
}

func TestExtractSelectorMissIsAnError(t *testing.T) {
	e := extractorFor(t, `
id: site
type: web
options:
  site_url: https://elilap.dev
  strategy: selectors
  selectors: { content: "main > article.does-not-exist" }
`)
	_, err := extractFixture(t, e, "services.html", "https://elilap.dev/services")
	if err == nil {
		t.Fatal("Extract(selector miss) = nil error, want an error naming the selector")
	}
	if !strings.Contains(err.Error(), "matched nothing") {
		t.Errorf("error = %q, want it to name the selector miss", err)
	}
}

func TestExtractBodyStrategyKeepsChrome(t *testing.T) {
	e := extractorFor(t, `
id: site
type: web
options: { site_url: https://elilap.dev, strategy: body }
`)
	page, err := extractFixture(t, e, "services.html", "https://elilap.dev/services")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if page.Strategy != StrategyBody {
		t.Errorf("Strategy = %q, want body", page.Strategy)
	}
	if !strings.Contains(page.Body, "TICKER-SENTINEL") {
		t.Error("body strategy should keep everything outside the chrome elements")
	}
	if strings.Contains(page.Body, "NAVIGATION-FOOTER-SENTINEL") {
		t.Error("chrome removal must still drop <footer> under the body strategy")
	}
}

func TestExtractRespectsNoIndex(t *testing.T) {
	e := extractorFor(t, `
id: site
type: web
options: { site_url: https://example.com }
`)
	page, err := extractFixture(t, e, "noindex.html", "https://example.com/secret")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if !page.NoIndex {
		t.Error("NoIndex = false, want true for a robots noindex page")
	}
}

func TestElilapAdapterMatch(t *testing.T) {
	adapter := elilapAdapter{}
	tests := map[string]bool{
		"https://elilap.dev/services":     true,
		"https://www.elilap.dev/services": true,
		"https://read.elilap.dev/post":    false,
		"https://elilap.dev.example.com/": false,
		"https://example.com/":            false,
	}
	for raw, want := range tests {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", raw, err)
		}
		if got := adapter.Match(u); got != want {
			t.Errorf("Match(%q) = %t, want %t", raw, got, want)
		}
	}
}
