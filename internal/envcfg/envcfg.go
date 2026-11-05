// Package envcfg owns the process environment layer: it loads a local .env at
// startup, decodes every static runtime setting into a typed struct, and
// provides the one accessor for the dynamically named credential variables
// that sources.yaml references.
//
// Two inputs, one precedence: a variable already present in the real process
// environment wins over the value in .env (godotenv never overrides), so
// containers, cron, launchd, and `VAR=... ./agent` all override a developer's
// local file. Struct defaults apply when a variable is unset *or empty*, so a
// .env copied from example.env - secrets blank, defaults listed - works
// unchanged.
//
// Every variable the binary reads is listed in example.env at the repo root.
// A new variable is not committed until it appears there with an empty value
// (secrets) or its default (AGENTS.md, docs/environment.md).
package envcfg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

// DotEnvPath is the file LoadDotEnv reads, relative to the working directory.
const DotEnvPath = ".env"

// EnvPrefix prefixes the knobs this service owns. The model-backend and
// credential variables keep their upstream-style names (LOCALAI_*, EMBED_MODEL,
// CHAT_MODEL, GHOST_*, POSTIZ_*) instead of growing an AGENT_ prefix.
const EnvPrefix = "AGENT"

// Environment variable names. Credential names are not listed here: they are
// declared per source in sources.yaml and resolved by Lookup.
const (
	// EnvDBPath is the single-file SQLite index location.
	EnvDBPath = EnvPrefix + "_DB_PATH"
	// EnvLocalAIBaseURL is the OpenAI-compatible base URL, with /v1.
	EnvLocalAIBaseURL = "LOCALAI_BASE_URL"
	// EnvLocalAIAPIKey is the optional bearer token for that backend.
	EnvLocalAIAPIKey = "LOCALAI_API_KEY"
	// EnvEmbedModel is the embedding model id.
	EnvEmbedModel = "EMBED_MODEL"
	// EnvChatModel is the chat model id used by the agent loop.
	EnvChatModel = "CHAT_MODEL"
)

// Defaults for the settings above. They are exported so callers, docs, and
// tests agree with the one definition.
const (
	// DefaultDBPath is where the index lives in a host run; the container
	// sets /data/index.db explicitly (PLAN.md Decision 11).
	DefaultDBPath = "./index.db"
	// DefaultLocalAIBaseURL is the verified self-hosted instance (Decision 10).
	DefaultLocalAIBaseURL = "http://192.168.78.243:8080/v1"
	// DefaultEmbedModel produces the 768-dim vectors chunks_vec stores.
	DefaultEmbedModel = "embeddinggemma-300m"
	// DefaultChatModel is the reasoning model the agent loop calls.
	DefaultChatModel = "spark-x2.5-4b"
)

// Config is every static runtime setting, decoded by envconfig from the
// environment (and therefore, once main has run, from .env as well).
//
// The envconfig tag is the full variable name because this surface mixes
// prefixes; the exported Env* constants are the source of truth and a test
// pins the tags to them.
type Config struct {
	// DBPath is the SQLite index file (migrations are applied to it on open).
	DBPath string `envconfig:"AGENT_DB_PATH"`
	// LocalAIBaseURL is the OpenAI-compatible endpoint, including /v1.
	LocalAIBaseURL string `envconfig:"LOCALAI_BASE_URL"`
	// LocalAIAPIKey is optional; empty means the backend needs no token.
	LocalAIAPIKey string `envconfig:"LOCALAI_API_KEY"`
	// EmbedModel is the model id behind /v1/embeddings.
	EmbedModel string `envconfig:"EMBED_MODEL"`
	// ChatModel is the model id behind /v1/chat/completions.
	ChatModel string `envconfig:"CHAT_MODEL"`
}

// Load decodes Config from the environment, applying defaults for unset *and
// empty* variables. An empty value is treated as unset so seeding .env from
// example.env is always safe; the placeholder itself carries the defaults.
//
// The returned error never contains a value, only the variable name envconfig
// failed on.
func Load() (Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return Config{}, fmt.Errorf("environment configuration: %w", err)
	}

	cfg.DBPath = DefaultOr(cfg.DBPath, DefaultDBPath)
	cfg.LocalAIBaseURL = DefaultOr(cfg.LocalAIBaseURL, DefaultLocalAIBaseURL)
	cfg.EmbedModel = DefaultOr(cfg.EmbedModel, DefaultEmbedModel)
	cfg.ChatModel = DefaultOr(cfg.ChatModel, DefaultChatModel)
	// An empty token is a legitimate value, not a missing one.
	cfg.LocalAIAPIKey = strings.TrimSpace(cfg.LocalAIAPIKey)
	return cfg, nil
}

// LoadDotEnv loads DotEnvPath into the process environment. It is the one
// startup hook: main runs it before anything else decodes a variable, which is
// what makes a developer's .env visible to envconfig and to Lookup alike.
//
// A missing file is not an error - containers, cron, and launchd set the
// variables directly. An unreadable or malformed file is an error naming the
// file, never a value.
func LoadDotEnv() error {
	_, err := loadDotEnv(DotEnvPath)
	return err
}

// loadDotEnv reports whether path was loaded, so tests can distinguish the
// "no .env in this checkout" case from a real failure.
func loadDotEnv(path string) (bool, error) {
	err := godotenv.Load(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}

	// Forward only I/O failures. godotenv's parser errors echo the offending
	// line, which can hold a credential value; the file path and the kind of
	// failure are all a caller may see.
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return false, fmt.Errorf("load env file: %w", pathErr)
	}
	return false, fmt.Errorf("load env file %s: malformed content (check KEY=VALUE syntax and quoting)", path)
}

// Lookup returns a dynamically named variable and whether it is set. Only the
// credentials block of sources.yaml has dynamic names: any configured source
// may reference any variable, so those cannot be struct fields. Static
// settings never go through here; they come from Load or logging.ConfigFromEnv.
//
// Callers must still treat an empty value as unset where a credential is
// required; Lookup reports presence, not validity.
func Lookup(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	return v, ok
}

// DefaultOr returns def when v is blank, so `VAR=` in .env behaves like an
// unset variable instead of silently overriding the default with "". It also
// trims the result, so a stray space around a value cannot leak into a path or
// a URL. Every loader in the repo applies it after envconfig.Process, which
// cannot tell an unset variable from one set to the empty string.
func DefaultOr(v, def string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	return v
}
