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
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"

	"github.com/eli-l/elilap.dev-agent/internal/llm"
)

// DotEnvPath is the file LoadDotEnv reads, relative to the working directory.
const DotEnvPath = ".env"

// EnvPrefix prefixes the knobs this service owns. The model-backend and
// credential variables keep upstream-style names (EMBED_*, CHAT_*, GHOST_*,
// POSTIZ_*) instead of growing an AGENT_ prefix.
const EnvPrefix = "AGENT"

// Environment variable names. Credential names are not listed here: they are
// declared per source in sources.yaml and resolved by Lookup.
const (
	// EnvDBPath is the single-file SQLite index location.
	EnvDBPath = EnvPrefix + "_DB_PATH"
	// EnvEmbedBaseURL is the OpenAI-compatible base URL that serves
	// /embeddings, with the /v1 suffix. It is independent of EnvChatBaseURL:
	// embeddings may stay local while the chat model is a cloud provider.
	EnvEmbedBaseURL = "EMBED_BASE_URL"
	// EnvEmbedAPIKey is the optional bearer token for the embeddings endpoint.
	EnvEmbedAPIKey = "EMBED_API_KEY"
	// EnvEmbedModel is the embedding model id.
	EnvEmbedModel = "EMBED_MODEL"
	// EnvEmbedDims is the vector width that model returns (chunks_vec width).
	EnvEmbedDims = "EMBED_DIMS"
	// EnvChatBaseURL is the OpenAI-compatible base URL that serves
	// /chat/completions, with the /v1 suffix. Independent of the embeddings
	// endpoint, so a local embedder can pair with a cloud chat model.
	EnvChatBaseURL = "CHAT_BASE_URL"
	// EnvChatAPIKey is the optional bearer token for the chat endpoint.
	EnvChatAPIKey = "CHAT_API_KEY"
	// EnvChatModel is the chat model id used by the agent loop.
	EnvChatModel = "CHAT_MODEL"
	// EnvChatTokenParam selects the completion-budget field: max_tokens for
	// LocalAI and most compatible servers, max_completion_tokens for OpenAI's
	// reasoning models, which reject max_tokens.
	EnvChatTokenParam = "CHAT_TOKEN_PARAM"
	// EnvA2ATokens is the optional comma-separated `id:token` bearer list for
	// the A2A surface. Empty means the loopback dev server is token-less.
	EnvA2ATokens = EnvPrefix + "_A2A_TOKENS"
	// EnvA2ATokensFile is the optional 0600 token file, re-read when it
	// changes so `agent tokens add|revoke` needs no restart.
	EnvA2ATokensFile = EnvPrefix + "_A2A_TOKENS_FILE"
	// EnvA2APublicURL is the absolute service root the agent card advertises.
	// Empty derives it from each request (Host plus scheme).
	EnvA2APublicURL = EnvPrefix + "_A2A_PUBLIC_URL"
)

// Defaults for the settings above. They are exported so callers, docs, and
// tests agree with the one definition.
const (
	// DefaultDBPath is where the index lives in a host run. The container
	// sets /data/index.db explicitly (PLAN.md Decision 11); both paths keep
	// the DB and its WAL/SHM siblings inside the runtime data directory.
	DefaultDBPath = "./data/index.db"
	// DefaultModelBaseURL is the verified self-hosted instance that serves both
	// roles (Decision 10). An empty EMBED_BASE_URL or CHAT_BASE_URL falls back
	// to it, so one variable can still move both without naming each.
	DefaultModelBaseURL = "http://192.168.78.243:8080/v1"
	// DefaultEmbedModel produces the 768-dim vectors chunks_vec stores.
	DefaultEmbedModel = "embeddinggemma-300m"
	// DefaultEmbedDims is the vector width of the default embedder, and the
	// width migrations/0001_init.sql declares.
	DefaultEmbedDims = 768
	// DefaultChatModel is the reasoning model the agent loop calls. The 4B q8
	// model is the current host baseline; see docs/localai.md.
	DefaultChatModel = "Spark-X2.6-4B-q8_0"
	// DefaultChatTokenParam is the OpenAI-compatible completion-budget field;
	// only an OpenAI reasoning endpoint needs max_completion_tokens.
	DefaultChatTokenParam = llm.FieldMaxTokens
	// DefaultA2ATokensFile is the local token file `agent tokens` edits. It
	// lives with the other runtime data and is gitignored; a missing file
	// simply means no tokens are configured.
	DefaultA2ATokensFile = "./data/a2a-tokens.txt"
)

// EmbedWidth is the vector width of the embeddings model (EMBED_DIMS). It
// decodes from a string so a blank value behaves like an unset variable:
// envconfig cannot turn "" into an int, and a .env copied from example.env
// ships the variable blank. Load normalizes zero to DefaultEmbedDims.
type EmbedWidth int

// Decode implements envconfig.Decoder. Values that are not whole numbers are
// rejected here, which keeps the failure at startup and names the variable.
func (w *EmbedWidth) Decode(value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		*w = 0
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("must be a whole number")
	}
	*w = EmbedWidth(n)
	return nil
}

// Config is every static runtime setting, decoded by envconfig from the
// environment (and therefore, once main has run, from .env as well).
//
// The envconfig tag is the full variable name because this surface mixes
// prefixes; the exported Env* constants are the source of truth and a test
// pins the tags to them.
type Config struct {
	// DBPath is the SQLite index file (migrations are applied to it on open).
	DBPath string `envconfig:"AGENT_DB_PATH"`
	// EmbedBaseURL is the OpenAI-compatible endpoint serving /embeddings.
	EmbedBaseURL string `envconfig:"EMBED_BASE_URL"`
	// EmbedAPIKey is optional; empty means the endpoint needs no token.
	EmbedAPIKey string `envconfig:"EMBED_API_KEY"`
	// EmbedModel is the model id behind /embeddings.
	EmbedModel string `envconfig:"EMBED_MODEL"`
	// EmbedDims is the vector width that model returns; chunks_vec is rebuilt
	// when it changes.
	EmbedDims EmbedWidth `envconfig:"EMBED_DIMS"`
	// ChatBaseURL is the OpenAI-compatible endpoint serving /chat/completions.
	ChatBaseURL string `envconfig:"CHAT_BASE_URL"`
	// ChatAPIKey is optional; empty means the endpoint needs no token.
	ChatAPIKey string `envconfig:"CHAT_API_KEY"`
	// ChatModel is the model id behind /chat/completions.
	ChatModel string `envconfig:"CHAT_MODEL"`
	// ChatTokenParam is the completion-budget field the chat endpoint accepts
	// (llm.FieldMaxTokens or llm.FieldMaxCompletionTokens).
	ChatTokenParam string `envconfig:"CHAT_TOKEN_PARAM"`
	// A2ATokens is the comma-separated `id:token` list for the A2A surface.
	A2ATokens string `envconfig:"AGENT_A2A_TOKENS"`
	// A2ATokensFile is the 0600 token file the store re-reads on change.
	A2ATokensFile string `envconfig:"AGENT_A2A_TOKENS_FILE"`
	// A2APublicURL is the card's advertised service root; empty derives it
	// from the request.
	A2APublicURL string `envconfig:"AGENT_A2A_PUBLIC_URL"`
}

// Load decodes Config from the environment, applying defaults for unset *and
// empty* variables. An empty value is treated as unset so seeding .env from
// example.env is always safe; the placeholder itself carries the defaults.
//
// The error names the variable it failed on. Decode failures can quote the
// offending value, which is safe here: every field on this struct is a
// non-secret setting. Credentials are not fields - sources.yaml references
// them by name and Lookup reads them (see Lookup).
func Load() (Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return Config{}, fmt.Errorf("environment configuration: %w", err)
	}

	cfg.DBPath = DefaultOr(cfg.DBPath, DefaultDBPath)
	cfg.EmbedBaseURL = DefaultOr(cfg.EmbedBaseURL, DefaultModelBaseURL)
	cfg.ChatBaseURL = DefaultOr(cfg.ChatBaseURL, DefaultModelBaseURL)
	cfg.EmbedModel = DefaultOr(cfg.EmbedModel, DefaultEmbedModel)
	cfg.ChatModel = DefaultOr(cfg.ChatModel, DefaultChatModel)
	cfg.ChatTokenParam = DefaultOr(cfg.ChatTokenParam, DefaultChatTokenParam)
	if cfg.EmbedDims <= 0 {
		cfg.EmbedDims = DefaultEmbedDims
	}
	cfg.A2ATokensFile = DefaultOr(cfg.A2ATokensFile, DefaultA2ATokensFile)
	// The token list and public URL are legitimately empty; trimming keeps a
	// stray space out of the pair syntax and the advertised URL.
	cfg.A2ATokens = strings.TrimSpace(cfg.A2ATokens)
	cfg.A2APublicURL = strings.TrimRight(strings.TrimSpace(cfg.A2APublicURL), "/")
	// An empty API key or token param value is a legitimate value, not a
	// missing one.
	cfg.EmbedAPIKey = strings.TrimSpace(cfg.EmbedAPIKey)
	cfg.ChatAPIKey = strings.TrimSpace(cfg.ChatAPIKey)
	// A typo here would otherwise surface as a 400 from the model backend in
	// the middle of a run, so it fails at startup instead.
	if cfg.ChatTokenParam != llm.FieldMaxTokens && cfg.ChatTokenParam != llm.FieldMaxCompletionTokens {
		return Config{}, fmt.Errorf("environment configuration: %s must be %q or %q",
			EnvChatTokenParam, llm.FieldMaxTokens, llm.FieldMaxCompletionTokens)
	}
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
