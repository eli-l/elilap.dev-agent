package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/requests"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// backupTimeFormat is a filename-safe UTC stamp; second resolution keeps the
// default name readable, and an explicit --out covers same-second repeats.
const backupTimeFormat = "20060102T150405Z"

// runBackup implements `agent backup`: an online VACUUM INTO snapshot of the
// index. It opens the source read-only and never creates or migrates it, so a
// typo in --db cannot leave an empty database behind.
func runBackup(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.DBPath, "path to the SQLite index to snapshot")
	reqFlag := fs.String("requests-db", "", "path to the knowledge-request store (default: requests.db beside the index; skipped when absent)")
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

	// One timestamp for the whole set: an index snapshot and the request
	// snapshot it pairs with must not be stamped a second apart, and the
	// request name needs its own uniqueness path because two runs can produce
	// distinct index names within the same second (--out a.db, --out b.db).
	stamp := time.Now().UTC().Format(backupTimeFormat)
	out := strings.TrimSpace(*outFlag)
	if out == "" {
		out = filepath.Join(filepath.Dir(*dbFlag), "index-"+stamp+".db")
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

	// The request store is original data that an index rebuild does not
	// recreate, so a backup that skipped it would silently drop questions
	// peers are waiting on.
	// Same precedence as `serve` and `agent requests`: an explicit flag wins,
	// then AGENT_REQUESTS_DB_PATH, then requests.db beside the index this
	// command actually snapshotted. Deriving it from the *parsed* index path
	// keeps `--db`/`--out` pairs coherent.
	requestsPath := strings.TrimSpace(*reqFlag)
	if requestsPath == "" {
		requestsPath = strings.TrimSpace(rtCfg.RequestsDBPath)
	}
	if requestsPath == "" {
		requestsPath = filepath.Join(filepath.Dir(*dbFlag), envcfg.DefaultRequestsDBFile)
	}
	if _, err := os.Stat(requestsPath); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "requests: no store at %s (skipped)\n", requestsPath)
		return 0
	}
	// Read-only: a backup must not create, migrate, or chmod the source, and
	// it must not need a write lock while `serve` is accepting submissions.
	reqStore, err := requests.OpenReadOnly(requestsPath)
	if err != nil {
		fmt.Fprintf(stderr, "agent backup: %v\n", err)
		return 1
	}
	defer reqStore.Close()
	reqOut := uniqueBackupPath(filepath.Dir(out), "requests-"+stamp)
	if err := reqStore.Backup(reqOut); err != nil {
		fmt.Fprintf(stderr, "agent backup: %v\n", err)
		return 1
	}
	reqInfo, err := os.Stat(reqOut)
	if err != nil {
		fmt.Fprintf(stderr, "agent backup: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "requests-db: %s\n", requestsPath)
	fmt.Fprintf(stdout, "requests-backup: %s\n", reqOut)
	fmt.Fprintf(stdout, "requests-bytes: %d\n", reqInfo.Size())
	return 0
}

// uniqueBackupPath returns <dir>/<base>.db, or <base>-2.db, <base>-3.db, ...
// when that name is taken. The index snapshot refuses an existing destination
// (a backup must never clobber one), so the paired request snapshot needs a
// free name of its own rather than failing the set.
func uniqueBackupPath(dir, base string) string {
	candidate := filepath.Join(dir, base+".db")
	for i := 2; ; i++ {
		_, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate
		}
		if err != nil {
			// The name cannot be probed (EACCES, ELOOP, EIO). Return it and
			// let Backup report the failure instead of minting new names
			// forever, which would hang the command.
			return candidate
		}
		candidate = filepath.Join(dir, base+"-"+strconv.Itoa(i)+".db")
	}
}
