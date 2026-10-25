package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// defaultQueryLimit is how many hits a query returns when the caller does
// not ask for a specific number.
const defaultQueryLimit = 10

// minVectorScore is the smallest cosine similarity a vector hit may have.
//
// A chunk that is not even directionally similar to the query carries no
// information about it, and keeping it would turn every query into "here
// are the chunks that exist".
const minVectorScore = 1e-6

// runQuery retrieves the chunks that answer a question.
//
// The query text is embedded once and handed to the hybrid retrieval: the
// index is asked twice, once lexically and once semantically, and the two
// rankings are fused. No hits is not an error - a question about something
// the corpus does not cover simply has no answer.
func runQuery(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", "", "path to the SQLite index (default: AGENT_DB_PATH or ./index.db)")
	limit := fs.Int("limit", defaultQueryLimit, "how many hits to return")
	asJSON := fs.Bool("json", false, "print the hits as a JSON array")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *limit <= 0 {
		fmt.Fprintln(stderr, "agent query: --limit must be greater than zero")
		return 2
	}

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		fmt.Fprintln(stderr, "agent query: the query text is empty: pass the question to answer")
		return 2
	}

	s, err := store.Open(indexPath(*dbFlag))
	if err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}
	defer s.Close()

	client, err := newEmbedClient()
	if err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}

	ctx := context.Background()
	vectors, err := client.Embed(ctx, []string{query})
	if err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		fmt.Fprintln(stderr, "agent query: the model server returned no vector for the query")
		return 1
	}

	searcher := &indexSearcher{store: s}
	hits, err := retrieval.Retrieve(ctx, searcher, searcher, vectors[0], query, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}

	if len(hits) == 0 {
		if *asJSON {
			fmt.Fprintln(stdout, "[]")
			return 0
		}
		fmt.Fprintln(stdout, "no matches")
		return 0
	}

	if *asJSON {
		encoded, err := json.MarshalIndent(hits, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "agent query: encode the hits: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(encoded))
		return 0
	}

	for _, hit := range hits {
		title := hit.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(stdout, "%s | %s | score %.6f\n", title, hit.SourceURL, hit.Score)
	}
	return 0
}

// indexSearcher adapts the index to the two searcher interfaces the
// retrieval layer defines.
//
// The two packages hold their own hit types on purpose: the store knows
// about the index and the retrieval layer knows about ranking. The adapter
// is where the two meet, so neither has to change.
type indexSearcher struct {
	store *store.Store
}

// SearchBM25 implements retrieval.BM25Searcher.
func (a *indexSearcher) SearchBM25(ctx context.Context, query string, limit int) ([]retrieval.Hit, error) {
	expression := ftsQuery(query)
	if expression == "" {
		return nil, nil
	}
	hits, err := a.store.SearchBM25(ctx, expression, limit)
	if err != nil {
		return nil, err
	}
	out := make([]retrieval.Hit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, retrieval.Hit{
			ChunkID:     hit.ChunkID,
			Title:       hit.Title,
			SourceURL:   hit.SourceURL,
			PublishedAt: hit.PublishedAt,
			Score:       hit.Score,
		})
	}
	return out, nil
}

// SearchVector implements retrieval.VectorSearcher.
func (a *indexSearcher) SearchVector(ctx context.Context, q []float32, limit int) ([]retrieval.Hit, error) {
	hits, err := a.store.SearchVector(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]retrieval.Hit, 0, len(hits))
	for _, hit := range hits {
		if hit.Score <= minVectorScore {
			// Not similar at all: it would only add noise to the
			// fused list.
			continue
		}
		out = append(out, retrieval.Hit{
			ChunkID:     hit.ChunkID,
			Title:       hit.Title,
			SourceURL:   hit.SourceURL,
			PublishedAt: hit.PublishedAt,
			Score:       hit.Score,
		})
	}
	return out, nil
}
