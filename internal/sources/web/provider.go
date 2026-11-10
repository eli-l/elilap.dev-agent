package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"golang.org/x/net/html"
)

// Provider implements shared.Provider over one public website.
//
// List is discovery only: sitemap, feeds, seed URLs, and an optional bounded
// crawl. Fetch downloads one page and runs the configured extraction
// strategy. Nothing here mutates the site, and every request goes through
// Client's politeness delay and robots.txt gate.
type Provider struct {
	opts      *Options
	site      *url.URL
	client    *Client
	extractor *Extractor
	now       func() time.Time
}

// NewProvider builds the web provider from parsed options, the shared HTTP
// client, the configured User-Agent, and an adapter set (nil means
// DefaultAdapters).
func NewProvider(opts *Options, hc *http.Client, userAgent string, adapters []Adapter) (*Provider, error) {
	if opts == nil {
		return nil, fmt.Errorf("web: options are required")
	}
	site, err := parseSiteURL(opts.SiteURL)
	if err != nil {
		return nil, err
	}
	return &Provider{
		opts:      opts,
		site:      site,
		client:    newClient(hc, userAgent, opts.delay(), opts.robotsEnabled(), site.Hostname()),
		extractor: newExtractor(opts, adapters),
		now:       time.Now,
	}, nil
}

// NewFromSource is the registry constructor. A web source carries no
// credentials: the site is public, so its URL is configuration.
func NewFromSource(src config.Source, opts any, bc shared.BuildContext) (shared.Provider, error) {
	o, ok := opts.(*Options)
	if !ok {
		return nil, fmt.Errorf("web: options are %T, want *web.Options", opts)
	}
	return NewProvider(o, bc.Client(), bc.UserAgent, nil)
}

// Type reports the config type string.
func (p *Provider) Type() string { return "web" }

// List returns the site's pages as cheap candidates. An item's identity is
// host+path (pageID); its date is the newest timestamp discovery reported.
//
// An unbounded listing (since is zero, i.e. a first or --full run) must
// include every page, because the ingestor treats pages absent from it as
// deleted upstream. If max_pages would cut such a listing short, List fails
// instead of letting the run drop real chunks.
func (p *Provider) List(ctx context.Context, since time.Time) ([]shared.Item, error) {
	log := logging.Sources().With().Str("provider", p.Type()).Str("site", p.opts.SiteURL).Logger()

	disc, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	if disc.incomplete != nil {
		if since.IsZero() {
			// An unbounded listing is what drift deletion runs on: it must be
			// provably complete, or a failed feed/crawl would delete pages.
			return nil, fmt.Errorf(
				"web: incomplete page discovery, refusing an unbounded listing (the ingest would delete pages it did not see): %w",
				disc.incomplete)
		}
		log.Warn().Err(disc.incomplete).
			Msg("supplementary discovery failed; this bounded listing is partial (it cannot delete drift anyway)")
	}
	if len(disc.candidates) == 0 && since.IsZero() {
		return nil, errors.New(
			"web: no pages discovered; refusing an empty unbounded listing so the ingest cannot delete every indexed page")
	}

	items := make([]shared.Item, 0, len(disc.candidates))
	for _, c := range disc.candidates {
		if !since.IsZero() && !c.LastModified.IsZero() && c.LastModified.Before(since) {
			continue
		}
		items = append(items, shared.Item{
			// No ETag: the page body is the only fingerprint, so a page without
			// an upstream timestamp is re-fetched each run and compared on
			// content rather than assumed unchanged.
			ID:           pageID(c.URL),
			URL:          c.URL.String(),
			LastModified: c.LastModified,
		})
	}

	if len(items) > p.opts.MaxPages {
		if since.IsZero() {
			return nil, fmt.Errorf(
				"web: %d page(s) exceed options.max_pages=%d on an unbounded listing; raising max_pages is required so the run does not drop pages",
				len(items), p.opts.MaxPages)
		}
		// Keep the newest pages: the listing order is by URL, so truncating it
		// as-is could drop a page updated inside the window while keeping an
		// older one.
		sortForTruncation(items)
		log.Warn().
			Int("pages", len(items)).
			Int("max_pages", p.opts.MaxPages).
			Msg("truncating an incremental listing at max_pages (newest pages are kept, older ones stay outside this window)")
		items = items[:p.opts.MaxPages]
	}

	log.Info().
		Time("since", since).
		Int("discovered", len(disc.candidates)).
		Int("items", len(items)).
		Msg("listed site pages")
	return items, nil
}

// sortForTruncation orders items newest first so a max_pages cut keeps the
// pages most likely to have changed. Undated pages sort last; ties fall back to
// the URL, keeping the order deterministic.
func sortForTruncation(items []shared.Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].LastModified, items[j].LastModified
		switch {
		case a.IsZero() != b.IsZero():
			return b.IsZero()
		case !a.Equal(b):
			return a.After(b)
		default:
			return items[i].URL < items[j].URL
		}
	})
}

// Fetch downloads one page and extracts it. A page mislabeled as HTML (a PDF
// served with a wrong type) and a page with no content region are errors; a
// page asking not to be indexed yields an empty document, so the ledger marks
// it seen without writing chunks.
func (p *Provider) Fetch(ctx context.Context, it shared.Item) (shared.Document, error) {
	log := logging.Sources().With().
		Str("provider", p.Type()).
		Str("item_id", it.ID).
		Str("url", it.URL).
		Logger()

	target, err := p.resolve(it.URL)
	if err != nil {
		return shared.Document{}, fmt.Errorf("web: item %s: %w", it.ID, err)
	}
	resp, err := p.client.get(ctx, target)
	if err != nil {
		log.Error().Err(err).Msg("fetch page failed")
		return shared.Document{}, err
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); !isHTMLContentType(ct) {
		return shared.Document{}, fmt.Errorf("web: %s: content type %q is not HTML", it.URL, ct)
	}
	root, err := parseHTML(resp.Body)
	if err != nil {
		return shared.Document{}, fmt.Errorf("web: %s: %w", it.URL, err)
	}

	page, err := p.extractor.Extract(resp.URL, root)
	if errors.Is(err, ErrNoContent) {
		log.Warn().Err(err).Msg("page has no content to index, writing the ledger row only")
		return shared.Document{
			Meta: map[string]any{"url": it.URL, "skipped": "no content"},
		}, nil
	}
	if err != nil {
		return shared.Document{}, err
	}
	if page.NoIndex {
		log.Warn().Msg("page asks not to be indexed (robots noindex), writing the ledger row only")
		return shared.Document{
			Meta: map[string]any{"url": it.URL, "strategy": page.Strategy, "noindex": true},
		}, nil
	}

	published := firstTime(page.PublishedAt, it.LastModified)
	if published.IsZero() {
		// An undated page (no sitemap lastmod, no feed date, no visible time)
		// has no honest publication time; the fetch time keeps retrieval
		// recency meaningful instead of stamping year 1.
		published = p.now().UTC()
	}
	// The newest of the page's own dates and the discovery timestamp: a
	// sitemap lastmod later than the article's publish date is a real edit,
	// and updated_at must not hide it.
	updated := latestTime(page.UpdatedAt, it.LastModified, page.PublishedAt, published)

	doc := shared.Document{
		Title:       page.Title,
		Body:        page.Body,
		HTML:        page.HTML,
		PublishedAt: published,
		UpdatedAt:   updated,
		Author:      page.Author,
		Tags:        page.Tags,
		Meta: map[string]any{
			"url":      it.URL,
			"strategy": page.Strategy,
			"site":     p.site.Host,
		},
	}
	if page.Description != "" {
		doc.Meta["description"] = page.Description
	}
	if len(page.JSONLDTypes) > 0 {
		doc.Meta["jsonld_types"] = page.JSONLDTypes
	}
	if !it.LastModified.IsZero() {
		doc.Meta["lastmod"] = it.LastModified.UTC().Format(time.RFC3339)
	}

	log.Debug().
		Str("title", page.Title).
		Str("strategy", page.Strategy).
		Int("html_bytes", len(page.HTML)).
		Int("tags", len(page.Tags)).
		Msg("fetched page")
	return doc, nil
}

// isHTMLContentType reports whether a Content-Type header carries HTML.
// Discovery responses are XML and never reach this check.
func isHTMLContentType(ct string) bool {
	if ct == "" {
		// Some servers omit the header; parsing will fail loudly if the body
		// is not HTML, which is a better signal than rejecting a valid page.
		return true
	}
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// parseHTML parses an HTML document.
func parseHTML(body []byte) (*html.Node, error) {
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	return root, nil
}
