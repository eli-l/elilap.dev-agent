package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/ingest"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// defaultMigrationsDir is where the embedded schema lives in a host checkout;
// the container sets --migrations explicitly if it differs.
const defaultMigrationsDir = "migrations"

// runIngest implements `agent ingest`: incremental by default, --full to
// re-fetch everything, --source to scope one provider, --dry-run to report
// the plan, and --since to index a bounded window (for example 7d).
func runIngest(args []string, dbPath string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath, "path to sources.yaml")
	sourceID := fs.String("source", "", "limit the run to one source id")
	full := fs.Bool("full", false, "re-fetch every listed item instead of trusting the ledger")
	dryRun := fs.Bool("dry-run", false, "report what would change without writing (needs an existing index)")
	sinceRaw := fs.String("since", "", "lower bound: RFC3339, YYYY-MM-DD, or a duration like 168h/7d/1w")
	overlap := fs.Duration("overlap", ingest.DefaultOverlap, "incremental list overlap before the last run")
	migrationsDir := fs.String("migrations", defaultMigrationsDir, "directory holding the SQL migrations")
	timeout := fs.Duration("timeout", 15*time.Minute, "overall run timeout")
	dbFlag := fs.String("db", dbPath, "path to the SQLite index")
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
	if *timeout <= 0 {
		fmt.Fprintf(stderr, "agent ingest: --timeout must be positive\n")
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}
	selected, err := ingest.Select(cfg, *sourceID)
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}

	opts := ingest.Options{
		Full:    *full,
		DryRun:  *dryRun,
		Overlap: *overlap,
		Chunking: shared.ChunkerConfig{
			TargetTokens:  cfg.Defaults.Chunking.TargetTokens,
			OverlapTokens: cfg.Defaults.Chunking.OverlapTokens,
		},
		HTTPClient: shared.NewHTTPClient(shared.DefaultHTTPTimeout),
	}
	if *sinceRaw != "" {
		since, err := ingest.ParseSince(*sinceRaw, time.Now())
		if err != nil {
			fmt.Fprintf(stderr, "agent ingest: %v\n", err)
			return 2
		}
		opts.Since = since
		opts.SinceSet = true
	}

	registry, err := newRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}
	st, err := openIngestStore(*dbFlag, *migrationsDir, *dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "agent ingest: %v\n", err)
		return 1
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	runner := &ingest.Runner{Store: st, Registry: registry}
	results, runErr := runner.Run(ctx, selected, opts)

	printIngestResults(stdout, results, *dryRun)
	log := logging.Main()
	if runErr != nil {
		log.Error().Err(runErr).Msg("ingest finished with errors")
		fmt.Fprintf(stderr, "agent ingest: %v\n", runErr)
		return 1
	}
	if !*dryRun {
		log.Info().Int("sources", len(results)).Msg("ingest complete")
	}
	return 0
}

// openIngestStore opens the index for a run. A dry run must not create or
// migrate the database, so it opens read-only and requires an existing index.
func openIngestStore(dbPath, migrationsDir string, dryRun bool) (*store.Store, error) {
	if dryRun {
		st, err := store.OpenReadOnly(dbPath)
		if errors.Is(err, store.ErrNoDatabase) {
			return nil, fmt.Errorf("--dry-run needs an existing index; run without it to create %s", dbPath)
		}
		return st, err
	}
	return store.Open(dbPath, migrationsDir)
}

// printIngestResults writes the per-source summary to stdout.
func printIngestResults(w io.Writer, results []ingest.Result, dryRun bool) {
	for _, r := range results {
		since := "all"
		if !r.Since.IsZero() {
			since = r.Since.Format(time.RFC3339)
		}
		if dryRun {
			// The run returned before writing, so Fetched/Added/Removed are
			// zero; the plan itself is the only source of truth here.
			toIndex, toRemove := 0, 0
			for _, id := range r.Planned {
				if strings.HasPrefix(id, "-") {
					toRemove++
				} else {
					toIndex++
				}
			}
			fmt.Fprintf(w, "%s: would index %d item(s) since %s, remove %d\n", r.SourceID, toIndex, since, toRemove)
			for _, id := range r.Planned {
				fmt.Fprintf(w, "  %s\n", id)
			}
			continue
		}
		fmt.Fprintf(w, "%s: indexed %d item(s) since %s: fetched %d, chunks +%d/~%d, removed %d\n",
			r.SourceID, r.Listed, since, r.Fetched, r.Added, r.Updated, r.Removed)
	}
}
