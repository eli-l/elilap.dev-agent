// Package ingest walks the configured providers, diffs what they list
// against the ledger in migrations/0002_state.sql, and writes changed items
// into the index.
//
// Incremental by default (PLAN.md §2.6): List(since = last_run_at - overlap)
// yields cheap candidates, the ledger decides which ones changed, and only
// those are fetched and chunked. Deletion is detected only when the list is
// unbounded (a full run without --since), because a bounded listing window
// cannot distinguish a deleted item from an older one outside the window.
// Full ignores the ledger fingerprints and re-fetches every listed item.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// DefaultOverlap is how far before the last run an incremental list starts.
// It absorbs clock skew and items that became visible upstream slightly after
// their updated_at, at the cost of re-checking a day of candidates the ledger
// then skips.
const DefaultOverlap = 24 * time.Hour

// Options controls one ingest invocation.
type Options struct {
	// Since is the explicit lower bound for listing. A non-zero value marks
	// the run as windowed even when SinceSet is false.
	Since time.Time
	// SinceSet distinguishes an explicit lower bound (windowed run, no drift
	// deletion) from the implicit zero of an incremental first run.
	SinceSet bool
	// Full re-fetches every listed item instead of trusting the ledger.
	Full bool
	// DryRun lists and diffs without writing chunks or ledger rows.
	DryRun bool
	// Overlap widens the incremental watermark; zero means DefaultOverlap.
	Overlap time.Duration
	// Chunking sizes the chunks (sources.yaml defaults.chunking).
	Chunking shared.ChunkerConfig
	// HTTPClient is shared by every provider; nil means http.DefaultClient.
	HTTPClient *http.Client
	// Now is overridable in tests; nil means time.Now.
	Now func() time.Time
}

// Result summarizes one source's run.
type Result struct {
	SourceID  string
	Type      string
	Kind      string
	Since     time.Time
	Listed    int
	Fetched   int
	Unchanged int // listed candidates the ledger already knew
	Added     int // chunks written (new or changed content)
	Updated   int // chunks whose content survived unchanged
	Removed   int // items deleted upstream and dropped from the index
	Planned   []string
}

// Runner holds the dependencies of an ingest run.
type Runner struct {
	Store    *store.Store
	Registry *sources.Registry
}

// Select returns the sources to run: one by id when id is non-empty, else
// every enabled source. A disabled or unknown id is an error, so a typo
// never silently ingests nothing.
func Select(cfg *config.Config, id string) ([]config.Source, error) {
	if id == "" {
		enabled := cfg.Enabled()
		if len(enabled) == 0 {
			return nil, errors.New("no enabled sources in the configuration")
		}
		return enabled, nil
	}
	for _, src := range cfg.Sources {
		if src.ID != id {
			continue
		}
		if !src.Enabled {
			return nil, fmt.Errorf("source %q is disabled", id)
		}
		return []config.Source{src}, nil
	}
	return nil, fmt.Errorf("unknown source %q", id)
}

// Run ingests every selected source. Per-source failures are collected so a
// broken provider does not hide the result of the others; a source whose
// writes fail part-way keeps the items already committed (each item is its
// own transaction).
func (r *Runner) Run(ctx context.Context, srcs []config.Source, opts Options) ([]Result, error) {
	if opts.Overlap < 0 {
		return nil, errors.New("overlap must not be negative")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Overlap == 0 {
		opts.Overlap = DefaultOverlap
	}

	results := make([]Result, 0, len(srcs))
	var errs []error
	for _, src := range srcs {
		res, err := r.runSource(ctx, src, opts)
		results = append(results, res)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %q: %w", src.ID, err))
		}
	}
	return results, errors.Join(errs...)
}

// runSource handles one provider: resolve credentials, list, diff, fetch,
// chunk, write, and record the run in the ledger.
func (r *Runner) runSource(ctx context.Context, src config.Source, opts Options) (Result, error) {
	log := logging.Sources().With().Str("source_id", src.ID).Str("type", src.Type).Logger()
	res := Result{SourceID: src.ID, Type: src.Type}

	kind, err := kindFor(src.Type)
	if err != nil {
		return res, err
	}
	res.Kind = kind

	parsed, err := r.Registry.ParseOptions(src)
	if err != nil {
		return res, err
	}
	provider, err := r.Registry.Build(src, parsed, opts.HTTPClient)
	if err != nil {
		return res, err
	}

	since := opts.Since
	if since.IsZero() && !opts.Full {
		last, err := r.Store.LastRunAt(src.ID)
		switch {
		case err == nil:
			since = last.Add(-opts.Overlap)
		case errors.Is(err, store.ErrNoSourceState):
			// First run: list everything.
		default:
			return res, err
		}
	}
	res.Since = since

	log.Info().Time("since", since).Bool("full", opts.Full).Bool("dry_run", opts.DryRun).Msg("listing source")
	items, err := provider.List(ctx, since)
	if err != nil {
		return res, err
	}
	res.Listed = len(items)

	ledgerItems := make([]store.LedgerItem, 0, len(items))
	byID := make(map[string]shared.Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
		li := store.LedgerItem{ItemID: it.ID, URL: it.URL, ETag: it.ETag}
		if !it.LastModified.IsZero() {
			li.LastModified = it.LastModified.UTC().Format(time.RFC3339)
		}
		// Fold a provider's cheap fingerprint into the ledger when it exposes
		// no etag or timestamp, so changes reported only via Hint are not
		// classified as unchanged forever.
		if li.ETag == "" && li.LastModified == "" && it.Hint != "" {
			li.ETag = "hint:" + it.Hint
		}
		ledgerItems = append(ledgerItems, li)
	}

	diff, err := r.Store.DiffLedger(src.ID, ledgerItems)
	if err != nil {
		return res, err
	}
	toFetch := diff.ToFetch
	if opts.Full {
		toFetch = ledgerItems
	}
	toRemove := diff.ToRemove
	if opts.SinceSet || !since.IsZero() {
		// A bounded list (both --since and the incremental watermark) cannot
		// prove an item was deleted upstream: older items are simply outside
		// the listing window. Only an unbounded list may delete drift.
		toRemove = nil
	}
	// A full run fetches every listed item, so nothing it lists is unchanged;
	// keeping the ledger-diff count would double-count against Fetched.
	res.Unchanged = diff.Seen - len(diff.ToFetch)
	if opts.Full {
		res.Unchanged = 0
	}

	now := opts.Now().UTC()
	runID := shared.NewRunID(src.ID, now)
	for _, it := range toFetch {
		res.Planned = append(res.Planned, it.ItemID)
	}
	for _, it := range toRemove {
		res.Planned = append(res.Planned, "-"+it.ItemID)
	}

	if opts.DryRun {
		log.Info().
			Int("listed", res.Listed).
			Int("to_fetch", len(toFetch)).
			Int("to_remove", len(toRemove)).
			Msg("dry run: no writes")
		return res, nil
	}

	for _, it := range toFetch {
		listed, ok := byID[it.ItemID]
		if !ok {
			// Full runs list every item, so a missing entry means the
			// provider returned a ledger row we never listed; fetch by id.
			listed = shared.Item{ID: it.ItemID}
		}
		listed.URL = firstNonEmpty(it.URL, listed.URL)
		if listed.LastModified.IsZero() {
			listed.LastModified = parseStamp(it.LastModified)
		}
		doc, err := provider.Fetch(ctx, listed)
		if err != nil {
			return res, err
		}
		chunks, err := BuildChunks(doc, kind, it.ItemID, firstNonEmpty(it.URL, docURL(doc)), opts.Chunking)
		if err != nil {
			return res, err
		}
		added, updated, err := r.Store.ReplaceItemChunks(store.ItemKey{Kind: kind, ID: it.ItemID}, chunks)
		if err != nil {
			return res, err
		}
		res.Fetched++
		res.Added += added
		res.Updated += updated

		li := it
		li.ContentHash = docHash(doc)
		if err := r.Store.UpsertItem(src.ID, li, runID, now); err != nil {
			return res, err
		}
	}

	for _, it := range toRemove {
		if err := r.Store.RemoveItem(src.ID, it); err != nil {
			return res, err
		}
		res.Removed++
	}

	// item_count describes the corpus, not this run's bounded listing window,
	// so record the ledger size after the writes instead of len(items).
	itemCount, err := r.Store.CountItems(src.ID)
	if err != nil {
		return res, err
	}
	if err := r.Store.SetSourceState(src.ID, src.Type, configHash(src, parsed), opts.Full, itemCount, now); err != nil {
		return res, err
	}
	log.Info().
		Int("listed", res.Listed).
		Int("fetched", res.Fetched).
		Int("added_chunks", res.Added).
		Int("updated_chunks", res.Updated).
		Int("removed_items", res.Removed).
		Msg("source ingest complete")
	return res, nil
}

// postizChunkTokens is the per-chunk budget for cross-posts (PLAN.md §3):
// 320 tokens with no overlap, because one social post is one thought and
// overlapping would duplicate it across chunks.
const postizChunkTokens = 320

// BuildChunks turns one fetched document into hash-addressed store chunks.
// HTML is preferred over plaintext so the chunker can split on real headings;
// plaintext is the fallback when the upstream sent no HTML.
func BuildChunks(doc shared.Document, kind, itemID, sourceURL string, cfg shared.ChunkerConfig) ([]store.Chunk, error) {
	cfg = chunkSizing(kind, cfg)
	text := doc.Body
	if doc.HTML != "" {
		md, err := shared.HTMLToMarkdown(doc.HTML)
		if err != nil {
			log := logging.Sources()
			log.Warn().Err(err).Str("item_id", itemID).Msg("html conversion failed, using plaintext")
		} else if md != "" {
			text = md
		}
	}
	if text == "" {
		text = doc.Title
	}
	parts := shared.ChunkMarkdown(text, cfg)
	if len(parts) == 0 {
		return nil, nil
	}

	updated := doc.UpdatedAt
	if updated.IsZero() {
		updated = doc.PublishedAt
	}
	out := make([]store.Chunk, 0, len(parts))
	for i, p := range parts {
		meta := make(map[string]any, len(doc.Meta)+2)
		for k, v := range doc.Meta {
			meta[k] = v
		}
		meta["tags"] = stringsToAny(doc.Tags)
		meta["provider"] = kind
		out = append(out, store.Chunk{
			ChunkID:     shared.ChunkID(kind, itemID, i),
			SourceKind:  kind,
			SourceID:    itemID,
			SourceURL:   sourceURL,
			Platform:    doc.Platform,
			Author:      doc.Author,
			PublishedAt: doc.PublishedAt,
			UpdatedAt:   updated,
			// The section is part of the identity: renaming a heading without
			// changing the body must still rewrite the chunk and its FTS row.
			ContentHash: shared.ContentHash(p.Section + "\n" + p.Text),
			Title:       doc.Title,
			Section:     p.Section,
			Body:        p.Text,
			BodyTokens:  p.Tokens,
			Meta:        meta,
		})
	}
	return out, nil
}

// kindFor maps a provider type to the chunks.source_kind value the schema
// allows. Keeping the mapping here (rather than in the providers) keeps the
// CHECK constraint and the provider contract in one place.
func kindFor(typeName string) (string, error) {
	switch typeName {
	case "ghost":
		return "ghost_post", nil
	case "postiz":
		return "postiz_post", nil
	default:
		return "", fmt.Errorf("provider type %q has no source_kind", typeName)
	}
}

// chunkSizing applies a source kind's chunking policy (PLAN.md §3) to the
// configured defaults: cross-posts cap at 320 tokens with no overlap, and a
// smaller configured target is honored. Every other kind uses the defaults
// as configured.
func chunkSizing(kind string, cfg shared.ChunkerConfig) shared.ChunkerConfig {
	if kind != "postiz_post" {
		return cfg
	}
	if cfg.TargetTokens > postizChunkTokens {
		cfg.TargetTokens = postizChunkTokens
	}
	cfg.OverlapTokens = 0
	return cfg
}

// configHash fingerprints the provider options (never credentials) so a
// reconfiguration is visible in source_state.config_hash.
func configHash(src config.Source, opts any) string {
	raw, _ := yaml.Marshal(opts)
	h := sha256.Sum256([]byte(src.Type + "\x00" + string(raw)))
	return hex.EncodeToString(h[:16])
}

// docHash hashes the normalized document text for the ledger's advisory
// content_hash column.
func docHash(doc shared.Document) string {
	return shared.ContentHash(doc.Title + "\n" + strings.Join([]string{doc.Body, doc.HTML}, "\n"))
}

// stringsToAny converts tags for JSON metadata, matching what the FTS writer
// reads back from meta_json.
func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// parseStamp parses an ISO-8601 UTC ledger stamp, returning the zero time
// for an empty or unparsable value.
func parseStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// docURL returns the document's canonical URL from provider metadata, when
// the provider recorded one.
func docURL(doc shared.Document) string {
	if doc.Meta == nil {
		return ""
	}
	if u, ok := doc.Meta["url"].(string); ok {
		return u
	}
	return ""
}
