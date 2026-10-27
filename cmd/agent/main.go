// Command agent is the single CLI for elilap.dev-agent. One binary with
// subcommands, per PLAN.md Decision 7; each subcommand has its own file.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one CLI invocation and returns the process exit code: 0 on
// success, 1 on a runtime or validation error, and 2 on a usage error.
//
// Logs go to stderr on the "main" module logger; stdout stays reserved for
// command output, so `agent ... | jq` is never polluted by diagnostics.
func run(args []string, stdout, stderr io.Writer) int {
	args, logCfg, err := takeLoggingFlags(args, logging.ConfigFromEnv())
	if err != nil {
		fmt.Fprintf(stderr, "agent: %v\n\n", err)
		usage(stderr)
		return 2
	}
	if err := logging.Setup(logCfg, stderr); err != nil {
		fmt.Fprintf(stderr, "agent: %v\n", err)
		return 2
	}
	log := logging.Main()

	if len(args) == 0 {
		log.Debug().Msg("no subcommand given")
		usage(stderr)
		return 2
	}
	log.Debug().
		Str("command", args[0]).
		Msg("starting")

	var code int
	switch args[0] {
	case "sources":
		code = runSources(args[1:], stdout, stderr)
	case "ingest", "embed", "query", "stats", "serve":
		// Placeholders until the store layer lands.
		log.Warn().Str("command", args[0]).Msg("not implemented (Phase 1)")
		fmt.Fprintf(stdout, "%s: not implemented (Phase 1)\n", args[0])
	default:
		log.Error().Str("command", args[0]).Msg("unknown subcommand")
		fmt.Fprintf(stderr, "agent: unknown subcommand %q\n\n", args[0])
		usage(stderr)
		code = 2
	}
	log.Debug().Int("exit_code", code).Msg("finished")
	return code
}

// loggingFlagHelp documents the global logging flags in usage text.
const loggingFlagHelp = `Global flags (anywhere in the command line):
  --log-level   trace|debug|info|warn|error|disabled (env AGENT_LOG_LEVEL, default info)
  --log-format  text|json (env AGENT_LOG_FORMAT, default text)
`

// takeLoggingFlags removes the global logging flags from args - they may
// appear anywhere, since each subcommand parses its own FlagSet - and applies
// them over the environment defaults. It returns the remaining arguments.
func takeLoggingFlags(args []string, cfg logging.Config) ([]string, logging.Config, error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		var target *string
		switch name {
		case "--log-level", "-log-level":
			target = &cfg.Level
		case "--log-format", "-log-format":
			target = &cfg.Format
		default:
			rest = append(rest, args[i])
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, cfg, fmt.Errorf("%s: a value is required", name)
			}
			i++
			value = args[i]
		}
		*target = value
	}
	return rest, cfg, nil
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

`+loggingFlagHelp+`
Run "agent sources list --help" or "agent sources validate --help" for flags.
`)
}
