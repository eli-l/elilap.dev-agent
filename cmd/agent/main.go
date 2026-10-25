// Command agent is the single CLI for elilap.dev-agent. One binary with
// subcommands, per PLAN.md Decision 7; each subcommand has its own file.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one CLI invocation and returns the process exit code: 0 on
// success, 1 on a runtime or validation error, and 2 on a usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "sources":
		return runSources(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "ingest":
		return runIngest(args[1:], stdout, stderr)
	case "embed":
		return runEmbed(args[1:], stdout, stderr)
	case "query":
		return runQuery(args[1:], stdout, stderr)
	case "stats":
		return runStats(args[1:], stdout, stderr)
	case "serve":
		// The A2A surface lands in a later phase. It exits non-zero on
		// purpose: a command that did nothing must never look like it
		// succeeded, or a script will treat the placeholder as a result.
		fmt.Fprintf(stderr, "agent serve: not implemented yet - the read-only A2A surface lands in Phase 4 (see PLAN.md)\n")
		return 1
	default:
		fmt.Fprintf(stderr, "agent: unknown subcommand %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

// usage writes the top-level command list.
func usage(w io.Writer) {
	fmt.Fprint(w, `usage: agent <command> [flags]

Commands:
  sources list       list configured providers and their resolved options
  sources validate   strictly validate sources.yaml and provider options
  doctor             check this machine against the runtime requirements
  ingest             import configured sources into the index
  embed              embed the indexed chunks that have no vector yet
  query              retrieve chunks for a query
  stats              report index statistics
  serve              serve the read-only A2A surface

The index lives at AGENT_DB_PATH (default ./index.db); every command that
touches it also accepts --db <path>. The model server is at LOCALAI_BASE_URL
(default http://localhost:8080/v1).

Missing credential variables warn in sources validate; --strict makes them errors.

Run "agent sources list --help", "agent sources validate --help",
"agent doctor --help", "agent ingest --help", "agent embed --help",
"agent query --help" or "agent stats --help" for flags.
`)
}
