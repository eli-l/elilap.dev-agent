package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/chunk"
	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// runIngest imports the configured sources into the index.
//
// The run is per source: every enabled source is listed, fetched, chunked
// and written. A source whose client does not exist yet is reported and
// skipped - it does not stop the sources that do work - and the command
// still fails when no source could be imported at all.
func runIngest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := defaultConfigPath
	fs.StringVar(&configPath, "config", defaultConfigPath, "path to sources.yaml")
	dbFlag := fs.String("db", "", "path to the SQLite index (default: AGENT_DB_PATH or ./index.db)")
	sourceFlag := fs.String("source", "", "import only this configured source")
	full := fs.Bool("full", false, "re-chunk and rewrite every item, even the unchanged ones")
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
	existing, err := existingContentHashes(ctx, s)
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
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

		items, err := provider.List(ctx, time.Time{})
		if err != nil {
			fmt.Fprintf(stderr, "agent ingest: source %s: %v\n", src.ID, err)
			return 1
		}
		if *limit > 0 && len(items) > *limit {
			items = items[:*limit]
		}

		written, unchanged := 0, 0
		for _, item := range items {
			doc, err := provider.Fetch(ctx, item)
			if err != nil {
				fmt.Fprintf(stderr, "agent ingest: source %s: %v\n", src.ID, err)
				return 1
			}

			texts := chunk.Split(doc.Body, cfg.Defaults.Chunking.TargetTokens, cfg.Defaults.Chunking.OverlapTokens)
			if len(texts) == 0 {
				continue
			}

			if !*full && itemUnchanged(provider.SourceKind(), item.ID, texts, existing) {
				unchanged++
				continue
			}

			for seq, text := range texts {
				c, err := buildChunk(provider.SourceKind(), item, doc, seq, text)
				if err != nil {
					fmt.Fprintf(stderr, "agent ingest: source %s: %v\n", src.ID, err)
					return 1
				}
				if err := s.PutChunk(ctx, c); err != nil {
					fmt.Fprintf(stderr, "agent ingest: source %s: %v\n", src.ID, err)
					return 1
				}
				written++
			}
		}

		fmt.Fprintf(stdout, "%s: %d item(s) seen, %d chunk(s) written, %d item(s) unchanged (index %s)\n",
			src.ID, len(items), written, unchanged, s.Path())
		imported++
	}

	if imported == 0 {
		fmt.Fprintf(stderr, "agent ingest: nothing was imported: %d source(s) skipped, no source could be read\n", skipped)
		return 1
	}

	fmt.Fprintf(stdout, "ingest: %d source(s) imported, %d skipped, index %s\n", imported, skipped, s.Path())
	return 0
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

// existingContentHashes maps the chunk ids in the index to their content
// hashes, so an item that did not change can be skipped.
func existingContentHashes(ctx context.Context, s *store.Store) (map[string]string, error) {
	chunks, err := s.ListChunks(ctx, 0, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(chunks))
	for _, c := range chunks {
		out[c.ChunkID] = c.ContentHash
	}
	return out, nil
}

// itemUnchanged reports whether every chunk of an item is already in the
// index with the same content.
//
// The chunk ids are derived from the source kind, the item id and the
// ordinal of the chunk, so an unchanged item produces exactly the same ids
// with exactly the same hashes.
func itemUnchanged(sourceKind, sourceID string, texts []string, existing map[string]string) bool {
	if len(existing) == 0 {
		return false
	}
	for seq, text := range texts {
		hash, ok := existing[chunkID(sourceKind, sourceID, seq)]
		if !ok || hash != contentHash(text) {
			return false
		}
	}
	return true
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
