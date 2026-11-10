// Package web implements the generic HTML provider: it discovers pages on a
// site (sitemap, feeds, seed URLs), fetches them, and extracts the content
// region as a shared.Document.
//
// The provider is deliberately split in two layers:
//
//   - what to index is configuration (sources.yaml options: site_url,
//     discovery sources, path filters, selectors);
//   - how to read one page is a *strategy*: a generic article/main chain, an
//     explicit CSS-selector extraction, or a host-specific Adapter written in
//     Go (see adapter.go) for sites the generic passes cannot read.
//
// Strategies are selected by name in options.strategy, so a new site is a
// config change and only a genuinely unusual site needs a Go adapter.
package web

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// Strategy names accepted by options.strategy.
const (
	// StrategyAuto runs the configured selectors, then a host adapter, then
	// the generic article chain.
	StrategyAuto = "auto"
	// StrategyArticle uses <article>, then <main>, then [role=main], then
	// <body>, picking the first region with enough text.
	StrategyArticle = "article"
	// StrategySelectors requires options.selectors.content.
	StrategySelectors = "selectors"
	// StrategyBody uses the whole <body> and only removes site chrome.
	StrategyBody = "body"
)

// Option defaults.
const (
	defaultMaxPages = 200
	defaultMinChars = 200
	defaultDelayMS  = 250
	maxCrawlDepth   = 3
)

// Selectors is the CSS-selector configuration of the generic extraction. Any
// field may be left empty; an empty field falls back to the strategy default
// or the page metadata.
type Selectors struct {
	// Content selects the element holding the page body. Required when
	// strategy is "selectors".
	Content string `yaml:"content"`
	// Title selects the headline. Defaults to the first h1 in the content
	// region, then og:title, then the JSON-LD headline/name, then <title>.
	Title string `yaml:"title"`
	// Author selects the byline. Defaults to the JSON-LD author, then the
	// article:author meta tag, then options.author.
	Author string `yaml:"author"`
	// Tags selects tag/category elements; every match contributes one tag.
	Tags string `yaml:"tags"`
	// Date selects a time element. Its datetime attribute wins over its text.
	Date string `yaml:"date"`
	// Remove lists selectors whose elements are deleted from the content
	// region before rendering (share bars, newsletter boxes, ...).
	Remove []string `yaml:"remove"`
}

// Options is the `options:` block of a `type: web` source.
//
// A web source has no credentials: the site is public, so site_url lives here
// rather than behind a { env: VAR } reference (an environment variable is for
// secrets and runtime values, a public URL is configuration).
type Options struct {
	// SiteURL is the absolute origin of the site (https://elilap.dev). Every
	// discovered URL outside its host is ignored.
	SiteURL string `yaml:"site_url"`
	// Sitemap enables discovery through sitemap.xml (following one level of
	// sitemapindex). Default true.
	Sitemap *bool `yaml:"sitemap"`
	// Feeds enables discovery through /rss.xml, /feed.xml, and /atom.xml.
	// Default true.
	Feeds *bool `yaml:"feeds"`
	// StartURLs are extra seeds, absolute or site-relative. They are always
	// listed, which is how a site without a sitemap or feed is ingested.
	StartURLs []string `yaml:"start_urls"`
	// CrawlDepth follows same-site links from the seed URLs this many levels
	// deep (0 disables crawling). Sitemap and feed results are not crawled.
	CrawlDepth int `yaml:"crawl_depth"`
	// IncludePaths keeps only URLs whose path matches one of these regular
	// expressions. Empty means every path is a candidate.
	IncludePaths []string `yaml:"include_paths"`
	// ExcludePaths drops URLs whose path matches any of these regular
	// expressions. Applied after IncludePaths.
	ExcludePaths []string `yaml:"exclude_paths"`
	// MaxPages caps how many pages one listing returns. Default 200.
	MaxPages int `yaml:"max_pages"`
	// Strategy names the extraction strategy: auto, article, selectors, body,
	// or the name of a registered Adapter. Default auto.
	Strategy string `yaml:"strategy"`
	// Selectors configures the generic extraction (see Selectors).
	Selectors Selectors `yaml:"selectors"`
	// Author is the fallback byline for pages that expose none.
	Author string `yaml:"author"`
	// MinChars is the amount of text a candidate region must hold before it
	// is accepted; below it the next candidate (and finally <body>) wins.
	// Default 200.
	MinChars int `yaml:"min_chars"`
	// DelayMS is the politeness delay between two requests to the site, in
	// milliseconds. Unset means 250; an explicit 0 disables the delay.
	DelayMS *int `yaml:"delay_ms"`
	// RespectRobots honours /robots.txt (default true). It should only be
	// turned off for a site you control and have verified has no robots.txt.
	RespectRobots *bool `yaml:"respect_robots"`

	// Compiled include/exclude patterns; filled by ParseOptions.
	include []*regexp.Regexp
	exclude []*regexp.Regexp
}

// ParseOptions strict-decodes and validates a source's options for the web
// provider. It is registered in the sources registry as type "web".
func ParseOptions(cfg config.Source) (any, error) {
	var o Options
	if err := cfg.OptionsInto(&o); err != nil {
		return nil, err
	}
	if err := o.normalize(); err != nil {
		return nil, err
	}
	return &o, nil
}

// normalize validates the options and applies defaults. It is separate from
// ParseOptions so tests can exercise it without a config.Source.
func (o *Options) normalize() error {
	site, err := parseSiteURL(o.SiteURL)
	if err != nil {
		return err
	}
	o.SiteURL = site.String()

	if o.MaxPages == 0 {
		o.MaxPages = defaultMaxPages
	}
	if o.MaxPages < 0 {
		return errors.New("options.max_pages must not be negative")
	}
	if o.MinChars == 0 {
		o.MinChars = defaultMinChars
	}
	if o.MinChars < 0 {
		return errors.New("options.min_chars must not be negative")
	}
	if o.DelayMS == nil {
		delay := defaultDelayMS
		o.DelayMS = &delay
	}
	if *o.DelayMS < 0 {
		return errors.New("options.delay_ms must not be negative")
	}
	if o.CrawlDepth < 0 || o.CrawlDepth > maxCrawlDepth {
		return fmt.Errorf("options.crawl_depth must be between 0 and %d", maxCrawlDepth)
	}

	o.Strategy = strings.ToLower(strings.TrimSpace(o.Strategy))
	if o.Strategy == "" {
		o.Strategy = StrategyAuto
	}
	if !o.knownStrategy(o.Strategy) {
		return fmt.Errorf("options.strategy %q is unknown (known: auto, article, selectors, body%s)",
			o.Strategy, adapterNamesSuffix())
	}

	if o.Strategy == StrategySelectors && strings.TrimSpace(o.Selectors.Content) == "" {
		return errors.New("options.selectors.content is required when strategy is selectors")
	}
	if err := validateSelector("options.selectors.content", o.Selectors.Content); err != nil {
		return err
	}
	if err := validateSelector("options.selectors.title", o.Selectors.Title); err != nil {
		return err
	}
	if err := validateSelector("options.selectors.author", o.Selectors.Author); err != nil {
		return err
	}
	if err := validateSelector("options.selectors.tags", o.Selectors.Tags); err != nil {
		return err
	}
	if err := validateSelector("options.selectors.date", o.Selectors.Date); err != nil {
		return err
	}
	for i, sel := range o.Selectors.Remove {
		if err := validateSelector(fmt.Sprintf("options.selectors.remove[%d]", i), sel); err != nil {
			return err
		}
	}

	var errs []error
	if o.include, err = compilePatterns("options.include_paths", o.IncludePaths); err != nil {
		errs = append(errs, err)
	}
	if o.exclude, err = compilePatterns("options.exclude_paths", o.ExcludePaths); err != nil {
		errs = append(errs, err)
	}

	seeds := make([]string, 0, len(o.StartURLs))
	for i, raw := range o.StartURLs {
		u, err := site.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Errorf("options.start_urls[%d]: %q is not an http(s) URL", i, raw))
			continue
		}
		if u.Host != site.Host {
			errs = append(errs, fmt.Errorf("options.start_urls[%d]: %q is outside %s", i, raw, site.Host))
			continue
		}
		seeds = append(seeds, normalizeURL(u).String())
	}
	sort.Strings(seeds)
	o.StartURLs = seeds
	return errors.Join(errs...)
}

// knownStrategy reports whether name is a built-in or registered strategy.
func (o *Options) knownStrategy(name string) bool {
	switch name {
	case StrategyAuto, StrategyArticle, StrategySelectors, StrategyBody:
		return true
	}
	for _, a := range DefaultAdapters() {
		if a.Name() == name {
			return true
		}
	}
	return false
}

// adapterNames lists the registered adapter names for error messages.
func adapterNames() []string {
	adapters := DefaultAdapters()
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
	}
	sort.Strings(names)
	return names
}

// adapterNamesSuffix renders ", or a registered adapter (elilap, ...)".
func adapterNamesSuffix() string {
	names := adapterNames()
	if len(names) == 0 {
		return ""
	}
	return ", or a registered adapter (" + strings.Join(names, ", ") + ")"
}

// delay is the configured politeness delay between requests.
func (o *Options) delay() time.Duration {
	if o.DelayMS == nil {
		return defaultDelayMS * time.Millisecond
	}
	return time.Duration(*o.DelayMS) * time.Millisecond
}

// sitemapEnabled reports whether sitemap discovery is on (default true).
func (o *Options) sitemapEnabled() bool { return o.Sitemap == nil || *o.Sitemap }

// feedsEnabled reports whether feed discovery is on (default true).
func (o *Options) feedsEnabled() bool { return o.Feeds == nil || *o.Feeds }

// robotsEnabled reports whether robots.txt is honoured (default true).
func (o *Options) robotsEnabled() bool { return o.RespectRobots == nil || *o.RespectRobots }

// MatchesHost reports whether u belongs to the configured site: the same host
// or its www alias, ignoring scheme and port. It is the one host rule the
// client, discovery, the adapters, and `agent sources inspect` share.
func (o *Options) MatchesHost(u *url.URL) bool {
	site, err := url.Parse(o.SiteURL)
	if err != nil || site.Host == "" {
		return false
	}
	return sameSiteHost(site.Hostname(), u)
}

// pathAllowed applies include_paths and exclude_paths to a URL path.
func (o *Options) pathAllowed(path string) bool {
	if path == "" {
		path = "/"
	}
	if len(o.include) > 0 && !matchesAny(o.include, path) {
		return false
	}
	return !matchesAny(o.exclude, path)
}

// matchesAny reports whether any pattern matches s.
func matchesAny(patterns []*regexp.Regexp, s string) bool {
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// compilePatterns compiles a list of regular expressions, naming the field
// and the offending value in the error.
func compilePatterns(field string, raws []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for i, raw := range raws {
		re, err := regexp.Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %q is not a valid regular expression: %w", field, i, raw, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// parseSiteURL validates the configured origin: an absolute http(s) URL with
// no query or fragment.
func parseSiteURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("options.site_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("options.site_url %q is not a URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("options.site_url %q must be http or https", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("options.site_url %q has no host", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("options.site_url %q must not carry a query or fragment", raw)
	}
	// Discovery composes /sitemap.xml, /robots.txt, and the feed probes from
	// the origin, so a base path would be silently ignored (or, worse, only
	// some requests would honour it).
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("options.site_url %q must be an origin without a path", raw)
	}
	return normalizeURL(u), nil
}

// normalizeURL lower-cases the scheme and host, drops query and fragment, and
// removes a trailing slash (except at the root). The result is the canonical
// identity of a page.
func normalizeURL(u *url.URL) *url.URL {
	clean := *u
	clean.Scheme = strings.ToLower(clean.Scheme)
	clean.Host = strings.ToLower(clean.Host)
	clean.RawQuery = ""
	clean.Fragment = ""
	clean.RawFragment = ""
	clean.ForceQuery = false
	if clean.Path == "" {
		clean.Path = "/"
	}
	if clean.Path != "/" {
		clean.Path = strings.TrimSuffix(clean.Path, "/")
	}
	return &clean
}

// pageID derives the ledger item id: host plus path ("elilap.dev/services").
// It is stable across schemes and trailing slashes, and namespaced by host so
// two web sources with colliding paths stay distinct (chunks.source_id is
// shared across every web source).
func pageID(u *url.URL) string {
	clean := normalizeURL(u)
	return clean.Host + clean.Path
}

// DefaultOptions returns options for a site with every default applied, for
// tools that inspect a URL before a source exists in sources.yaml (the
// `agent sources inspect` authoring path). It is exactly what an
// `options: { site_url: ... }` block produces.
func DefaultOptions(siteURL string) (*Options, error) {
	o := &Options{SiteURL: siteURL}
	if err := o.normalize(); err != nil {
		return nil, err
	}
	return o, nil
}
