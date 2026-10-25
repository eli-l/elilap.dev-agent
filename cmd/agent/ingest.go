package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eli-l/elilap.dev-agent/internal/chunk"
	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// runIngest imports the configured sources into the index.
//
// The run is ledger-driven: the index carries a ledger - source_state and
// source_items - that says what the last run of every source saw, and this
// run decides from that ledger what to fetch, what to rewrite and what to
// delete. A source whose client does not exist yet is reported and skipped
// - it does not stop the sources that do work - and the command still fails
// when no source could be imported at all.
func runIngest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := defaultConfigPath
	fs.StringVar(&configPath, "config", defaultConfigPath, "path to sources.yaml")
	dbFlag := fs.String("db", "", "path to the SQLite index (default: AGENT_DB_PATH or ./index.db)")
	sourceFlag := fs.String("source", "", "import only this configured source")
	full := fs.Bool("full", false, "re-fetch, re-chunk and rewrite every item, even the unchanged ones")
	dryRun := fs.Bool("dry-run", false, "report what would be written, skipped and deleted, and change nothing")
	limit := fs.Int("limit", 0, "cap the number of items per source (0 = no limit)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent ingest: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *limit < 0 {
		fmt.Fprintln(stderr, "agent ingest: --limit cannot be negative")
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}

	selected, err := selectSources(cfg, *sourceFlag)
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}

	reg, err := newRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}

	dbPath := indexPath(*dbFlag)
	s, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}
	defer s.Close()

	ctx := context.Background()
	opts := ingestOptions{full: *full, dryRun: *dryRun, limit: *limit}
	runID := newRunID()

	if *dryRun {
		fmt.Fprintf(stdout, "dry run: nothing is written to %s\n", s.Path())
	}

	imported, skipped := 0, 0
	for _, src := range selected {
		provider, err := reg.NewProvider(src)
		if err != nil {
			if errors.Is(err, sources.ErrNoClient) {
				fmt.Fprintf(stdout, "skip  %s: %v\n", src.ID, err)
				skipped++
				continue
			}
			fmt.Fprintf(stderr, "agent ingest: %v\n", err)
			return 1
		}

		summary, err := ingestSource(ctx, s, cfg, src, provider, opts, runID, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "agent ingest: %v\n", err)
			return 1
		}

		if opts.dryRun {
			fmt.Fprintf(stdout, "dry-run %s: %d item(s) seen, %d item(s) fetched, %d chunk(s) would be written, "+
				"%d stale chunk(s) would be removed, %d item(s) would be removed, %d item(s) unchanged (index %s)\n",
				src.ID, summary.Seen, summary.Fetched, summary.Written,
				summary.StaleRemoved, summary.ItemsRemoved, summary.Unchanged, s.Path())
		} else {
			fmt.Fprintf(stdout, "%s: %d item(s) seen, %d item(s) fetched, %d chunk(s) written, "+
				"%d stale chunk(s) removed, %d item(s) removed, %d item(s) unchanged (index %s)\n",
				src.ID, summary.Seen, summary.Fetched, summary.Written,
				summary.StaleRemoved, summary.ItemsRemoved, summary.Unchanged, s.Path())
		}
		imported++
	}

	if imported == 0 {
		fmt.Fprintf(stderr, "agent ingest: nothing was imported: %d source(s) skipped, no source could be read\n", skipped)
		return 1
	}

	fmt.Fprintf(stdout, "ingest: %d source(s) imported, %d skipped, index %s\n", imported, skipped, s.Path())
	return 0
}

// ingestOptions are the modes one ingest run can run in.
type ingestOptions struct {
	// full re-fetches and re-chunks every item, whatever the ledger says.
	full bool
	// dryRun reports what would happen and writes nothing at all.
	dryRun bool
	// limit caps the number of items per source.
	limit int
}

// sourceSummary counts what the run of one source did.
type sourceSummary struct {
	// Seen is how many items the provider listed.
	Seen int
	// Fetched is how many items were fetched in full.
	Fetched int
	// Written is how many chunks were written.
	Written int
	// StaleRemoved is how many chunks were deleted because the item no
	// longer produces them.
	StaleRemoved int
	// ItemsRemoved is how many items were deleted because the run did not
	// see them any more.
	ItemsRemoved int
	// Unchanged is how many items were left alone.
	Unchanged int
}

// ingestSource imports one source into the index, driven by the ledger.
//
// The ledger holds one row per item the source contributed. A run compares
// what the provider lists with what the ledger holds and, for every item,
// decides one of three things:
//
//   - the item did not change: the provider's cheap fingerprint still
//     matches the one the ledger holds, so the item is not even fetched;
//   - the item did not change, but the run had to look at it: the content
//     hash still matches, so the chunks in the index are the ones the item
//     produces and nothing is written;
//   - the item changed: its chunks are written, and the chunks it no
//     longer produces are deleted.
//
// The items the ledger holds but the run did not see are gone upstream:
// their chunks are deleted with their ledger rows.
func ingestSource(
	ctx context.Context,
	s *store.Store,
	cfg *config.Config,
	src config.Source,
	provider sources.Provider,
	opts ingestOptions,
	runID string,
	stdout io.Writer,
) (sourceSummary, error) {
	var summary sourceSummary
	kind := provider.SourceKind()

	configHash, err := sourceConfigHash(src, cfg.Defaults.Chunking)
	if err != nil {
		return summary, err
	}

	previous, hadState, err := s.SourceState(ctx, src.ID)
	if err != nil {
		return summary, err
	}
	rows, err := s.SourceItems(ctx, src.ID)
	if err != nil {
		return summary, err
	}
	ledger := make(map[string]store.SourceItem, len(rows))
	for _, row := range rows {
		ledger[row.ItemID] = row
	}

	// The chunking policy decides where the content of an item is cut, so
	// a configuration that changed since the last run invalidates the
	// chunks the last run wrote: every item is fetched and re-chunked.
	configChanged := hadState && previous.ConfigHash != "" && previous.ConfigHash != configHash
	if configChanged {
		fmt.Fprintf(stdout, "%s: the configuration changed since the last run: every item is fetched and re-chunked\n", src.ID)
	}

	// The whole list of the provider is asked for, not the items modified
	// since the last run: the ledger, and not a time filter, decides what
	// is new. A filtered list would make the items this run did not reach
	// look like items that are gone.
	items, err := provider.List(ctx, time.Time{})
	if err != nil {
		return summary, fmt.Errorf("source %s: %w", src.ID, err)
	}
	if opts.limit > 0 && len(items) > opts.limit {
		items = items[:opts.limit]
	}
	summary.Seen = len(items)

	// The chunk ids the index holds for every item of this source, so the
	// run can tell whether the chunks of an item are still the ones the
	// item produces.
	indexed, err := indexChunkIDs(ctx, s, kind)
	if err != nil {
		return summary, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	seen := make(map[string]bool, len(items))

	for _, item := range items {
		seen[item.ID] = true
		row, known := ledger[item.ID]
		fingerprint := itemFingerprint(item)

		// The provider's cheap fingerprint still matches the one the
		// ledger holds, and the index still holds the chunks of the item:
		// the item did not change, so it is not even fetched.
		if !opts.full && !configChanged && known && fingerprint != "" &&
			row.ETag == fingerprint && itemChunksContiguous(indexed[item.ID], kind, item.ID) {
			summary.Unchanged++
			if opts.dryRun {
				fmt.Fprintf(stdout, "dry-run %s: unchanged %s\n", src.ID, item.ID)
			}
			if !opts.dryRun {
				row.LastSeenAt = now
				row.SeenRunID = runID
				if err := s.PutSourceItem(ctx, row); err != nil {
					return summary, fmt.Errorf("source %s: %w", src.ID, err)
				}
			}
			continue
		}

		doc, err := provider.Fetch(ctx, item)
		if err != nil {
			return summary, fmt.Errorf("source %s: %w", src.ID, err)
		}
		summary.Fetched++

		texts := chunk.Split(doc.Body, cfg.Defaults.Chunking.TargetTokens, cfg.Defaults.Chunking.OverlapTokens)
		keep := chunkIDs(kind, item.ID, len(texts))
		bodyHash := contentHash(doc.Body)

		// The content of the item did not change and the index holds
		// exactly the chunks the item produces: there is nothing to
		// write.
		if !opts.full && !configChanged && known && bodyHash != "" && row.ContentHash == bodyHash &&
			itemChunksMatch(indexed[item.ID], keep) {
			summary.Unchanged++
			if opts.dryRun {
				fmt.Fprintf(stdout, "dry-run %s: unchanged %s\n", src.ID, item.ID)
			}
			if !opts.dryRun {
				row.URL = item.URL
				row.ContentHash = bodyHash
				row.ETag = fingerprint
				row.LastModified = itemModified(item)
				row.LastSeenAt = now
				row.SeenRunID = runID
				if err := s.PutSourceItem(ctx, row); err != nil {
					return summary, fmt.Errorf("source %s: %w", src.ID, err)
				}
			}
			continue
		}

		// The item changed: its chunks are written, and the chunks it
		// no longer produces are deleted. That is the case of an item
		// that was edited so that it got shorter: the chunks it used to
		// produce would otherwise stay in the index forever, matching
		// queries with a text the item does not hold any more.
		for seq, text := range texts {
			c, err := buildChunk(kind, item, doc, seq, text)
			if err != nil {
				return summary, fmt.Errorf("source %s: %w", src.ID, err)
			}
			if !opts.dryRun {
				if err := s.PutChunk(ctx, c); err != nil {
					return summary, fmt.Errorf("source %s: %w", src.ID, err)
				}
			}
		}
		summary.Written += len(texts)
		if opts.dryRun {
			fmt.Fprintf(stdout, "dry-run %s: would write %d chunk(s) for %s\n", src.ID, len(texts), item.ID)
		}

		stale := 0
		if opts.dryRun {
			stale = staleChunkCount(indexed[item.ID], keep)
		} else {
			stale, err = s.DeleteChunksForItem(ctx, kind, item.ID, keep)
			if err != nil {
				return summary, fmt.Errorf("source %s: %w", src.ID, err)
			}
		}
		if stale > 0 {
			summary.StaleRemoved += stale
			if opts.dryRun {
				fmt.Fprintf(stdout, "dry-run %s: would remove %d stale chunk(s) for %s\n", src.ID, stale, item.ID)
			} else {
				fmt.Fprintf(stdout, "%s: removed %d stale chunk(s) for %s\n", src.ID, stale, item.ID)
			}
		}

		updated := store.SourceItem{
			SourceID:     src.ID,
			ItemID:       item.ID,
			URL:          item.URL,
			ContentHash:  bodyHash,
			ETag:         fingerprint,
			LastModified: itemModified(item),
			FirstSeenAt:  now,
			LastSeenAt:   now,
			SeenRunID:    runID,
		}
		if known {
			// The first time the item was seen is not the first time it
			// was seen now.
			updated.FirstSeenAt = row.FirstSeenAt
		}
		if !opts.dryRun {
			if err := s.PutSourceItem(ctx, updated); err != nil {
				return summary, fmt.Errorf("source %s: %w", src.ID, err)
			}
		}
		ledger[item.ID] = updated
		indexed[item.ID] = keep
	}

	// The items the ledger holds but this run did not see are gone
	// upstream: their chunks go with them.
	if opts.limit > 0 {
		fmt.Fprintf(stdout, "%s: --limit is set: the items this run did not reach are left alone\n", src.ID)
	} else {
		for id := range ledger {
			if seen[id] {
				continue
			}
			removed := 0
			if opts.dryRun {
				removed = len(indexed[id])
			} else {
				removed, err = s.DeleteChunksForItem(ctx, kind, id, nil)
				if err != nil {
					return summary, fmt.Errorf("source %s: %w", src.ID, err)
				}
			}
			summary.ItemsRemoved++
			if opts.dryRun {
				fmt.Fprintf(stdout, "dry-run %s: would remove the item %s and %d chunk(s)\n", src.ID, id, removed)
			} else {
				fmt.Fprintf(stdout, "%s: removed the item %s and %d chunk(s)\n", src.ID, id, removed)
			}
			delete(ledger, id)
		}
		if !opts.dryRun {
			keep := make([]string, 0, len(seen))
			for id := range seen {
				keep = append(keep, id)
			}
			if _, err := s.DeleteSourceItems(ctx, src.ID, keep); err != nil {
				return summary, fmt.Errorf("source %s: %w", src.ID, err)
			}
		}
	}

	// The state of the source records what this run did. The last full
	// run is only moved by a full run: an incremental run keeps it.
	state := store.SourceState{
		SourceID:   src.ID,
		Type:       src.Type,
		ConfigHash: configHash,
		LastRunAt:  now,
		LastFullAt: previous.LastFullAt,
		ItemCount:  len(ledger),
	}
	if opts.full {
		state.LastFullAt = now
	}
	if !opts.dryRun {
		if err := s.PutSourceState(ctx, state); err != nil {
			return summary, fmt.Errorf("source %s: %w", src.ID, err)
		}
	}

	return summary, nil
}

// selectSources returns the configured sources a run should import.
//
// Without --source every enabled source runs. With --source exactly that
// source runs, and it has to exist and be enabled: importing a source the
// user did not enable would do what the config says not to do.
func selectSources(cfg *config.Config, sourceID string) ([]config.Source, error) {
	if sourceID == "" {
		enabled := cfg.Enabled()
		if len(enabled) == 0 {
			return nil, fmt.Errorf("no source is enabled in the config: enable one or pass --source <id>")
		}
		return enabled, nil
	}

	for _, src := range cfg.Sources {
		if src.ID != sourceID {
			continue
		}
		if !src.Enabled {
			return nil, fmt.Errorf("source %q is disabled in the config: enable it to import it", sourceID)
		}
		return []config.Source{src}, nil
	}

	ids := make([]string, 0, len(cfg.Sources))
	for _, src := range cfg.Sources {
		ids = append(ids, src.ID)
	}
	return nil, fmt.Errorf("no source with the id %q is configured (configured ids: %s)",
		sourceID, strings.Join(ids, ", "))
}

// sourceConfigHash is the hash of the resolved configuration of a source.
//
// It covers the source id, its type, its options, the chunking policy that
// decides where its content is cut, and the NAMES of the environment
// variables that carry its credentials. A credential value is never hashed
// and never stored: only the name of the variable that holds it, so a
// rotated key does not look like a changed configuration.
//
// The hash is what tells the ledger whether the index still describes the
// source as it is configured now.
func sourceConfigHash(src config.Source, chunking config.Chunking) (string, error) {
	options, err := canonicalOptions(src.Options)
	if err != nil {
		return "", fmt.Errorf("source %q: %w", src.ID, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "id=%s\n", src.ID)
	fmt.Fprintf(&b, "type=%s\n", src.Type)
	fmt.Fprintf(&b, "options=%s\n", options)
	fmt.Fprintf(&b, "chunking=target:%d,overlap:%d\n", chunking.TargetTokens, chunking.OverlapTokens)
	for _, env := range []string{src.Credentials.APIBaseURL.Env, src.Credentials.APIKey.Env} {
		if strings.TrimSpace(env) == "" {
			continue
		}
		fmt.Fprintf(&b, "env=%s\n", env)
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

// canonicalOptions is the options of a source as one stable string: the
// YAML node decoded and encoded as JSON, whose keys are sorted. The same
// options always produce the same string, whatever order they are written
// in.
func canonicalOptions(node yaml.Node) (string, error) {
	if node.Kind == 0 {
		return "{}", nil
	}
	var decoded any
	if err := node.Decode(&decoded); err != nil {
		return "", fmt.Errorf("read the options: %w", err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("encode the options: %w", err)
	}
	return string(encoded), nil
}

// newRunID is the identity of an ingest run: the UTC time it started plus
// a short random suffix, so two runs in the same second never share an id.
// It is written into the ledger as the run that last saw every item.
func newRunID() string {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// The timestamp alone still identifies the run.
		return stamp
	}
	return stamp + "-" + hex.EncodeToString(suffix[:])
}

// itemFingerprint is the cheap fingerprint of an item: the upstream entity
// tag when the provider offers one, and the provider's own hint otherwise.
//
// It is what the ledger compares to decide that an item did not change, so
// it has to change whenever the content of the item changes.
func itemFingerprint(item sources.Item) string {
	if tag := strings.TrimSpace(item.ETag); tag != "" {
		return tag
	}
	return strings.TrimSpace(item.Hint)
}

// itemModified is the upstream modification time of an item, ISO-8601 UTC,
// or the empty string when the provider does not know it.
func itemModified(item sources.Item) string {
	if item.LastModified.IsZero() {
		return ""
	}
	return item.LastModified.UTC().Format(time.RFC3339)
}

// indexChunkIDs maps every item of a source kind to the chunk ids the
// index holds for it.
func indexChunkIDs(ctx context.Context, s *store.Store, kind string) (map[string][]string, error) {
	chunks, err := s.ListChunks(ctx, 0, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string)
	for _, c := range chunks {
		if c.SourceKind != kind {
			continue
		}
		out[c.SourceID] = append(out[c.SourceID], c.ChunkID)
	}
	return out, nil
}

// chunkIDs are the ids of the chunks an item produces, in order.
func chunkIDs(kind, itemID string, count int) []string {
	ids := make([]string, 0, count)
	for seq := 0; seq < count; seq++ {
		ids = append(ids, chunkID(kind, itemID, seq))
	}
	return ids
}

// itemChunksContiguous reports whether the chunks the index holds for an
// item are the chunks the item produces, with no gap in their ordinals.
//
// The chunk ids are hashes, so their order in the index says nothing about
// the order they were produced in. The check is therefore over the set:
// the ids of the item must be exactly the ids of the ordinals 0 to n-1.
func itemChunksContiguous(have []string, kind, itemID string) bool {
	if len(have) == 0 {
		return false
	}
	expected := make(map[string]bool, len(have))
	for seq := 0; seq < len(have); seq++ {
		expected[chunkID(kind, itemID, seq)] = true
	}
	if len(expected) != len(have) {
		return false
	}
	for _, id := range have {
		if !expected[id] {
			return false
		}
	}
	return true
}

// itemChunksMatch reports whether the chunks the index holds for an item
// are exactly the chunks the item produces now.
func itemChunksMatch(have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	held := make(map[string]bool, len(have))
	for _, id := range have {
		held[id] = true
	}
	for _, id := range want {
		if !held[id] {
			return false
		}
	}
	return true
}

// staleChunkCount is how many of the chunks the index holds for an item are
// not among the chunks the item produces now.
func staleChunkCount(have, keep []string) int {
	kept := make(map[string]bool, len(keep))
	for _, id := range keep {
		kept[id] = true
	}
	stale := 0
	for _, id := range have {
		if !kept[id] {
			stale++
		}
	}
	return stale
}

// buildChunk turns one chunk of one document into the row the store keeps.
func buildChunk(sourceKind string, item sources.Item, doc sources.Document, seq int, text string) (store.Chunk, error) {
	published := doc.PublishedAt
	if published.IsZero() {
		published = item.LastModified
	}
	if published.IsZero() {
		published = time.Now()
	}

	meta := "{}"
	if len(doc.Meta) > 0 {
		encoded, err := json.Marshal(doc.Meta)
		if err != nil {
			return store.Chunk{}, fmt.Errorf("encode the metadata of %q: %w", item.ID, err)
		}
		meta = string(encoded)
	}

	return store.Chunk{
		ChunkID:     chunkID(sourceKind, item.ID, seq),
		SourceKind:  sourceKind,
		SourceID:    item.ID,
		SourceURL:   item.URL,
		Platform:    doc.Platform,
		Author:      "",
		PublishedAt: published.UTC().Format(time.RFC3339),
		UpdatedAt:   published.UTC().Format(time.RFC3339),
		ContentHash: contentHash(text),
		Title:       doc.Title,
		Section:     "",
		Body:        text,
		BodyTokens:  chunk.Tokens(text),
		MetaJSON:    meta,
		Tags:        doc.Tags,
	}, nil
}
