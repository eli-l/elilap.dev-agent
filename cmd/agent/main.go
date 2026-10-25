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
	case "ingest", "embed", "query", "stats", "serve":
		// Placeholders until the store layer lands.
		fmt.Fprintf(stdout, "%s: not implemented (Phase 1)\n", args[0])
		return 0
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
  ingest             import configured sources into the index
  embed              embed indexed chunks
  query              retrieve chunks for a query
  stats              report index statistics
  serve              serve the read-only A2A surface

Missing credential variables warn in sources validate; --strict makes them errors.

Run "agent sources list --help" or "agent sources validate --help" for flags.
`)
}
