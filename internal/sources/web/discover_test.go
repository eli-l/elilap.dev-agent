package web

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readTestdata returns a fixture file's bytes.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return body
}

func TestParseSitemap(t *testing.T) {
	doc, err := parseSitemap(readTestdata(t, "sitemap-pages.xml"))
	if err != nil {
		t.Fatalf("parseSitemap(urlset) error = %v", err)
	}
	if len(doc.URLs) != 6 {
		t.Fatalf("urls = %d, want 6", len(doc.URLs))
	}
	if got := doc.URLs[1].Loc; got != "https://elilap.dev/services" {
		t.Errorf("second loc = %q, want the services page", got)
	}
	if got := parseStamp(doc.URLs[1].LastMod); !got.Equal(time.Date(2026, 10, 23, 5, 41, 34, 984_000_000, time.UTC)) {
		t.Errorf("lastmod = %v, want the sitemap's fractional-second stamp", got)
	}

	index, err := parseSitemap(readTestdata(t, "sitemap-index.xml"))
	if err != nil {
		t.Fatalf("parseSitemap(index) error = %v", err)
	}
	if len(index.Sitemaps) != 2 || len(index.URLs) != 0 {
		t.Errorf("index = %d sitemaps / %d urls, want 2/0", len(index.Sitemaps), len(index.URLs))
	}

	if _, err := parseSitemap([]byte("<html><body>not a sitemap</body></html>")); err == nil {
		t.Error("parseSitemap(html) = nil error, want an error")
	}
}

func TestParseFeed(t *testing.T) {
	entries, err := parseFeed(readTestdata(t, "rss.xml"))
	if err != nil {
		t.Fatalf("parseFeed(rss) error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if got := entries[0].Link; got != "https://read.elilap.dev/your-stack-matters-more-than-you-think" {
		t.Errorf("first link = %q", got)
	}
	want := time.Date(2026, 11, 6, 15, 0, 32, 0, time.UTC)
	if !entries[0].Published.Equal(want) {
		t.Errorf("first pubDate = %v, want %v", entries[0].Published, want)
	}

	atom := []byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>Atom entry</title>
    <link rel="self" href="https://elilap.dev/self"/>
    <link rel="alternate" href="https://elilap.dev/atom-entry"/>
    <published>2026-11-01T09:00:00Z</published>
    <updated>2026-11-02T09:00:00Z</updated>
  </entry>
</feed>`)
	entries, err = parseFeed(atom)
	if err != nil {
		t.Fatalf("parseFeed(atom) error = %v", err)
	}
	if len(entries) != 1 || entries[0].Link != "https://elilap.dev/atom-entry" {
		t.Fatalf("atom entries = %+v, want the alternate link", entries)
	}
	if !entries[0].Published.Equal(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("atom published = %v", entries[0].Published)
	}

	if _, err := parseFeed([]byte("<html/>")); err == nil {
		t.Error("parseFeed(html) = nil error, want an error")
	}
}

// TestParseFeedPrefersTheLaterTimestamp guards incremental List: a page
// published before the window but edited inside it must keep its edit time, or
// it is filtered out and never re-fetched.
func TestParseFeedPrefersTheLaterTimestamp(t *testing.T) {
	atom := []byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>Edited after publishing</title>
    <link rel="alternate" href="https://elilap.dev/edited"/>
    <published>2026-09-01T09:00:00Z</published>
    <updated>2026-11-01T09:00:00Z</updated>
  </entry>
</feed>`)
	entries, err := parseFeed(atom)
	if err != nil {
		t.Fatalf("parseFeed(atom) error = %v", err)
	}
	if got, want := entries[0].Updated, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("atom Updated = %v, want the later edit time %v", got, want)
	}

	rss := []byte(`<?xml version="1.0"?><rss><channel><item>
  <title>Edited after publishing</title>
  <link>https://elilap.dev/edited</link>
  <pubDate>Mon, 01 Sep 2026 09:00:00 GMT</pubDate>
  <dc:date>2026-11-01T09:00:00Z</dc:date>
</item></channel></rss>`)
	entries, err = parseFeed(rss)
	if err != nil {
		t.Fatalf("parseFeed(rss) error = %v", err)
	}
	if got, want := entries[0].Updated, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("rss Updated = %v, want the later dc:date %v", got, want)
	}
}

func TestRobotsBlankLineEndsAGroup(t *testing.T) {
	// An empty "ourbot" block must not absorb the wildcard group's rules.
	rules := parseRobots("User-agent: ourbot\n\nUser-agent: *\nDisallow: /private\n")
	if !rules.allowed("/private", "ourbot/1.0") {
		t.Error("ourbot was denied /private: the empty block merged with the wildcard group")
	}
	if rules.allowed("/private", "otherbot/1.0") {
		t.Error("otherbot was allowed /private, want the wildcard Disallow to apply")
	}
}

func TestParseStampFormats(t *testing.T) {
	tests := map[string]bool{
		"2026-10-23T05:41:34.984Z":                true,
		"2026-10-23T05:41:34Z":                    true,
		"2026-10-23":                              true,
		"Mon, 02 Nov 2026 13:21:35 GMT":           true,
		"Sat Nov 16 2024 00:00:00 GMT+0000 (UTC)": true,
		"not a date":                              false,
		"":                                        false,
	}
	for raw, wantValid := range tests {
		got := parseStamp(raw)
		if got.IsZero() == wantValid {
			t.Errorf("parseStamp(%q) = %v, want valid=%t", raw, got, wantValid)
		}
	}
}

func TestSkipExtension(t *testing.T) {
	skipped := []string{"/img/a.PNG", "/x.pdf", "/sitemap.xml", "/_astro/app.css", "/rss.xml"}
	kept := []string{"/", "/services", "/case-study/pwa-for-magento2", "/blog.html"}
	for _, path := range skipped {
		if !skipExtension(path) {
			t.Errorf("skipExtension(%q) = false, want true", path)
		}
	}
	for _, path := range kept {
		if skipExtension(path) {
			t.Errorf("skipExtension(%q) = true, want false", path)
		}
	}
}

func TestPageLinks(t *testing.T) {
	root, err := parseHTML([]byte(`<html><body>
<a href="/services/">Services</a>
<a href="https://elilap.dev/case-study/x#top">Case</a>
<a href="https://read.elilap.dev/post">Cross-host</a>
<a href="#anchor">Anchor</a>
<a href="mailto:contact@elilap.dev">Mail</a>
<a>No href</a>
</body></html>`))
	if err != nil {
		t.Fatalf("parseHTML() error = %v", err)
	}
	base, _ := url.Parse("https://elilap.dev/")
	links := pageLinks(root, base)
	var got []string
	for _, u := range links {
		got = append(got, u.String())
	}
	want := []string{"https://elilap.dev/services", "https://elilap.dev/case-study/x", "https://read.elilap.dev/post"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pageLinks() = %v, want %v", got, want)
	}
}

func TestRobotsRules(t *testing.T) {
	body := `
# comment
User-agent: *
Disallow: /tag/
Disallow: /privacy
Allow: /tag/public$

User-agent: elilap-dev-agent
Disallow: /case-study/internal
Allow: /

User-agent: otherbot
Disallow: /
`
	rules := parseRobots(body)
	tests := []struct {
		name  string
		path  string
		agent string
		want  bool
	}{
		{name: "named group allows more than wildcard", path: "/services", agent: "elilap-dev-agent/0.1 (+https://elilap.dev)", want: true},
		{name: "named group disallows its prefix", path: "/case-study/internal/x", agent: "elilap-dev-agent/0.1", want: false},
		{name: "wildcard disallows tag prefix", path: "/tag/go", agent: "unknownbot/1.0", want: false},
		{name: "wildcard allow overrides exact tag", path: "/tag/public", agent: "unknownbot/1.0", want: true},
		{name: "wildcard disallows privacy", path: "/privacy", agent: "unknownbot/1.0", want: false},
		{name: "wildcard allows other paths", path: "/services", agent: "unknownbot/1.0", want: true},
		{name: "named group does not apply to other agents", path: "/case-study/internal", agent: "googlebot/2.1", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.allowed(tc.path, tc.agent); got != tc.want {
				t.Errorf("allowed(%q, %q) = %t, want %t", tc.path, tc.agent, got, tc.want)
			}
		})
	}

	if !(*robotsRules)(nil).allowed("/anything", "elilap-dev-agent") {
		t.Error("nil rules must allow every path")
	}
	empty := parseRobots("")
	if !empty.allowed("/services", "elilap-dev-agent") {
		t.Error("empty robots.txt must allow every path")
	}
}
