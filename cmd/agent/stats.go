package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// runStats reports what the index holds.
func runStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", "", "path to the SQLite index (default: AGENT_DB_PATH or ./index.db)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent stats: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	s, err := store.Open(indexPath(*dbFlag))
	if err != nil {
		fmt.Fprintf(stderr, "agent stats: %v\n", err)
		return 1
	}
	defer s.Close()

	stats, err := s.Stats(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "agent stats: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "index: %s\n", s.Path())
	fmt.Fprintf(stdout, "chunks: %d\n", stats.Chunks)
	fmt.Fprintf(stdout, "vectors: %d\n", stats.Vectors)
	fmt.Fprintf(stdout, "sources: %d\n", stats.Sources)
	fmt.Fprintf(stdout, "fts rows: %d\n", stats.FTSRows)
	fmt.Fprintf(stdout, "embedding model: %s\n", envOr("EMBED_MODEL", defaultEmbedModel))
	return 0
}
