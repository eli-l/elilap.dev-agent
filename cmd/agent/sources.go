package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources"
	"gopkg.in/yaml.v3"
)

// defaultConfigPath is the config location used when --config is absent.
const defaultConfigPath = "sources.yaml"

// runSources dispatches the `agent sources` subcommands.
func runSources(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "agent sources: expected a subcommand: list, validate\n")
		return 2
	}
	switch args[0] {
	case "list":
		return runSourcesList(args[1:], stdout, stderr)
	case "validate":
		return runSourcesValidate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "agent sources: unknown subcommand %q (expected list or validate)\n", args[0])
		return 2
	}
}

// newRegistry builds the registry with every built-in provider registered.
func newRegistry() (*sources.Registry, error) {
	r := sources.NewRegistry()
	if err := sources.RegisterBuiltins(r); err != nil {
		return nil, fmt.Errorf("register providers: %w", err)
	}
	return r, nil
}

// parseConfigFlag parses one subcommand's flags and loads the config. The
// returned code is meaningful only when ok is false.
func parseConfigFlag(name string, args []string, stderr io.Writer, register func(*flag.FlagSet)) (cfg *config.Config, path string, code int, ok bool) {
	fs := flag.NewFlagSet("agent sources "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	path = defaultConfigPath
	fs.StringVar(&path, "config", defaultConfigPath, "path to sources.yaml")
	if register != nil {
		register(fs)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, path, 0, false
		}
		return nil, path, 2, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent sources %s: unexpected argument %q\n", name, fs.Arg(0))
		return nil, path, 2, false
	}

	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "agent sources %s: %v\n", name, err)
		return nil, path, 1, false
	}
	return cfg, path, 0, true
}

// runSourcesList prints every configured source with its resolved options
// and the environment variable names its credentials read. Credential
// values are never printed - only names and whether they are set.
func runSourcesList(args []string, stdout, stderr io.Writer) int {
	cfg, path, code, ok := parseConfigFlag("list", args, stderr, nil)
	if !ok {
		return code
	}

	reg, err := newRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "agent sources list: %v\n", err)
		return 1
	}

	unset := make(map[string]bool)
	for _, name := range cfg.MissingEnv() {
		unset[name] = true
	}

	fmt.Fprintf(stdout, "config: %s (version %d, %d source(s), %d enabled)\n",
		path, cfg.Version, len(cfg.Sources), len(cfg.Enabled()))
	if cfg.Defaults.UserAgent != "" {
		fmt.Fprintf(stdout, "user_agent: %s\n", cfg.Defaults.UserAgent)
	}
	fmt.Fprintf(stdout, "chunking: target_tokens=%d overlap_tokens=%d\n",
		cfg.Defaults.Chunking.TargetTokens, cfg.Defaults.Chunking.OverlapTokens)

	if len(cfg.Sources) == 0 {
		fmt.Fprintln(stdout, "\nno sources configured")
		return 0
	}

	for _, src := range cfg.Sources {
		state := "disabled"
		if src.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(stdout, "\n- id: %s\n  type: %s\n  state: %s\n", src.ID, src.Type, state)

		opts, err := reg.ParseOptions(src)
		if err != nil {
			fmt.Fprintf(stderr, "agent sources list: %v\n", err)
			return 1
		}
		rendered, err := renderYAML(opts)
		if err != nil {
			fmt.Fprintf(stderr, "agent sources list: source %q: render options: %v\n", src.ID, err)
			return 1
		}
		fmt.Fprintln(stdout, "  options:")
		for _, line := range strings.Split(strings.TrimRight(string(rendered), "\n"), "\n") {
			fmt.Fprintf(stdout, "    %s\n", line)
		}

		fmt.Fprintln(stdout, "  credentials:")
		printCredential(stdout, "api_url", src.Credentials.APIBaseURL, unset)
		printCredential(stdout, "api_key", src.Credentials.APIKey, unset)
	}
	return 0
}

// renderYAML encodes v as YAML with two-space indent and no trailing
// newline, so it can be indented under a source block.
func renderYAML(v any) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// printCredential writes one credential reference as name plus set state.
func printCredential(w io.Writer, field string, ref config.EnvRef, unset map[string]bool) {
	if ref.Env == "" {
		fmt.Fprintf(w, "    %s: (not set)\n", field)
		return
	}
	state := "set"
	if unset[ref.Env] {
		state = "unset"
	}
	fmt.Fprintf(w, "    %s: env %s (%s)\n", field, ref.Env, state)
}

// runSourcesValidate validates the config structurally and validates each
// source's options against its provider's schema. Missing credential
// environment variables are warnings by default and errors under --strict.
func runSourcesValidate(args []string, stdout, stderr io.Writer) int {
	var strict bool
	cfg, path, code, ok := parseConfigFlag("validate", args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&strict, "strict", false, "treat missing credential environment variables as errors")
	})
	if !ok {
		return code
	}

	reg, err := newRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "agent sources validate: %v\n", err)
		return 1
	}
	if err := reg.ValidateAll(cfg); err != nil {
		fmt.Fprintf(stderr, "agent sources validate: %v\n", err)
		return 1
	}

	missing := cfg.MissingEnv()
	for _, name := range missing {
		if strict {
			fmt.Fprintf(stderr, "agent sources validate: credential environment variable %s is not set\n", name)
			continue
		}
		fmt.Fprintf(stderr, "agent sources validate: warning: credential environment variable %s is not set\n", name)
	}
	if strict && len(missing) > 0 {
		return 1
	}

	fmt.Fprintf(stdout, "%s: ok (version %d, %d source(s), %d enabled, %d provider type(s) validated)\n",
		path, cfg.Version, len(cfg.Sources), len(cfg.Enabled()), len(reg.KnownTypes()))
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "warning: %d credential environment variable(s) not set: %s\n",
			len(missing), strings.Join(missing, ", "))
	}
	return 0
}
