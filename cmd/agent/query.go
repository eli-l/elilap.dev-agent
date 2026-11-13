package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// queryTimeout bounds one CLI query, embedding call included.
const queryTimeout = 30 * time.Second

// platformList collects a repeatable --platform flag.
type platformList []string

func (p *platformList) String() string { return strings.Join(*p, ",") }

func (p *platformList) Set(value string) error {
	*p = append(*p, value)
	return nil
}

// runQuery implements `agent query`: the raw retrieval tool, with no LLM in the
// loop. It is the CLI twin of POST /v1/retrieve and shares retrieval.Searcher
// with it, so both return the same chunks for the same parameters.
func runQuery(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	top := fs.Int("top", retrieval.DefaultTopN, "maximum number of chunks to return")
	hybrid := fs.Bool("hybrid", true, "merge BM25 and vector results (false = vector only)")
	source := fs.String("source", "all", "restrict to ghost, postiz, web, or all")
	var platforms platformList
	fs.Var(&platforms, "platform", "restrict to a Postiz platform (repeatable)")
	after := fs.String("after", "", "only chunks published on or after this date (YYYY-MM-DD or RFC3339)")
	before := fs.String("before", "", "only chunks published before this date (YYYY-MM-DD or RFC3339)")
	excludeScheduled := fs.Bool("exclude-scheduled", false, "drop Postiz posts still in the QUEUE state")
	halflife := fs.Float64("recency-halflife-days", 0, "recency half-life in days (0 disables weighting)")
	format := fs.String("format", "text", "output format: text or jsonl")
	dbFlag := fs.String("db", rtCfg.DBPath, "path to the SQLite index")
	model := fs.String("model", rtCfg.EmbedModel, "embeddings model id")
	timeout := fs.Duration("timeout", queryTimeout, "overall request timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		fmt.Fprintf(stderr, "agent query: provide a query, for example: agent query \"harness lock-in\"\n")
		return 2
	}
	if *format != "text" && *format != "jsonl" {
		fmt.Fprintf(stderr, "agent query: --format must be text or jsonl, got %q\n", *format)
		return 2
	}
	if *top < 0 {
		fmt.Fprintf(stderr, "agent query: --top must not be negative\n")
		return 2
	}
	if *top == 0 {
		fmt.Fprintf(stderr, "agent query: --top must be at least 1 (default %d)\n", retrieval.DefaultTopN)
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintf(stderr, "agent query: --timeout must be positive\n")
		return 2
	}
	if *halflife < 0 || math.IsNaN(*halflife) {
		fmt.Fprintf(stderr, "agent query: --recency-halflife-days must not be negative\n")
		return 2
	}

	// Read-only by contract: a query never creates, migrates, or writes the
	// index. A missing file is the "run ingest + embed first" case.
	st, err := store.OpenReadOnly(*dbFlag)
	if errors.Is(err, store.ErrNoDatabase) {
		fmt.Fprintf(stderr, "agent query: no index at %s; run `agent ingest` and `agent embed` first\n", *dbFlag)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := verifyIndexModel(st, *model, int(rtCfg.EmbedDims)); err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}

	client, err := llm.NewClient(rtCfg.EmbedBaseURL, rtCfg.EmbedAPIKey,
		shared.NewHTTPClient(embedHTTPTimeout))
	if err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}
	searcher := &retrieval.Searcher{Store: st, Embedder: client, Model: *model}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	hits, err := searcher.Search(ctx, retrieval.Query{
		Text:                text,
		TopN:                *top,
		Source:              *source,
		Platforms:           platforms,
		After:               *after,
		Before:              *before,
		RecencyHalflifeDays: *halflife,
		ExcludeScheduled:    *excludeScheduled,
		VectorOnly:          !*hybrid,
	})
	if err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		if errors.Is(err, retrieval.ErrInvalidQuery) {
			return 2
		}
		return 1
	}
	if err := printQueryHits(stdout, *format, hits); err != nil {
		fmt.Fprintf(stderr, "agent query: %v\n", err)
		return 1
	}
	return 0
}

// printQueryHits renders the tool result. jsonl is one hit per line (the shape
// the model's tool call receives and the HTTP array carries); text is the
// human view over the same struct.
func printQueryHits(w io.Writer, format string, hits []retrieval.Hit) error {
	switch format {
	case "jsonl":
		enc := json.NewEncoder(w)
		for i := range hits {
			if err := enc.Encode(hits[i]); err != nil {
				return err
			}
		}
	case "text":
		if len(hits) == 0 {
			fmt.Fprintln(w, "no results")
			return nil
		}
		for i, h := range hits {
			fmt.Fprintf(w, "%d. score %.6f", i+1, h.Score)
			if h.Cosine != nil {
				fmt.Fprintf(w, "  cosine %.4f", *h.Cosine)
			}
			if h.Distance != nil {
				fmt.Fprintf(w, "  distance %.4f", *h.Distance)
			}
			if h.BM25 != nil {
				fmt.Fprintf(w, "  bm25 %.4f", *h.BM25)
			}
			fmt.Fprintln(w)
			heading := h.Title
			if h.Section != "" {
				heading += " / " + h.Section
			}
			if heading != "" {
				fmt.Fprintf(w, "   %s\n", heading)
			}
			if h.SourceURL != "" {
				fmt.Fprintf(w, "   %s\n", h.SourceURL)
			}
			fmt.Fprintf(w, "   chunk_id: %s  source: %s  published: %s\n",
				h.ChunkID, h.SourceKind, h.PublishedAt)
			fmt.Fprintf(w, "   %s\n\n", strings.ReplaceAll(h.Body, "\n", "\n   "))
		}
	default:
		return fmt.Errorf("unknown format %q", format)
	}
	return nil
}
