package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// Headings and candidates are reported down to this depth: deeper structure
// is noise for choosing a content region.
const (
	inspectMinChars = 80
	inspectMaxCands = 10
)

// Analysis is the report `agent sources inspect` prints and `--format json`
// encodes. It is the input to the strategy-authoring loop: fetch one page,
// report every plausible content region with a selector that addresses it,
// and let a human or an LLM pick one - or conclude that selectors are not
// enough and an Adapter is needed.
type Analysis struct {
	// URL is the inspected page.
	URL string `json:"url"`
	// Status is the HTTP status code.
	Status int `json:"status"`
	// ContentType is the response Content-Type header.
	ContentType string `json:"content_type"`
	// Bytes is the response body size.
	Bytes int `json:"bytes"`
	// Title is the document <title>.
	Title string `json:"title"`
	// Meta holds the page's meta tags (name/property -> content).
	Meta map[string]string `json:"meta,omitempty"`
	// JSONLD holds the decoded schema.org blocks, when the page publishes
	// any. Most hand-written sites (elilap.dev included) publish none.
	JSONLD []any `json:"jsonld,omitempty"`
	// JSONLDTypes lists the schema.org types found.
	JSONLDTypes []string `json:"jsonld_types,omitempty"`
	// Headings lists h1-h3 in document order.
	Headings []Heading `json:"headings,omitempty"`
	// Candidates are the plausible content regions, best first.
	Candidates []Candidate `json:"candidates,omitempty"`
	// Adapters lists the registered adapters that claim this URL.
	Adapters []string `json:"adapters,omitempty"`
	// Discovery is what the source's discovery sources return.
	Discovery DiscoverySummary `json:"discovery"`
	// Suggested is the options block the report recommends. It is a
	// starting point, not a verdict: a page family can need a selector the
	// heuristic never proposes.
	Suggested Suggestion `json:"suggested"`
	// Preview is what the configured strategy would actually write for this
	// page: the real title, body size, tags, and dates the chunker would get.
	// It is the check that the suggestion and the config agree.
	Preview *Preview `json:"preview,omitempty"`
}

// Preview is the extraction result for the inspected page under the source's
// current strategy.
type Preview struct {
	Title       string    `json:"title"`
	Strategy    string    `json:"strategy"`
	Chars       int       `json:"chars"`
	Tags        []string  `json:"tags,omitempty"`
	Author      string    `json:"author,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	Skipped     string    `json:"skipped,omitempty"`
}

// Heading is one h1-h3 of the page.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// Candidate is one plausible content region: a selector that addresses it,
// how much text it holds, and how much of that text is links (a content
// region has a low link density; a navigation menu has a high one).
type Candidate struct {
	Selector    string  `json:"selector"`
	Tag         string  `json:"tag"`
	Chars       int     `json:"chars"`
	LinkDensity float64 `json:"link_density"`
	Headings    int     `json:"headings"`
	Depth       int     `json:"depth"`
	text        string
	// Score ranks candidates: text volume discounted by link density, with a
	// bonus for the semantic content containers (<article> wins over a larger
	// layout ancestor that repeats the same text plus chrome).
	Score float64 `json:"score"`
}

// Suggestion is the recommended extraction configuration.
type Suggestion struct {
	// Strategy is "auto" when the generic chain already finds the winner,
	// "selectors" when an explicit content selector is needed, or the name
	// of a matching adapter.
	Strategy string `json:"strategy"`
	// Content is the content selector for strategy "selectors".
	Content string `json:"content,omitempty"`
	// Reason explains the choice in one line.
	Reason string `json:"reason"`
}

// Inspect fetches one page of the source and reports how it would be
// extracted. An empty rawURL inspects the site root.
func (p *Provider) Inspect(ctx context.Context, rawURL string) (*Analysis, error) {
	target := p.site
	if strings.TrimSpace(rawURL) != "" {
		u, err := p.resolve(rawURL)
		if err != nil {
			return nil, err
		}
		target = u
	}
	if !p.opts.MatchesHost(target) {
		return nil, fmt.Errorf("web: %s is outside %s", target.Redacted(), p.site.Host)
	}

	resp, err := p.client.get(ctx, target)
	if err != nil {
		return nil, err
	}
	root, err := parseHTML(resp.Body)
	if err != nil {
		return nil, err
	}

	analysis := &Analysis{
		URL:         resp.URL.Redacted(),
		Status:      resp.Status,
		ContentType: resp.Header.Get("Content-Type"),
		Bytes:       len(resp.Body),
		Meta:        map[string]string{},
	}
	md := readMetadata(root)
	analysis.Title = firstNonEmpty(md.title, md.headline)
	analysis.JSONLDTypes = md.types
	for _, n := range findElements(root, "meta", nil) {
		key := firstNonEmpty(attrVal(n, "property"), attrVal(n, "name"))
		if key == "" {
			continue
		}
		analysis.Meta[key] = attrVal(n, "content")
	}
	if len(analysis.Meta) == 0 {
		analysis.Meta = nil
	}
	for _, script := range findElements(root, "script", nil) {
		if !strings.Contains(strings.ToLower(attrVal(script, "type")), "ld+json") {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(strings.TrimSpace(rawTextOf(script))), &doc); err == nil {
			analysis.JSONLD = append(analysis.JSONLD, doc)
		}
	}
	for _, tag := range []string{"h1", "h2", "h3"} {
		for _, n := range findElements(root, tag, nil) {
			if text := strings.TrimSpace(textOf(n)); text != "" {
				analysis.Headings = append(analysis.Headings, Heading{Level: int(tag[1] - '0'), Text: text})
			}
		}
	}
	for _, adapter := range p.extractor.adapters {
		if adapter.Match(resp.URL) {
			analysis.Adapters = append(analysis.Adapters, adapter.Name())
		}
	}
	// The discovery pass also supplies the page's sitemap/feed timestamp, so
	// the preview can report the same dates ingest would write.
	var discovered time.Time
	if disc, err := p.discoverWith(ctx, false); err == nil {
		analysis.Discovery = disc.summary
		want := normalizeURL(resp.URL).String()
		for _, c := range disc.candidates {
			if c.URL.String() == want {
				discovered = c.LastModified
				break
			}
		}
	} else {
		analysis.Discovery.Incomplete = err.Error()
	}
	analysis.Candidates = contentCandidates(root)
	analysis.Suggested = suggest(analysis)
	analysis.Preview = p.preview(resp.URL, root, discovered)
	return analysis, nil
}

// preview runs the configured strategy over the parsed page. Candidate
// analysis has already run, so the extractor's chrome removal cannot distort
// it; the page is otherwise unmodified by this call.
func (p *Provider) preview(u *url.URL, root *html.Node, discovered time.Time) *Preview {
	page, err := p.extractor.Extract(u, root)
	if errors.Is(err, ErrNoContent) {
		return &Preview{Strategy: p.opts.Strategy, Skipped: "no content"}
	}
	if err != nil {
		return &Preview{Strategy: p.opts.Strategy, Skipped: err.Error()}
	}
	if page.NoIndex {
		// Fetch writes the ledger row and no chunks for a noindex page, so the
		// preview must not claim ingest would index it.
		return &Preview{Strategy: page.Strategy, Skipped: "noindex"}
	}
	// Mirror Fetch's date fallbacks: the page's own dates, then the discovery
	// timestamp. Fetch additionally stamps the fetch time when both are
	// absent; the preview reports "(none)" rather than inventing a date.
	return &Preview{
		Title:       page.Title,
		Strategy:    page.Strategy,
		Chars:       len(page.Body),
		Tags:        page.Tags,
		Author:      page.Author,
		PublishedAt: firstTime(page.PublishedAt, discovered),
		UpdatedAt:   latestTime(page.UpdatedAt, discovered, page.PublishedAt),
	}
}

// contentCandidates ranks the elements that could be the page body. A node
// qualifies when it holds enough text; ancestors of a winner repeat the same
// text, so candidates are deduplicated by their text prefix and the deepest
// node is kept.
func contentCandidates(root *html.Node) []Candidate {
	var all []Candidate
	var walk func(*html.Node, int)
	walk = func(n *html.Node, depth int) {
		if n.Type == html.ElementNode && !isSilentTag(n.Data) {
			text := textOf(n)
			if len(text) >= inspectMinChars {
				sel := selectorFor(root, n)
				if sel != "" {
					chars := len(text)
					linkChars := linkTextChars(n)
					density := 0.0
					if chars > 0 {
						density = float64(linkChars) / float64(chars)
					}
					all = append(all, Candidate{
						text:        text,
						Selector:    sel,
						Tag:         n.Data,
						Chars:       chars,
						LinkDensity: round3(density),
						Headings:    len(findElements(n, "h1", nil)) + len(findElements(n, "h2", nil)) + len(findElements(n, "h3", nil)),
						Depth:       depth,
						Score:       round3(float64(chars) * (1 - density) * regionWeight(n.Data)),
					})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	walk(root, 0)

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].Depth > all[j].Depth
	})

	// Ancestors repeat a descendant's text (and both qualify), so candidates
	// are grouped by their text signature: the deepest member of a group is
	// the tightest element that still holds all of it.
	type group struct{ candidate Candidate }
	bySignature := make(map[string]group, len(all))
	var order []string
	for _, c := range all {
		if c.Tag == "html" || c.Tag == "body" {
			continue
		}
		sig := textSignature(c.text)
		g, ok := bySignature[sig]
		if !ok {
			bySignature[sig] = group{candidate: c}
			order = append(order, sig)
			continue
		}
		if c.Depth > g.candidate.Depth {
			g.candidate = c
			bySignature[sig] = g
		}
	}

	out := make([]Candidate, 0, len(order))
	for _, sig := range order {
		out = append(out, bySignature[sig].candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Depth > out[j].Depth
	})
	if len(out) > inspectMaxCands {
		out = out[:inspectMaxCands]
	}
	return out
}

// regionWeight biases the ranking towards the semantic content container.
// Without it the largest ancestor always wins, which is how a <main> wrapping
// an <article> would beat the article itself.
func regionWeight(tag string) float64 {
	switch tag {
	case "article":
		return 1.2
	case "main":
		return 1.05
	}
	return 1.0
}

// textSignature identifies an element's text for candidate de-duplication:
// the head and tail of the normalized text, which an ancestor and its content
// descendant share.
func textSignature(text string) string {
	const window = 80
	r := []rune(text)
	if len(r) <= 2*window {
		return string(r)
	}
	return string(r[:window]) + "|" + string(r[len(r)-window:])
}

// suggest turns the ranking into a starting configuration.
func suggest(a *Analysis) Suggestion {
	if len(a.Adapters) > 0 {
		return Suggestion{
			Strategy: a.Adapters[0],
			Reason:   "a registered adapter claims this host",
		}
	}
	if len(a.Candidates) == 0 {
		return Suggestion{Strategy: StrategyBody, Reason: "no content region stood out; index the whole body"}
	}
	best := a.Candidates[0]
	switch {
	case best.LinkDensity > 0.5:
		return Suggestion{
			Strategy: StrategyBody,
			Reason:   "every candidate is link-heavy; the page may be a listing rather than an article",
		}
	case best.Tag == "article" || best.Tag == "main":
		return Suggestion{
			Strategy: StrategyAuto,
			Content:  best.Selector,
			Reason:   "the generic article/main chain already finds <" + best.Tag + ">",
		}
	default:
		return Suggestion{
			Strategy: StrategySelectors,
			Content:  best.Selector,
			Reason:   "the richest region is <" + best.Tag + ">, which the generic chain would miss",
		}
	}
}

// selectorFor builds a stable CSS selector for one node: a tag path from
// <body>, using ids where present and :nth-of-type to disambiguate siblings.
// Classes are deliberately not used - hand-written sites (Tailwind, Astro)
// carry utility classes that are neither stable nor meaningful.
func selectorFor(root, n *html.Node) string {
	var parts []string
	for cur := n; cur != nil && cur.Type == html.ElementNode; cur = cur.Parent {
		if cur.Data == "body" || cur.Data == "html" {
			break
		}
		part := cur.Data
		if id := attrVal(cur, "id"); id != "" && !strings.ContainsAny(id, " .#:>[]") {
			parts = append(parts, part+"#"+id)
			break
		}
		if countSiblings(cur) > 1 {
			part += fmt.Sprintf(":nth-of-type(%d)", elementIndex(cur))
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	sel := strings.Join(parts, " > ")
	compiled, err := cascadia.Compile(sel)
	if err != nil {
		return ""
	}
	// A selector must address exactly this node; otherwise it is not usable
	// in sources.yaml and reporting it would mislead the author.
	if query := cascadia.QueryAll(root, compiled); len(query) != 1 {
		return ""
	}
	return sel
}

// countSiblings returns how many element siblings n has (including itself).
func countSiblings(n *html.Node) int {
	count := 0
	for c := n.Parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == n.Data {
			count++
		}
	}
	return count
}

// elementIndex returns n's 1-based position among same-tag siblings.
func elementIndex(n *html.Node) int {
	index := 0
	for c := n.Parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == n.Data {
			index++
			if c == n {
				return index
			}
		}
	}
	return index
}

// linkTextChars approximates the link text inside n, the denominator of the
// link-density heuristic.
func linkTextChars(n *html.Node) int {
	total := 0
	for _, a := range findElements(n, "a", nil) {
		total += len(textOf(a))
	}
	return total
}

// round3 rounds a float to three decimals, so JSON output stays readable.
func round3(f float64) float64 {
	return float64(int64(f*1000+0.5)) / 1000
}

// Text renders the analysis for a terminal: enough to choose a strategy
// without re-reading the page, and no more.
func (a *Analysis) Text(site string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "site: %s\n", site)
	fmt.Fprintf(&b, "url: %s\n", a.URL)
	fmt.Fprintf(&b, "status: %d, type: %s, %d bytes\n", a.Status, emptyAs(a.ContentType, "unknown"), a.Bytes)
	fmt.Fprintf(&b, "title: %s\n", emptyAs(a.Title, "(none)"))
	if len(a.Adapters) > 0 {
		fmt.Fprintf(&b, "adapters matching: %s\n", strings.Join(a.Adapters, ", "))
	} else {
		b.WriteString("adapters matching: none\n")
	}
	if len(a.JSONLDTypes) > 0 {
		fmt.Fprintf(&b, "json-ld: %s\n", strings.Join(a.JSONLDTypes, ", "))
	} else {
		b.WriteString("json-ld: none\n")
	}
	if len(a.Headings) > 0 {
		b.WriteString("headings:\n")
		for _, h := range a.Headings {
			fmt.Fprintf(&b, "  h%d %s\n", h.Level, truncateRunes(h.Text, 90))
		}
	}
	b.WriteString("\ncontent candidates (text volume discounted by link density):\n")
	if len(a.Candidates) == 0 {
		b.WriteString("  none found\n")
	}
	for i, c := range a.Candidates {
		marker := "  "
		if i == 0 {
			marker = "->"
		}
		fmt.Fprintf(&b, "%s %-48s %6d chars  links %4.0f%%  headings %d\n",
			marker, c.Selector, c.Chars, c.LinkDensity*100, c.Headings)
	}
	fmt.Fprintf(&b, "\ndiscovery: sitemap %d url(s), feeds %d, seeds %d, crawl %d, total %d\n",
		a.Discovery.SitemapURLs, a.Discovery.FeedURLs, a.Discovery.SeedURLs,
		a.Discovery.CrawlURLs, a.Discovery.TotalURLs)
	if a.Discovery.Incomplete != "" {
		fmt.Fprintf(&b, "  incomplete: %s\n", a.Discovery.Incomplete)
	}
	if p := a.Preview; p != nil {
		b.WriteString("\nwhat ingest would write with the configured strategy:\n")
		if p.Skipped != "" {
			fmt.Fprintf(&b, "  skipped: %s\n", p.Skipped)
		} else {
			fmt.Fprintf(&b, "  title: %s\n", emptyAs(p.Title, "(none)"))
			published := "(none)"
			if !p.PublishedAt.IsZero() {
				published = p.PublishedAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(&b, "  %d chars of Markdown, tags %v, published %s\n",
				p.Chars, p.Tags, published)
		}
	}
	b.WriteString("\nsuggested options:\n")
	fmt.Fprintf(&b, "  strategy: %s\n", a.Suggested.Strategy)
	if a.Suggested.Content != "" {
		fmt.Fprintf(&b, "  selectors:\n    content: %q\n", a.Suggested.Content)
	}
	fmt.Fprintf(&b, "  # %s\n", a.Suggested.Reason)
	return b.String()
}

// emptyAs substitutes a placeholder for an empty value.
func emptyAs(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return placeholder
	}
	return s
}

// SiteURL exposes the provider's configured origin (used by the CLI report).
func (p *Provider) SiteURL() *url.URL { return p.site }
