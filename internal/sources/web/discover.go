package web

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"golang.org/x/net/html"
)

// Discovery limits.
const (
	// sitemapName is the conventional sitemap location.
	sitemapName = "sitemap.xml"
	// maxChildSitemaps bounds how many sitemaps of a sitemapindex are read.
	maxChildSitemaps = 20
	// feedPaths are the conventional feed locations, probed when feeds are
	// enabled. A 404 on any of them is expected, not an error.
	feedPaths = "/rss.xml,/feed.xml,/atom.xml,/feed"
)

// candidate is one discovered page before the ledger sees it.
type candidate struct {
	// URL is the normalized absolute page URL.
	URL *url.URL
	// LastModified is the sitemap/feed timestamp, zero when the discovery
	// source published none.
	LastModified time.Time
}

// discovery is one listing pass: the merged pages, the per-source counts, and
// the supplementary sources (feeds, crawl) that failed. Incomplete is nil
// when every enabled source answered; a non-nil value means the listing may be
// missing pages, which an unbounded run must not treat as deletions.
type discovery struct {
	candidates []candidate
	summary    DiscoverySummary
	incomplete error
}

// discover lists the site's pages from every enabled discovery source:
// sitemap.xml (following one sitemapindex level), the conventional feeds, the
// configured start_urls, and an optional bounded same-site crawl.
//
// A source that is merely absent (404) contributes nothing. A failed sitemap
// is a hard error; a failed feed or crawl is reported through
// discovery.incomplete, because only the caller knows whether the run is
// unbounded and would therefore delete pages the listing did not see.
func (p *Provider) discover(ctx context.Context) (discovery, error) {
	return p.discoverWith(ctx, true)
}

// discoverWith is discover with the crawl switchable. `agent sources inspect`
// passes false: it needs the page counts and one page's timestamp, not a walk
// over the site.
func (p *Provider) discoverWith(ctx context.Context, includeCrawl bool) (discovery, error) {
	summary := DiscoverySummary{}
	log := logging.Sources().With().Str("provider", p.Type()).Str("site", p.opts.SiteURL).Logger()
	site := p.site
	byURL := make(map[string]candidate)
	add := func(c candidate) {
		if c.URL == nil {
			return
		}
		u := normalizeURL(c.URL)
		// Same rule the client and the adapters use: the www alias is the same
		// site, or a sitemap written against the canonical host would have
		// every page dropped.
		if !sameSiteHost(site.Hostname(), u) || !p.opts.pathAllowed(u.Path) {
			return
		}
		if skipExtension(u.Path) {
			return
		}
		key := u.String()
		if existing, ok := byURL[key]; ok {
			// Keep the newest timestamp any discovery source reported: a feed's
			// publish date plus a sitemap's lastmod must not hide a later edit
			// behind the older of the two.
			if c.LastModified.After(existing.LastModified) {
				existing.LastModified = c.LastModified
			}
			byURL[key] = existing
			return
		}
		byURL[key] = c
	}

	var (
		errs       []error // sitemap failures: always fatal, the listing is untrustworthy
		incomplete []error // feed/crawl failures: fatal only on an unbounded listing
	)
	if p.opts.sitemapEnabled() {
		found, err := p.sitemapCandidates(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		summary.SitemapURLs = len(found)
		for _, c := range found {
			add(c)
		}
		log.Debug().Int("urls", len(found)).Msg("sitemap discovery")
	}
	if p.opts.feedsEnabled() {
		found, err := p.feedCandidates(ctx)
		if err != nil {
			// Feeds are supplementary: the sitemap usually carries the pages,
			// and a probe can legitimately hit an HTML page. The caller fails
			// an unbounded run on `incomplete`, so drift deletion stays safe.
			incomplete = append(incomplete, err)
			log.Warn().Err(err).Msg("feed discovery failed, continuing with other sources")
		}
		summary.FeedURLs = len(found)
		for _, c := range found {
			add(c)
		}
		log.Debug().Int("urls", len(found)).Msg("feed discovery")
	}
	summary.SeedURLs = len(p.opts.StartURLs)
	for _, raw := range p.opts.StartURLs {
		u, err := site.Parse(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("start_url %q: %w", raw, err))
			continue
		}
		add(candidate{URL: u})
	}
	if includeCrawl && p.opts.CrawlDepth > 0 {
		found, err := p.crawl(ctx)
		if err != nil {
			// A crawl is explicitly configured, so a failure means the listing
			// may be missing exactly the pages the crawl was meant to find.
			incomplete = append(incomplete, err)
			log.Warn().Err(err).Msg("link crawl failed, continuing with discovered pages")
		}
		summary.CrawlURLs = len(found)
		for _, c := range found {
			add(c)
		}
		log.Debug().Int("urls", len(found)).Msg("crawl discovery")
	}

	candidates := make([]candidate, 0, len(byURL))
	for _, c := range byURL {
		candidates = append(candidates, c)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].URL.String() < candidates[j].URL.String()
	})

	summary.TotalURLs = len(candidates)
	incompleteErr := errors.Join(incomplete...)
	if incompleteErr != nil {
		summary.Incomplete = incompleteErr.Error()
	}
	result := discovery{candidates: candidates, summary: summary, incomplete: incompleteErr}

	if len(errs) > 0 {
		// A failed sitemap means the listing is untrustworthy on every run:
		// fail even when other sources found pages, rather than let a partial
		// listing delete what it did not see.
		return result, fmt.Errorf("page discovery failed (%d page(s) found): %w",
			len(candidates), errors.Join(errs...))
	}
	return result, nil
}

// DiscoverySummary counts what each discovery source contributed. It is
// reported by `agent sources inspect` so an author can see whether the
// sitemap, the feeds, or the seeds are doing the work.
type DiscoverySummary struct {
	SitemapURLs int `json:"sitemap_urls"`
	FeedURLs    int `json:"feed_urls"`
	SeedURLs    int `json:"seed_urls"`
	CrawlURLs   int `json:"crawl_urls"`
	TotalURLs   int `json:"total_urls"`
	// Incomplete names the supplementary sources (feeds, crawl) that failed,
	// empty when discovery was complete.
	Incomplete string `json:"incomplete,omitempty"`
}

// sitemapCandidates reads /sitemap.xml, following one sitemapindex level.
// An absent sitemap is not an error (many sites have none); a failed one is.
func (p *Provider) sitemapCandidates(ctx context.Context) ([]candidate, error) {
	target := p.site.JoinPath(sitemapName)
	resp, err := p.client.get(ctx, target)
	if err != nil {
		if isNotExist(err) {
			log := logging.Sources().With().Str("provider", p.Type()).Logger()
			log.Debug().Str("url", target.Redacted()).Msg("no sitemap.xml")
			return nil, nil
		}
		return nil, fmt.Errorf("sitemap: %w", err)
	}
	doc, err := parseSitemap(resp.Body)
	if err != nil {
		if looksLikeHTML(resp) {
			// An SPA fallback or soft-404 serves index.html at every path,
			// /sitemap.xml included. That is an absent sitemap, not a broken
			// one: feeds and seeds still list the site.
			log := logging.Sources().With().Str("provider", p.Type()).Logger()
			log.Debug().Str("url", target.Redacted()).Msg("sitemap answered with html, treating it as absent")
			return nil, nil
		}
		return nil, fmt.Errorf("sitemap %s: %w", target.Redacted(), err)
	}

	var out []candidate
	for _, e := range doc.URLs {
		u, err := p.resolve(e.Loc)
		if err != nil {
			continue
		}
		out = append(out, candidate{URL: u, LastModified: parseStamp(e.LastMod)})
	}
	children := doc.Sitemaps
	if len(children) > maxChildSitemaps {
		// Truncating here would silently drop pages, and an unbounded run would
		// read the missing pages as deletions. Refuse the listing instead.
		return nil, fmt.Errorf(
			"sitemap %s: %d child sitemaps exceed the %d-sitemap cap; refusing a partial listing",
			target.Redacted(), len(children), maxChildSitemaps)
	}
	for _, child := range children {
		u, err := p.resolve(child.Loc)
		if err != nil {
			continue
		}
		if u.Host != p.site.Host {
			// The sitemapindex is site-controlled content, not operator
			// content: never let it point the fetcher at another host.
			log := logging.Sources().With().Str("provider", p.Type()).Logger()
			log.Debug().Str("child", u.Redacted()).Str("site", p.site.Host).
				Msg("skipping sitemap child on another host")
			continue
		}
		sub, err := p.client.get(ctx, u)
		if err != nil {
			if isNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("child sitemap %s: %w", u.Redacted(), err)
		}
		subDoc, err := parseSitemap(sub.Body)
		if err != nil {
			return nil, fmt.Errorf("child sitemap %s: %w", u.Redacted(), err)
		}
		if subDoc.XMLName.Local == "sitemapindex" {
			// Nested indexes occur in the wild. Following them is not
			// implemented, and ignoring one would silently drop its pages, so
			// refuse the listing instead.
			return nil, fmt.Errorf("child sitemap %s is itself a sitemapindex; refusing a partial listing", u.Redacted())
		}
		for _, e := range subDoc.URLs {
			page, err := p.resolve(e.Loc)
			if err != nil {
				continue
			}
			out = append(out, candidate{URL: page, LastModified: parseStamp(e.LastMod)})
		}
	}
	return out, nil
}

// feedCandidates probes the conventional feed paths and returns their entries.
// A 404/410/401/403 is an absent feed; any other failure is reported to the
// caller, which decides whether it is fatal.
func (p *Provider) feedCandidates(ctx context.Context) ([]candidate, error) {
	var (
		out  []candidate
		errs []error
	)
	for _, path := range strings.Split(feedPaths, ",") {
		target := &url.URL{Scheme: p.site.Scheme, Host: p.site.Host, Path: path}
		resp, err := p.client.get(ctx, target)
		if err != nil {
			if !isProbeAbsent(err) {
				errs = append(errs, err)
			}
			continue
		}
		entries, err := parseFeed(resp.Body)
		if err != nil {
			// A 200 HTML page at /feed is an absent feed, not a broken one:
			// the probe is best-effort and the site is not expected to serve
			// a feed at every conventional path.
			log := logging.Sources().With().Str("provider", p.Type()).Logger()
			log.Debug().Err(err).Str("url", target.Redacted()).Msg("probe is not a feed")
			continue
		}
		for _, e := range entries {
			u, err := p.resolve(e.Link)
			if err != nil {
				continue
			}
			out = append(out, candidate{
				URL:          u,
				LastModified: latestTime(e.Published, e.Updated),
			})
		}
	}
	return out, errors.Join(errs...)
}

// crawl follows same-site links from the configured start URLs, up to
// options.crawl_depth hops: depth 1 lists the links found on the seeds, and the
// seeds themselves are always listed separately. A page that fails to fetch is
// skipped with a warning, but a crawl that reaches none of its seeds at all is
// an error: the listing is then missing exactly what the crawl was configured
// to find.
func (p *Provider) crawl(ctx context.Context) ([]candidate, error) {
	log := logging.Sources().With().Str("provider", p.Type()).Logger()
	seen := make(map[string]bool)
	queue := make([]candidate, 0, len(p.opts.StartURLs))
	for _, raw := range p.opts.StartURLs {
		u, err := p.site.Parse(raw)
		if err != nil {
			continue
		}
		queue = append(queue, candidate{URL: normalizeURL(u)})
	}
	var (
		out     []candidate
		attempt int
		fetched int
		errs    []error
	)
	for depth := 0; depth < p.opts.CrawlDepth && len(queue) > 0; depth++ {
		var next []candidate
		for _, c := range queue {
			key := c.URL.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			attempt++
			resp, err := p.client.get(ctx, c.URL)
			if err != nil {
				log.Warn().Err(err).Msg("crawl page skipped")
				errs = append(errs, err)
				continue
			}
			fetched++
			root, err := parseHTML(resp.Body)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.URL.Redacted(), err))
				continue
			}
			for _, link := range pageLinks(root, resp.URL) {
				if !sameSiteHost(p.site.Hostname(), link) || seen[link.String()] ||
					!p.opts.pathAllowed(link.Path) || skipExtension(link.Path) {
					continue
				}
				next = append(next, candidate{URL: link})
			}
		}
		out = append(out, next...)
		queue = next
	}
	if len(errs) > 0 {
		// Every failed crawl page hides its own links, so any failure makes the
		// crawl's contribution to the listing incomplete - the caller decides
		// whether that is fatal (unbounded run) or a warning (a window).
		return out, fmt.Errorf("crawl fetched %d of %d page(s): %w", fetched, attempt, errors.Join(errs...))
	}
	return out, nil
}

// siteURL is one sitemap <url> or <sitemap> entry.
type siteURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// sitemapDoc is either a <urlset> or a <sitemapindex>.
type sitemapDoc struct {
	XMLName  xml.Name
	URLs     []siteURL `xml:"url"`
	Sitemaps []siteURL `xml:"sitemap"`
}

// looksLikeHTML reports whether a response is an HTML document rather than the
// XML the caller expected: an SPA fallback or a soft-404 page.
func looksLikeHTML(resp *response) bool {
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") {
		return true
	}
	head := resp.Body
	if len(head) > 512 {
		head = head[:512]
	}
	body := strings.ToLower(strings.TrimSpace(string(head)))
	return strings.HasPrefix(body, "<!doctype html") || strings.HasPrefix(body, "<html")
}

// parseSitemap decodes a sitemap document. A sitemapindex and a urlset differ
// only in their entry element, so one struct covers both.
func parseSitemap(body []byte) (*sitemapDoc, error) {
	var doc sitemapDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode sitemap xml: %w", err)
	}
	if doc.XMLName.Local != "urlset" && doc.XMLName.Local != "sitemapindex" {
		return nil, fmt.Errorf("unexpected sitemap root <%s>", doc.XMLName.Local)
	}
	return &doc, nil
}

// feedEntry is the normalized shape of an RSS item or an Atom entry.
type feedEntry struct {
	Title     string
	Link      string
	Published time.Time
	Updated   time.Time
}

// rssFeed is the RSS 2.0 shape discovery reads.
type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
			Date    string `xml:"date"`
		} `xml:"item"`
	} `xml:"channel"`
}

// atomFeed is the Atom shape discovery reads.
type atomFeed struct {
	XMLName xml.Name `xml:"feed"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Published string `xml:"published"`
		Updated   string `xml:"updated"`
	} `xml:"entry"`
}

// parseFeed decodes an RSS 2.0 or Atom feed into normalized entries.
func parseFeed(body []byte) ([]feedEntry, error) {
	var probe struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("decode feed xml: %w", err)
	}
	switch probe.XMLName.Local {
	case "rss":
		var f rssFeed
		if err := xml.Unmarshal(body, &f); err != nil {
			return nil, fmt.Errorf("decode rss: %w", err)
		}
		out := make([]feedEntry, 0, len(f.Channel.Items))
		for _, item := range f.Channel.Items {
			published := parseStamp(item.PubDate)
			out = append(out, feedEntry{
				Title:     item.Title,
				Link:      item.Link,
				Published: published,
				// dc:date is the modification time; a later edit must not be
				// hidden behind the original publication date.
				Updated: latestTime(parseStamp(item.Date), published),
			})
		}
		return out, nil
	case "feed":
		var f atomFeed
		if err := xml.Unmarshal(body, &f); err != nil {
			return nil, fmt.Errorf("decode atom: %w", err)
		}
		out := make([]feedEntry, 0, len(f.Entries))
		for _, entry := range f.Entries {
			out = append(out, feedEntry{
				Title:     entry.Title,
				Link:      atomLink(entry.Links),
				Published: parseStamp(entry.Published),
				Updated:   latestTime(parseStamp(entry.Updated), parseStamp(entry.Published)),
			})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected feed root <%s>", probe.XMLName.Local)
	}
}

// atomLink picks the alternate link of an Atom entry.
func atomLink(links []struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}) string {
	var fallback string
	for _, l := range links {
		if fallback == "" {
			fallback = l.Href
		}
		if l.Rel == "" || l.Rel == "alternate" {
			return l.Href
		}
	}
	return fallback
}

// pageLinks returns every absolute, in-site URL referenced by an <a href>.
func pageLinks(root *html.Node, base *url.URL) []*url.URL {
	var out []*url.URL
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				href := strings.TrimSpace(a.Val)
				if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") ||
					strings.HasPrefix(href, "tel:") || strings.HasPrefix(href, "javascript:") {
					continue
				}
				u, err := base.Parse(href)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
					continue
				}
				out = append(out, normalizeURL(u))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// skipExtension reports whether a path names a non-page asset. Sitemaps list
// a handful of these, and a crawl finds many.
func skipExtension(path string) bool {
	last := path
	if i := strings.LastIndexByte(last, '/'); i >= 0 {
		last = last[i+1:]
	}
	dot := strings.LastIndexByte(last, '.')
	if dot < 0 {
		return false
	}
	switch strings.ToLower(last[dot:]) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".svg", ".ico",
		".css", ".js", ".mjs", ".json", ".xml", ".txt", ".pdf", ".zip",
		".gz", ".tar", ".woff", ".woff2", ".ttf", ".otf", ".mp3", ".mp4",
		".webm", ".rss", ".atom":
		return true
	}
	return false
}

// resolve parses a sitemap/feed reference against the site origin.
func (p *Provider) resolve(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty url")
	}
	u, err := p.site.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("unusable url %q", raw)
	}
	return normalizeURL(u), nil
}

// timeLayouts are the timestamp shapes sitemaps and feeds publish.
var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"2006-01-02T15:04:05",
	"2006-01-02",
	// JavaScript Date#toString, which is what a hand-written <time datetime>
	// carries ("Sat Nov 16 2024 00:00:00 GMT+0000 (...)").
	"Mon Jan 2 2006 15:04:05 GMT-0700",
}

// parseStamp parses a sitemap/feed timestamp, returning the zero time for an
// empty or unrecognized value.
func parseStamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	// JavaScript's Date#toString appends a zone label ("... GMT+0000
	// (Coordinated Universal Time)"); the label is not part of the layout.
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		s = s[:i]
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// latestTime returns the newest non-zero time, or the zero time when every
// argument is zero. "Newest" is the right rule for an item's date: a feed's
// update time and a sitemap's lastmod both report the last change, and the
// older of the two would hide an edit from an incremental window.
func latestTime(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// firstTime returns the first non-zero time.
func firstTime(times ...time.Time) time.Time {
	for _, t := range times {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
