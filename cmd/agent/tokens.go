package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
)

// runTokens implements `agent tokens`: issue, revoke, and list the A2A bearer
// credentials. Values are printed exactly once (on issue) and never logged.
func runTokens(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		tokensUsage(stderr)
		return 2
	}
	file := rtCfg.A2ATokensFile
	if strings.TrimSpace(file) == "" {
		file = envcfg.DefaultA2ATokensFile
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("agent tokens add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		id := fs.String("id", "", "token id recorded in audit lines (required)")
		path := fs.String("file", file, "token file to update")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(stderr, "agent tokens add: unexpected argument %q\n", fs.Arg(0))
			return 2
		}
		idValue := strings.TrimSpace(*id)
		if idValue == "" {
			fmt.Fprintln(stderr, "agent tokens add: --id is required")
			return 2
		}
		// The env list is static and cannot be edited here; reject an id it
		// already owns instead of writing a file the server will refuse.
		envOnly, err := auth.NewStore(rtCfg.A2ATokens, "")
		if err != nil {
			fmt.Fprintf(stderr, "agent tokens add: %v\n", err)
			return 1
		}
		for _, existing := range envOnly.IDs() {
			if existing == idValue {
				fmt.Fprintf(stderr, "agent tokens add: token id %q is already configured in %s; revoke it there instead\n", idValue, envcfg.EnvA2ATokens)
				return 1
			}
		}
		token, err := auth.AddToken(*path, idValue)
		if err != nil {
			fmt.Fprintf(stderr, "agent tokens add: %v\n", err)
			return 1
		}
		// The id:token line is the only place the value is ever printed.
		fmt.Fprintf(stdout, "%s:%s\n", token.ID, token.Value)
		fmt.Fprintf(stderr, "agent tokens add: wrote id %q to %s; the token is shown once, hand it to the peer over a secure channel\n", token.ID, *path)
		return 0

	case "revoke":
		fs := flag.NewFlagSet("agent tokens revoke", flag.ContinueOnError)
		fs.SetOutput(stderr)
		id := fs.String("id", "", "token id to remove (required)")
		path := fs.String("file", file, "token file to update")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(stderr, "agent tokens revoke: unexpected argument %q\n", fs.Arg(0))
			return 2
		}
		if strings.TrimSpace(*id) == "" {
			fmt.Fprintln(stderr, "agent tokens revoke: --id is required")
			return 2
		}
		if err := auth.RevokeToken(*path, strings.TrimSpace(*id)); err != nil {
			fmt.Fprintf(stderr, "agent tokens revoke: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "revoked %s\n", strings.TrimSpace(*id))
		return 0

	case "list":
		fs := flag.NewFlagSet("agent tokens list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		path := fs.String("file", file, "token file to read")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(stderr, "agent tokens list: unexpected argument %q\n", fs.Arg(0))
			return 2
		}
		store, err := auth.NewStore(rtCfg.A2ATokens, *path)
		if err != nil {
			fmt.Fprintf(stderr, "agent tokens list: %v\n", err)
			return 1
		}
		ids := store.IDs()
		if len(ids) == 0 {
			fmt.Fprintln(stdout, "no tokens configured")
			return 0
		}
		for _, id := range ids {
			fmt.Fprintln(stdout, id)
		}
		return 0

	default:
		fmt.Fprintf(stderr, "agent tokens: unknown subcommand %q\n\n", args[0])
		tokensUsage(stderr)
		return 2
	}
}

// tokensUsage documents the token subcommands.
func tokensUsage(w io.Writer) {
	fmt.Fprint(w, `usage: agent tokens <add|revoke|list> [flags]

Issue a read-only A2A bearer token. Tokens live in a 0600 file that the
server re-reads when it changes, so add/revoke need no restart.

  agent tokens add --id alice          # prints id:token once
  agent tokens revoke --id alice
  agent tokens list

The file defaults to AGENT_A2A_TOKENS_FILE (`+envcfg.DefaultA2ATokensFile+`);
--file overrides it. Tokens can also come from AGENT_A2A_TOKENS
("id:token,id2:token2"), but those are fixed at process start. "list"
shows ids from both sources; "revoke" edits the file only.
`)
}
