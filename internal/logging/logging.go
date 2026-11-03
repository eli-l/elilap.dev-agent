// Package logging owns the process-wide zerolog root and the per-module child
// loggers the binary logs through: main (application lifecycle), http (HTTP
// surfaces), sources (upstream fetchers such as Ghost and Postiz), db (the
// SQLite index, including retrieval reads), and agent (the grounded loop's
// chat requests and responses).
//
// Every module logger is a child of one root and carries a "module" field, so
// a single level knob and a single writer configure the whole process. The
// root discards everything until Setup runs, which keeps library packages and
// tests silent unless the binary configures logging.
//
// Nothing sensitive goes through these loggers: sources log item ids and
// environment variable names, never credential values or document bodies
// (AGENTS.md). The one deliberate exception is the agent module at debug
// level, where the grounded loop logs the exact prompts, excerpts, and model
// replies it processes so retrieval can be debugged end to end; see
// docs/logging.md.
package logging

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/kelseyhightower/envconfig"
	"github.com/rs/zerolog"
)

// Module names. Each is the value of the "module" field on every line the
// module logs.
const (
	// ModuleMain logs application lifecycle events from the CLI.
	ModuleMain = "main"
	// ModuleHTTP logs HTTP server requests.
	ModuleHTTP = "http"
	// ModuleSources logs upstream fetchers (Ghost, Postiz).
	ModuleSources = "sources"
	// ModuleDB logs database open, migration, and write activity, plus the
	// retrieval read path (query, candidates, ranked hits) at debug level.
	ModuleDB = "db"
	// ModuleAgent logs the grounded agent loop: the chat requests sent to the
	// model and the replies it returns, at debug level.
	ModuleAgent = "agent"
)

// modules is the fixed set of module loggers Setup builds.
var modules = []string{ModuleMain, ModuleHTTP, ModuleSources, ModuleDB, ModuleAgent}

// EnvPrefix is the prefix envconfig reads; Config's fields therefore map to
// AGENT_LOG_LEVEL and AGENT_LOG_FORMAT. Runtime values live in the
// environment, per AGENTS.md.
const EnvPrefix = "AGENT_LOG"

// Environment variable names carrying the logging configuration.
const (
	EnvLevel  = EnvPrefix + "_LEVEL"
	EnvFormat = EnvPrefix + "_FORMAT"
)

// Defaults for the two settings. They are applied after decoding, so an unset
// *or empty* variable means the default (an empty value in .env behaves like
// an unset one, per envcfg.DefaultOr).
const (
	// DefaultLevel keeps a normal run quiet.
	DefaultLevel = "info"
	// DefaultFormat is human-readable console output.
	DefaultFormat = "text"
)

// Format names accepted by Config.Format and reported by Format.
const (
	// FormatText is human-readable console output (the default).
	FormatText = DefaultFormat
	// FormatJSON is one JSON record per line.
	FormatJSON = "json"
)

// Config is the resolved logging configuration. Values come from the
// environment through envconfig (see ConfigFromEnv); the Default* constants
// are what an unset or empty variable yields.
type Config struct {
	// Level is a zerolog level name: trace, debug, info, warn, error,
	// fatal, panic, or disabled. Empty means info.
	Level string `envconfig:"LEVEL"`
	// Format selects the line encoding: text (human-readable console) or
	// json. Empty means text.
	Format string `envconfig:"FORMAT"`
}

// ConfigFromEnv reads the logging configuration from the environment,
// defaulting to info-level console output. An error means a variable is set
// to something envconfig cannot decode; it never contains a value.
func ConfigFromEnv() (Config, error) {
	var cfg Config
	if err := envconfig.Process(EnvPrefix, &cfg); err != nil {
		return Config{}, fmt.Errorf("logging environment: %w", err)
	}
	cfg.Level = envcfg.DefaultOr(strings.TrimSpace(cfg.Level), DefaultLevel)
	cfg.Format = envcfg.DefaultOr(strings.TrimSpace(cfg.Format), DefaultFormat)
	return cfg, nil
}

var (
	mu sync.RWMutex
	// child holds the module loggers; empty (all lookups fall back to a
	// no-op logger) until Setup runs.
	child = map[string]zerolog.Logger{}
	// format is the resolved output format ("text" or "json"). Command
	// output follows it too, so a JSON run never mixes a human line into a
	// stream a collector is parsing.
	format = DefaultFormat
)

// Setup installs the root logger writing to out and rebuilds every module
// child logger from it. It returns an error for an unknown level or format so
// a typo fails at startup instead of silently changing verbosity.
func Setup(cfg Config, out io.Writer) error {
	if out == nil {
		return fmt.Errorf("logging: output writer is required")
	}
	lvl, err := ParseLevel(cfg.Level)
	if err != nil {
		return err
	}
	w, err := writerFor(cfg.Format, out)
	if err != nil {
		return err
	}

	root := zerolog.New(w).Level(lvl).With().Timestamp().Logger()
	next := make(map[string]zerolog.Logger, len(modules))
	for _, name := range modules {
		next[name] = root.With().Str("module", name).Logger()
	}

	mu.Lock()
	child = next
	format = normalizeFormat(cfg.Format)
	mu.Unlock()
	return nil
}

// Reset discards the configured loggers and returns the package to its silent
// default. It exists so a caller (or a test) can tear down a Setup.
func Reset() {
	mu.Lock()
	child = map[string]zerolog.Logger{}
	format = DefaultFormat
	mu.Unlock()
}

// Format returns the resolved output format: "text" or "json". Command
// output renders through it so `agent ingest | jq` works under
// AGENT_LOG_FORMAT=json without a second format flag. Before Setup it is
// DefaultFormat.
func Format() string {
	mu.RLock()
	defer mu.RUnlock()
	return format
}

// normalizeFormat lower-cases a format name, mapping empty to the default.
func normalizeFormat(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return DefaultFormat
	}
	return name
}

// Logger returns the child logger for one module. An unknown module, or any
// module before Setup has run, gets a no-op logger.
func Logger(module string) zerolog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	if l, ok := child[module]; ok {
		return l
	}
	return zerolog.Nop()
}

// Main returns the application-lifecycle logger.
func Main() zerolog.Logger { return Logger(ModuleMain) }

// HTTP returns the HTTP-server logger.
func HTTP() zerolog.Logger { return Logger(ModuleHTTP) }

// Sources returns the upstream-fetcher logger.
func Sources() zerolog.Logger { return Logger(ModuleSources) }

// DB returns the database logger.
func DB() zerolog.Logger { return Logger(ModuleDB) }

// Agent returns the grounded-loop logger. Its debug lines carry the prompts,
// tool results, and model replies the loop processes.
func Agent() zerolog.Logger { return Logger(ModuleAgent) }

// ParseLevel maps a level name to its zerolog level. An empty name is info.
func ParseLevel(name string) (zerolog.Level, error) {
	if strings.TrimSpace(name) == "" {
		return zerolog.InfoLevel, nil
	}
	lvl, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(name)))
	if err != nil {
		return zerolog.NoLevel, fmt.Errorf("log level %q: want trace, debug, info, warn, error, fatal, panic, or disabled", name)
	}
	return lvl, nil
}

// writerFor returns the encoder for a format name: a colored console writer
// for text when out is a terminal, plain JSON otherwise.
func writerFor(format string, out io.Writer) (io.Writer, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatText:
		return zerolog.ConsoleWriter{
			Out:        out,
			TimeFormat: time.RFC3339,
			NoColor:    !isTerminal(out),
		}, nil
	case FormatJSON:
		return out, nil
	default:
		return nil, fmt.Errorf("log format %q: want text or json", format)
	}
}

// isTerminal reports whether out is a character device, so colored console
// output is only used when a human is reading it.
func isTerminal(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
