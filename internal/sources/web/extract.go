package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// ErrNoContent reports that a page yielded nothing indexable (an empty
// placeholder route, a redirect stub). The provider skips such a page with a
// warning and records it in the ledger, so one content-free URL cannot fail a
// whole site ingest.
var ErrNoContent = errors.New("page has no extractable content")

// Page is one extracted HTML page: the content region plus the metadata the
// shared.Document needs. It is the extractor's output, not a provider type.
type Page struct {
	Title       string
	Body        string // Markdown rendering of the content region
	HTML        string // content region HTML (preferred by the chunker)
	Author      string
	Tags        []string
	PublishedAt time.Time
	UpdatedAt   time.Time
	// NoIndex is set when the page's robots meta asks not to be indexed; the
	// provider then writes a ledger row but no chunks.
	NoIndex bool
	// Strategy names the strategy that produced the page (article, selectors,
	// body, or an adapter name).
	Strategy string
	// Description is the meta description, kept in chunk metadata.
	Description string
	// JSONLDTypes lists the schema.org types the page published.
	JSONLDTypes []string
}

// Extractor applies one source's strategy configuration to parsed pages. It
// is stateless apart from the options and adapters it was built with, so one
// instance serves a whole run.
type Extractor struct {
	opts     *Options
	adapters []Adapter
}

// newExtractor builds an extractor over the given adapters (nil means
// DefaultAdapters).
func newExtractor(opts *Options, adapters []Adapter) *Extractor {
	if adapters == nil {
		adapters = DefaultAdapters()
	}
	return &Extractor{opts: opts, adapters: adapters}
}

// Extract turns a parsed page into a Page. It reports an error only when the
// page yields nothing indexable at all; a thin page is a warning, because one
// link-heavy page must not fail the whole source.
func (e *Extractor) Extract(u *url.URL, root *html.Node) (Page, error) {
	md := readMetadata(root)
	content, strategy, sel, err := e.selectRegion(root, u, &md)
	if err != nil {
		return Page{}, err
	}
	if content == nil {
		return Page{}, fmt.Errorf("web: %s: %w", u.Redacted(), ErrNoContent)
	}

	// The headline is read before chrome removal so a layout that wraps the
	// h1 in <header> cannot lose its title.
	title := firstNonEmpty(
		sel.Title,
		e.selectorText(content, e.opts.Selectors.Title),
		firstHeadingText(content),
		md.headline,
		md.title,
	)
	author := firstNonEmpty(
		sel.Author,
		e.selectorText(content, e.opts.Selectors.Author),
		md.author,
		e.opts.Author,
	)
	tags := append([]string{}, sel.Tags...)
	tags = append(tags, e.selectorAll(content, e.opts.Selectors.Tags)...)
	tags = append(tags, md.tags...)
	tags = dedupeStrings(tags)

	published, updated := e.dates(content, md)

	removeChrome(content)
	for _, raw := range append(append([]string{}, e.opts.Selectors.Remove...), sel.RemoveSelectors...) {
		compile, err := compileSelector("remove", raw)
		if err != nil {
			continue // validated at parse time; ignore here
		}
		for _, n := range queryAll(content, compile) {
			unlink(n)
		}
	}

	body, err := markdownOf(content)
	if err != nil {
		return Page{}, err
	}
	raw, err := renderHTML(content)
	if err != nil {
		return Page{}, err
	}
	if title == "" && strings.TrimSpace(body) == "" {
		return Page{}, fmt.Errorf("web: %s: %w", u.Redacted(), ErrNoContent)
	}
	if len(tags) == 0 {
		tags = sectionTags(u)
	}

	log := logging.Sources().With().Str("provider", "web").Str("url", u.Redacted()).Logger()
	log.Debug().
		Str("strategy", strategy).
		Int("chars", len(body)).
		Int("tags", len(tags)).
		Bool("noindex", md.noIndex).
		Msg("extracted page")
	if len(body) < e.opts.MinChars {
		// Thin pages are legitimate (contact, tags), but a page this small is
		// usually a mis-selected region; the line is what makes that visible.
		log.Warn().
			Int("chars", len(body)).
			Int("min_chars", e.opts.MinChars).
			Str("strategy", strategy).
			Msg("extracted content is shorter than min_chars")
	}

	return Page{
		Title:       strings.TrimSpace(title),
		Body:        body,
		HTML:        raw,
		Author:      strings.TrimSpace(author),
		Tags:        tags,
		PublishedAt: published,
		UpdatedAt:   updated,
		NoIndex:     md.noIndex,
		Strategy:    strategy,
		Description: md.description,
		JSONLDTypes: md.types,
	}, nil
}

// selectRegion applies the configured strategy and returns the content node,
// the strategy name that won, and any adapter selection (zero for the generic
// strategies).
func (e *Extractor) selectRegion(root *html.Node, u *url.URL, md *pageMetadata) (*html.Node, string, Selection, error) {
	switch e.opts.Strategy {
	case StrategySelectors:
		sel, err := compileSelector("options.selectors.content", e.opts.Selectors.Content)
		if err != nil {
			return nil, "", Selection{}, err
		}
		node := queryFirst(root, sel)
		if node == nil {
			return nil, "", Selection{}, fmt.Errorf(
				"web: %s: content selector %q matched nothing",
				u.Redacted(), e.opts.Selectors.Content)
		}
		return node, StrategySelectors, Selection{}, nil

	case StrategyBody:
		body := findFirstTag(root, "body")
		if body == nil {
			body = root
		}
		return body, StrategyBody, Selection{}, nil

	case StrategyArticle:
		return articleRegion(root, e.opts.MinChars), StrategyArticle, Selection{}, nil

	case StrategyAuto:
		if strings.TrimSpace(e.opts.Selectors.Content) != "" {
			sel, err := compileSelector("options.selectors.content", e.opts.Selectors.Content)
			if err == nil {
				if node := queryFirst(root, sel); node != nil {
					return node, StrategySelectors, Selection{}, nil
				}
				log := logging.Sources().With().Str("provider", "web").Logger()
				log.Warn().Str("selector", e.opts.Selectors.Content).
					Msg("content selector matched nothing, falling back to the generic chain")
			}
		}
		if adapter, ok := selectAdapter(e.adapters, StrategyAuto, u); ok {
			sel, err := adapter.Select(root, u)
			if err == nil && sel.Node != nil {
				return sel.Node, adapter.Name(), sel, nil
			}
			log := logging.Sources().With().Str("provider", "web").Logger()
			log.Warn().Err(err).Str("adapter", adapter.Name()).
				Msg("adapter extraction failed, falling back to the generic chain")
		}
		return articleRegion(root, e.opts.MinChars), StrategyArticle, Selection{}, nil

	default:
		adapter, ok := adapterByName(e.adapters, e.opts.Strategy)
		if !ok {
			return nil, "", Selection{}, fmt.Errorf("web: unknown strategy %q", e.opts.Strategy)
		}
		sel, err := adapter.Select(root, u)
		if err != nil {
			return nil, "", Selection{}, fmt.Errorf("web: adapter %s: %w", adapter.Name(), err)
		}
		if sel.Node == nil {
			return nil, "", Selection{}, fmt.Errorf("web: adapter %s selected no content for %s",
				adapter.Name(), u.Redacted())
		}
		return sel.Node, adapter.Name(), sel, nil
	}
}

// articleRegion returns the first content-ish region holding at least
// minChars of text, falling back to <body> when every candidate is thin.
func articleRegion(root *html.Node, minChars int) *html.Node {
	for _, selector := range []string{"article", "main", "[role=main]", "body"} {
		sel, err := cascadia.Compile(selector)
		if err != nil {
			continue
		}
		if node := queryFirst(root, sel); node != nil {
			if len(textOf(node)) >= minChars || selector == "body" {
				return node
			}
		}
	}
	if body := findFirstTag(root, "body"); body != nil {
		return body
	}
	return root
}

// chromeSelector matches navigation and non-prose elements removed from every
// content region. <header> is deliberately absent: article layouts often put
// the headline there.
var chromeSelector = cascadia.MustCompile("nav,footer,aside,form,iframe,svg,button,noscript,template,script,style")

// removeChrome deletes navigation and non-prose elements from a content
// region.
func removeChrome(content *html.Node) {
	for _, n := range queryAll(content, chromeSelector) {
		unlink(n)
	}
}

// pageMetadata is the machine-readable metadata of one page, gathered from
// <title>, meta tags, and JSON-LD.
type pageMetadata struct {
	title       string
	description string
	author      string
	headline    string
	tags        []string
	types       []string
	noIndex     bool
	published   time.Time
	updated     time.Time
}

// readMetadata walks the document head: <title>, <meta>, and JSON-LD blocks.
func readMetadata(root *html.Node) pageMetadata {
	md := pageMetadata{}
	if title := findFirstTag(root, "title"); title != nil {
		md.title = strings.TrimSpace(textOf(title))
	}
	meta := map[string]string{}
	var articleTags []string
	for _, n := range findElements(root, "meta", nil) {
		key := strings.ToLower(firstNonEmpty(attrVal(n, "property"), attrVal(n, "name")))
		value := strings.TrimSpace(attrVal(n, "content"))
		if key == "" || value == "" {
			continue
		}
		// article:tag is repeatable by Open Graph spec; every occurrence is a
		// tag, unlike the single-valued keys below where the first wins.
		if key == "article:tag" {
			articleTags = append(articleTags, splitKeywords(value)...)
			continue
		}
		if _, seen := meta[key]; !seen {
			meta[key] = value
		}
	}
	md.tags = append(md.tags, articleTags...)
	md.description = firstNonEmpty(meta["description"], meta["og:description"])
	md.headline = firstNonEmpty(meta["og:title"], meta["twitter:title"])
	md.published = firstTime(
		parseStamp(meta["article:published_time"]),
		parseStamp(meta["datepublished"]),
		parseStamp(meta["og:published_time"]),
	)
	md.updated = firstTime(
		parseStamp(meta["article:modified_time"]),
		parseStamp(meta["datemodified"]),
		parseStamp(meta["og:updated_time"]),
	)
	md.author = firstNonEmpty(meta["author"], meta["article:author"])
	if strings.HasPrefix(md.author, "http://") || strings.HasPrefix(md.author, "https://") {
		// article:author is often a profile URL, not a byline.
		md.author = ""
	}
	if v, ok := meta["keywords"]; ok {
		md.tags = append(md.tags, splitKeywords(v)...)
	}
	md.noIndex = strings.Contains(strings.ToLower(meta["robots"]), "noindex")

	for _, script := range findElements(root, "script", nil) {
		if !strings.Contains(strings.ToLower(attrVal(script, "type")), "ld+json") {
			continue
		}
		raw := strings.TrimSpace(rawTextOf(script))
		if raw == "" {
			continue
		}
		md.mergeJSONLD(raw)
	}
	return md
}

// mergeJSONLD folds one JSON-LD block into the metadata. A block may be a
// single object, an array, or an object with an @graph array; anything that
// does not decode is ignored rather than failing the page.
func (md *pageMetadata) mergeJSONLD(raw string) {
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return
	}
	for _, obj := range jsonLDObjects(doc) {
		md.mergeObject(obj)
	}
}

// mergeObject reads the schema.org fields the extractor uses.
func (md *pageMetadata) mergeObject(obj map[string]any) {
	if t := jsonLDType(obj["@type"]); t != "" {
		if !containsString(md.types, t) {
			md.types = append(md.types, t)
		}
	}
	md.headline = firstNonEmpty(md.headline, jsonLDString(obj["headline"]), jsonLDString(obj["name"]))
	md.author = firstNonEmpty(md.author, jsonLDAuthor(obj["author"]))
	md.published = firstTime(md.published, parseStamp(jsonLDString(obj["datePublished"])))
	md.updated = firstTime(md.updated, parseStamp(jsonLDString(obj["dateModified"])))
	for _, v := range []string{"keywords", "articleSection"} {
		md.tags = append(md.tags, splitKeywords(jsonLDString(obj[v]))...)
	}
}

// jsonLDObjects normalizes a decoded JSON-LD document into its objects.
func jsonLDObjects(v any) []map[string]any {
	switch t := v.(type) {
	case map[string]any:
		out := []map[string]any{t}
		if graph, ok := t["@graph"]; ok {
			out = append(out, jsonLDObjects(graph)...)
		}
		return out
	case []any:
		var out []map[string]any
		for _, item := range t {
			out = append(out, jsonLDObjects(item)...)
		}
		return out
	default:
		return nil
	}
}

// jsonLDString renders a scalar JSON-LD value.
func jsonLDString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// jsonLDType renders @type, which may be a string or an array.
func jsonLDType(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// jsonLDAuthor renders an author value: a string, {"name": ...}, or a list.
func jsonLDAuthor(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		return firstNonEmpty(jsonLDString(t["name"]), jsonLDString(t["@id"]))
	case []any:
		for _, item := range t {
			if name := jsonLDAuthor(item); name != "" {
				return name
			}
		}
	}
	return ""
}

// dates returns the page's publication and modification time, preferring a
// configured date selector, then the time element in the content region, then
// the metadata.
func (e *Extractor) dates(content *html.Node, md pageMetadata) (time.Time, time.Time) {
	published, updated := md.published, md.updated
	if sel := strings.TrimSpace(e.opts.Selectors.Date); sel != "" {
		compiled, err := compileSelector("options.selectors.date", sel)
		if err == nil {
			if node := queryFirst(content, compiled); node != nil {
				stamp := firstTime(parseStamp(attrVal(node, "datetime")), parseStamp(textOf(node)))
				if !stamp.IsZero() {
					published = stamp
					if updated.IsZero() {
						updated = stamp
					}
				}
			}
		}
	}
	if published.IsZero() {
		if node := findFirstTag(content, "time"); node != nil {
			published = parseStamp(attrVal(node, "datetime"))
		}
	}
	if updated.IsZero() {
		updated = published
	}
	return published, updated
}

// selectorText returns the text of the first element matching sel.
func (e *Extractor) selectorText(root *html.Node, sel string) string {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return ""
	}
	compiled, err := compileSelector("selector", sel)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(textOf(queryFirst(root, compiled)))
}

// selectorAll returns the text of every element matching sel.
func (e *Extractor) selectorAll(root *html.Node, sel string) []string {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return nil
	}
	compiled, err := compileSelector("selector", sel)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range queryAll(root, compiled) {
		if text := strings.TrimSpace(textOf(n)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// rawTextOf returns every text node under n, including script bodies. The
// JSON-LD reader needs it because textOf deliberately skips <script>.
func rawTextOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// sectionTags derives structural tags from the URL path: /case-study/x
// becomes "case-study". It is the honest fallback when a page publishes no
// tags and the site has no taxonomy.
func sectionTags(u *url.URL) []string {
	path := strings.Trim(strings.TrimSpace(u.Path), "/")
	if path == "" {
		return nil
	}
	first, _, _ := strings.Cut(path, "/")
	if first == "" || first == "index" {
		return nil
	}
	return []string{first}
}

// firstHeadingText returns the text of the first h1 (then h2) under n.
func firstHeadingText(n *html.Node) string {
	for _, tag := range []string{"h1", "h2"} {
		if node := findFirstTag(n, tag); node != nil {
			if text := strings.TrimSpace(textOf(node)); text != "" {
				return text
			}
		}
	}
	return ""
}

// attrVal returns an element's attribute value, or "".
func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// splitKeywords splits a comma-separated keyword list.
func splitKeywords(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// dedupeStrings removes duplicates and blanks, preserving order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

// containsString reports whether list holds want.
func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// truncateRunes cuts s to at most n runes without splitting a code point.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// firstNonEmpty returns the first non-blank string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
