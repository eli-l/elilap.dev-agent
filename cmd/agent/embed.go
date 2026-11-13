package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// embedHTTPTimeout bounds one embeddings request. A batch is small, but the
// first call to a cold model server can be slow; the shared retrying
// transport handles transient 429/5xx on top of this.
const embedHTTPTimeout = 120 * time.Second

// runEmbed implements `agent embed`: vectorize every chunk that needs it,
// in batches, resuming where a previous run stopped. It reads only the local
// index and writes only chunks_vec plus the embedded_with/embedded_at stamps.
func runEmbed(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent embed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", rtCfg.EmbedModel, "embeddings model id")
	batch := fs.Int("batch", embed.DefaultBatchSize, "chunks per embeddings request")
	limit := fs.Int("limit", 0, "embed at most this many chunks (0 = all pending)")
	full := fs.Bool("full", false, "re-embed every chunk, ignoring the current tag")
	dryRun := fs.Bool("dry-run", false, "report how many chunks are pending without writing")
	progress := fs.Bool("progress", false, "show a live progress line on stderr")
	timeout := fs.Duration("timeout", 30*time.Minute, "overall run timeout")
	dbFlag := fs.String("db", rtCfg.DBPath, "path to the SQLite index")
	migrationsDir := fs.String("migrations", defaultMigrationsDir, "directory holding the SQL migrations")
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
	if *timeout <= 0 {
		fmt.Fprintf(stderr, "agent embed: --timeout must be positive\n")
		return 2
	}
	if *batch <= 0 {
		fmt.Fprintf(stderr, "agent embed: --batch must be positive\n")
		return 2
	}
	if *limit < 0 {
		fmt.Fprintf(stderr, "agent embed: --limit must not be negative\n")
		return 2
	}
	// The live line is an unterminated \r record on stderr; under JSON logs
	// that would corrupt the JSONL stream a collector reads, so the flag is
	// ignored (with a proper log record) rather than silently breaking it.
	if *progress && logging.Format() == logging.FormatJSON {
		log := logging.Main()
		log.Warn().Msg("embed: --progress disabled under json log format")
		*progress = false
	}

	var st *store.Store
	var err error
	if *dryRun {
		st, err = store.OpenReadOnly(*dbFlag)
		if errors.Is(err, store.ErrNoDatabase) {
			fmt.Fprintf(stderr, "agent embed: --dry-run needs an existing index; run without it to create %s\n", *dbFlag)
			return 1
		}
	} else {
		st, err = store.Open(*dbFlag, *migrationsDir, int(rtCfg.EmbedDims))
	}
	if err != nil {
		fmt.Fprintf(stderr, "agent embed: %v\n", err)
		return 1
	}
	defer st.Close()

	tag := embed.Tag(*model)
	if *dryRun {
		// A width change makes the next real run rebuild chunks_vec, which
		// drops every stored vector. Count the whole corpus then, so a
		// dry-run after an EMBED_DIMS change does not report "nothing
		// pending" for a run that will re-embed everything.
		rebuild := st.Dims() != int(rtCfg.EmbedDims)
		if rebuild {
			fmt.Fprintf(stderr, "agent embed: EMBED_DIMS=%d differs from the index (%d); the next run rebuilds chunks_vec and re-embeds every chunk\n",
				int(rtCfg.EmbedDims), st.Dims())
		}
		pending, err := st.CountPendingEmbeddings(tag, *full || rebuild)
		if err != nil {
			fmt.Fprintf(stderr, "agent embed: %v\n", err)
			return 1
		}
		if *limit > 0 && pending > *limit {
			pending = *limit
		}
		printEmbedResult(stdout, embedResult{
			Model: *model, Tag: tag, Full: *full, DryRun: true, Pending: pending,
		})
		return 0
	}

	client, err := llm.NewClient(rtCfg.EmbedBaseURL, rtCfg.EmbedAPIKey,
		shared.NewHTTPClient(embedHTTPTimeout))
	if err != nil {
		fmt.Fprintf(stderr, "agent embed: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	runner := &embed.Runner{Store: st, Embedder: client}
	opts := embed.Options{
		Model:     *model,
		BatchSize: *batch,
		Limit:     *limit,
		Full:      *full,
	}
	// --progress writes an overwriting line to stderr while the run is in
	// flight; the final result still goes to stdout unchanged.
	var printer *progressPrinter
	if *progress {
		printer = newProgressPrinter(stderr)
		opts.Progress = printer.update
	}
	res, runErr := runner.Run(ctx, opts)
	if printer != nil {
		printer.stop()
	}
	printEmbedResult(stdout, embedResult{
		Model:     *model,
		Tag:       tag,
		Full:      *full,
		Pending:   res.Pending,
		Embedded:  res.Embedded,
		Skipped:   res.Skipped,
		Remaining: res.Remaining,
		Batches:   res.Batches,
	})
	if runErr != nil {
		log := logging.Main()
		log.Error().Err(runErr).Msg("embed finished with errors")
		fmt.Fprintf(stderr, "agent embed: %v\n", runErr)
		return 1
	}
	return 0
}

// embedResult is the printable summary of one embed invocation.
type embedResult struct {
	Model     string `json:"model"`
	Tag       string `json:"tag"`
	Full      bool   `json:"full"`
	DryRun    bool   `json:"dry_run,omitempty"`
	Pending   int    `json:"pending"`
	Embedded  int    `json:"embedded"`
	Skipped   int    `json:"skipped,omitempty"`
	Remaining int    `json:"remaining"`
	Batches   int    `json:"batches"`
}

// printEmbedResult writes the summary as JSONL when the log format is json,
// matching `agent ingest | jq`, or as text lines otherwise.
func printEmbedResult(w io.Writer, r embedResult) {
	if logging.Format() == logging.FormatJSON {
		_ = json.NewEncoder(w).Encode(r)
		return
	}
	fmt.Fprintf(w, "model:      %s\n", r.Model)
	fmt.Fprintf(w, "tag:        %s\n", r.Tag)
	if r.DryRun {
		fmt.Fprintf(w, "pending:    %d\n", r.Pending)
		return
	}
	fmt.Fprintf(w, "pending:    %d\n", r.Pending)
	fmt.Fprintf(w, "embedded:   %d\n", r.Embedded)
	if r.Skipped > 0 {
		fmt.Fprintf(w, "skipped:    %d\n", r.Skipped)
	}
	fmt.Fprintf(w, "remaining:  %d\n", r.Remaining)
	fmt.Fprintf(w, "batches:    %d\n", r.Batches)
}
