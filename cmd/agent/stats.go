package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// runStats implements `agent stats`: chunk, FTS, vector and ledger counts for
// the index. It is read-only.
func runStats(args []string, dbPath string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", dbPath, "path to the SQLite index")
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

	// Read-only by contract: never create or migrate the index just to count
	// rows, so a missing file is reported instead of an empty database.
	st, err := store.OpenReadOnly(*dbFlag)
	if err != nil {
		fmt.Fprintf(stderr, "agent stats: %v\n", err)
		return 1
	}
	defer st.Close()

	c, err := st.Stats()
	if err != nil {
		fmt.Fprintf(stderr, "agent stats: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "db:        %s\n", *dbFlag)
	fmt.Fprintf(stdout, "chunks:    %d\n", c.Chunks)
	fmt.Fprintf(stdout, "fts rows:  %d\n", c.FTSRows)
	fmt.Fprintf(stdout, "vec rows:  %d\n", c.VecRows)
	fmt.Fprintf(stdout, "embedded:  %d\n", c.Embedded)
	fmt.Fprintf(stdout, "sources:   %d\n", c.Items)
	if c.LastRun != "" {
		fmt.Fprintf(stdout, "last run:  %s\n", c.LastRun)
	} else {
		fmt.Fprintln(stdout, "last run:  never")
	}
	return 0
}
