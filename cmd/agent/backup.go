package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// backupTimeFormat is a filename-safe UTC stamp; second resolution keeps the
// default name readable, and an explicit --out covers same-second repeats.
const backupTimeFormat = "20060102T150405Z"

// runBackup implements `agent backup`: an online VACUUM INTO snapshot of the
// index. It opens the source read-only and never creates or migrates it, so a
// typo in --db cannot leave an empty database behind.
func runBackup(args []string, dbPath string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", dbPath, "path to the SQLite index to snapshot")
	outFlag := fs.String("out", "", "destination file (default: index-<UTC timestamp>.db next to the index)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent backup: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	out := strings.TrimSpace(*outFlag)
	if out == "" {
		out = filepath.Join(filepath.Dir(*dbFlag), "index-"+time.Now().UTC().Format(backupTimeFormat)+".db")
	}

	st, err := store.OpenReadOnly(*dbFlag)
	if err != nil {
		fmt.Fprintf(stderr, "agent backup: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := st.Backup(out); err != nil {
		fmt.Fprintf(stderr, "agent backup: %v\n", err)
		return 1
	}
	info, err := os.Stat(out)
	if err != nil {
		fmt.Fprintf(stderr, "agent backup: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "db:     %s\n", *dbFlag)
	fmt.Fprintf(stdout, "backup: %s\n", out)
	fmt.Fprintf(stdout, "bytes:  %d\n", info.Size())
	return 0
}
