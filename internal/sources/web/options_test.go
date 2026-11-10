package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// sourceFromYAML parses an inline `options:` block into a config.Source, the
// same path sources.yaml takes.
func sourceFromYAML(t *testing.T, doc string) config.Source {
	t.Helper()
	var node struct {
		ID      string    `yaml:"id"`
		Type    string    `yaml:"type"`
		Options yaml.Node `yaml:"options"`
	}
	if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return config.Source{ID: node.ID, Type: node.Type, Options: node.Options}
}

// parse applies ParseOptions to an inline options block.
func parse(t *testing.T, doc string) (*Options, error) {
	t.Helper()
	opts, err := ParseOptions(sourceFromYAML(t, doc))
	if err != nil {
		return nil, err
	}
	return opts.(*Options), nil
}

func TestParseOptionsDefaults(t *testing.T) {
	o, err := parse(t, `
id: site
type: web
options:
  site_url: https://elilap.dev/
`)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	if o.SiteURL != "https://elilap.dev/" {
		t.Errorf("SiteURL = %q, want the host lower-cased and the root kept", o.SiteURL)
	}
	if o.Strategy != StrategyAuto {
		t.Errorf("Strategy = %q, want auto", o.Strategy)
	}
	if o.MaxPages != defaultMaxPages || o.MinChars != defaultMinChars || o.delay() != defaultDelayMS*time.Millisecond {
		t.Errorf("defaults = max_pages %d, min_chars %d, delay %v; want %d/%d/%dms",
			o.MaxPages, o.MinChars, o.delay(), defaultMaxPages, defaultMinChars, defaultDelayMS)
	}
	if !o.sitemapEnabled() || !o.feedsEnabled() || !o.robotsEnabled() {
		t.Errorf("discovery switches = sitemap %t, feeds %t, robots %t; want all true",
			o.sitemapEnabled(), o.feedsEnabled(), o.robotsEnabled())
	}
	if len(o.StartURLs) != 0 || len(o.IncludePaths) != 0 || len(o.ExcludePaths) != 0 {
		t.Errorf("unexpected defaults: %+v", o)
	}
}

func TestParseOptionsRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			name:    "missing site_url",
			doc:     "id: site\ntype: web\noptions: { strategy: auto }",
			wantErr: "options.site_url is required",
		},
		{
			name:    "non-http site_url",
			doc:     "id: site\ntype: web\noptions: { site_url: 'ftp://elilap.dev' }",
			wantErr: "must be http or https",
		},
		{
			name:    "site_url with query",
			doc:     "id: site\ntype: web\noptions: { site_url: 'https://elilap.dev/?a=1' }",
			wantErr: "must not carry a query or fragment",
		},
		{
			name:    "unknown strategy",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, strategy: reader }",
			wantErr: `options.strategy "reader" is unknown`,
		},
		{
			name:    "selectors strategy without content",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, strategy: selectors }",
			wantErr: "options.selectors.content is required",
		},
		{
			name: "invalid content selector",
			doc: `id: site
type: web
options:
  site_url: https://elilap.dev
  strategy: selectors
  selectors: { content: "main >>> p" }`,
			wantErr: "is not a valid CSS selector",
		},
		{
			name:    "invalid include regex",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, include_paths: ['(['] }",
			wantErr: "options.include_paths[0]",
		},
		{
			name:    "negative max_pages",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, max_pages: -1 }",
			wantErr: "options.max_pages must not be negative",
		},
		{
			name:    "crawl depth above the cap",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, crawl_depth: 9 }",
			wantErr: "options.crawl_depth must be between 0 and 3",
		},
		{
			name:    "start_url outside the site",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, start_urls: ['https://read.elilap.dev/'] }",
			wantErr: "is outside elilap.dev",
		},
		{
			name:    "site_url with a path",
			doc:     "id: site\ntype: web\noptions: { site_url: 'https://elilap.dev/blog' }",
			wantErr: "must be an origin without a path",
		},
		{
			name:    "start_url with a non-http scheme",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, start_urls: ['ftp://elilap.dev/x'] }",
			wantErr: "is not an http(s) URL",
		},
		{
			name:    "unknown option key",
			doc:     "id: site\ntype: web\noptions: { site_url: https://elilap.dev, selector: main }",
			wantErr: "selector",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, tc.doc)
			if err == nil {
				t.Fatalf("ParseOptions() = nil error, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseOptions() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseOptionsNormalizesSeedsAndAcceptsAdapterStrategy(t *testing.T) {
	o, err := parse(t, `
id: site
type: web
options:
  site_url: https://elilap.dev
  strategy: elilap
  start_urls: [/services/, "https://elilap.dev/case-study/pwa-for-magento2"]
  include_paths: ["^/services$", "^/case-study/"]
  exclude_paths: ["^/tag/"]
`)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	if len(o.StartURLs) != 2 {
		t.Fatalf("StartURLs = %v, want two normalized seeds", o.StartURLs)
	}
	if o.StartURLs[0] != "https://elilap.dev/case-study/pwa-for-magento2" ||
		o.StartURLs[1] != "https://elilap.dev/services" {
		t.Errorf("StartURLs = %v, want absolute normalized URLs in sorted order", o.StartURLs)
	}
	if !o.pathAllowed("/services") || !o.pathAllowed("/case-study/x") {
		t.Errorf("pathAllowed() rejected included paths")
	}
	if o.pathAllowed("/tag/go") || o.pathAllowed("/privacy") {
		t.Errorf("pathAllowed() accepted an excluded or non-included path")
	}
}

func TestOptionsMatchesHost(t *testing.T) {
	o, err := parse(t, "id: site\ntype: web\noptions: { site_url: https://elilap.dev }")
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	tests := map[string]bool{
		"https://elilap.dev/services":     true,
		"https://www.elilap.dev/services": true,
		"http://elilap.dev:8080/services": true, // scheme and port are ignored
		"https://read.elilap.dev/post":    false,
		"https://elilap.dev.example.com/": false,
	}
	for raw, want := range tests {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", raw, err)
		}
		if got := o.MatchesHost(u); got != want {
			t.Errorf("MatchesHost(%q) = %t, want %t", raw, got, want)
		}
	}
}

func TestNormalizeURLAndPageID(t *testing.T) {
	raw, err := url.Parse("HTTPS://EliLap.dev/services/?utm_source=x#top")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	u := normalizeURL(raw)
	if got, want := u.String(), "https://elilap.dev/services"; got != want {
		t.Errorf("normalized = %q, want %q", got, want)
	}
	if got, want := pageID(u), "elilap.dev/services"; got != want {
		t.Errorf("pageID = %q, want %q", got, want)
	}
	root, err := parseSiteURL("https://elilap.dev/")
	if err != nil {
		t.Fatalf("parseSiteURL() error = %v", err)
	}
	if got := pageID(root); got != "elilap.dev/" {
		t.Errorf("pageID(root) = %q, want elilap.dev/", got)
	}
}
