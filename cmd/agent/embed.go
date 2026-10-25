package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// defaultEmbedBatch is how many chunks are embedded in one request when the
// caller does not say otherwise.
const defaultEmbedBatch = 32

// runEmbed embeds the chunks of the index that have no vector yet.
//
// Embedding is a separate command from the ingest on purpose: the model
// server is the slow, expensive half of the pipeline, and a chunk can be
// indexed long before it is embedded. The work list is the chunks without a
// vector, so a run that is interrupted can simply be run again.
func runEmbed(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent embed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", "", "path to the SQLite index (default: AGENT_DB_PATH or ./index.db)")
	limit := fs.Int("limit", 0, "cap the number of chunks to embed in this run (0 = all)")
	batch := fs.Int("batch", defaultEmbedBatch, "how many chunks to embed per request")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent embed: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *limit < 0 {
		fmt.Fprintln(stderr, "agent embed: --limit cannot be negative")
		return 2
	}
	if *batch <= 0 {
		fmt.Fprintln(stderr, "agent embed: --batch must be greater than zero")
		return 2
	}

	s, err := store.Open(indexPath(*dbFlag))
	if err != nil {
		fmt.Fprintf(stderr, "agent embed: %v\n", err)
		return 1
	}
	defer s.Close()

	ctx := context.Background()
	chunks, err := s.ListChunks(ctx, *limit, true)
	if err != nil {
		fmt.Fprintf(stderr, "agent embed: %v\n", err)
		return 1
	}
	if len(chunks) == 0 {
		fmt.Fprintf(stdout, "embed: nothing to embed, every chunk has a vector (index %s)\n", s.Path())
		return 0
	}

	client, err := newEmbedClient()
	if err != nil {
		fmt.Fprintf(stderr, "agent embed: %v\n", err)
		return 1
	}

	embedded := 0
	for start := 0; start < len(chunks); start += *batch {
		end := start + *batch
		if end > len(chunks) {
			end = len(chunks)
		}
		batchChunks := chunks[start:end]

		texts := make([]string, 0, len(batchChunks))
		for _, c := range batchChunks {
			texts = append(texts, c.Body)
		}

		vectors, err := client.Embed(ctx, texts)
		if err != nil {
			fmt.Fprintf(stderr, "agent embed: %v\n", err)
			return 1
		}
		for i, vector := range vectors {
			if err := s.PutVector(ctx, batchChunks[i].ChunkID, vector); err != nil {
				fmt.Fprintf(stderr, "agent embed: %v\n", err)
				return 1
			}
			embedded++
		}
	}

	remaining, err := s.ListChunks(ctx, 0, true)
	if err != nil {
		fmt.Fprintf(stderr, "agent embed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "embed: %d chunk(s) embedded with %s, %d chunk(s) still without a vector (index %s)\n",
		embedded, client.Model(), len(remaining), s.Path())
	return 0
}
