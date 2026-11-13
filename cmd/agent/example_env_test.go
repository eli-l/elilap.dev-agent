package main

import (
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/joho/godotenv"
)

// TestExampleEnvIsComplete enforces the repository rule that every environment
// variable the agent reads is listed in example.env with an empty value
// (secrets) or its default: a new variable lands in code and example.env in
// the same change. The Go inventory is derived from the envconfig struct tags
// and sources.yaml, so adding a field without listing it fails; a stale entry
// fails too. evals/ is a Python harness with no envconfig structs, so its
// EVAL_* variables are pinned explicitly below.
func TestExampleEnvIsComplete(t *testing.T) {
	values, err := godotenv.Read(filepath.Join("..", "..", "example.env"))
	if err != nil {
		t.Fatalf("read example.env: %v", err)
	}

	// Static settings: every envconfig field, keyed by its full variable name.
	static := make(map[string]bool)
	for _, name := range envTags(envcfg.Config{}) {
		static[name] = true
	}
	for _, name := range envTags(logging.Config{}) {
		static[logging.EnvPrefix+"_"+name] = true
	}

	// Values example.env must carry for settings that have a default.
	defaults := map[string]string{
		logging.EnvLevel:         logging.DefaultLevel,
		logging.EnvFormat:        logging.DefaultFormat,
		envcfg.EnvDBPath:         envcfg.DefaultDBPath,
		envcfg.EnvEmbedBaseURL:   envcfg.DefaultModelBaseURL,
		envcfg.EnvEmbedAPIKey:    "",
		envcfg.EnvEmbedModel:     envcfg.DefaultEmbedModel,
		envcfg.EnvEmbedDims:      strconv.Itoa(envcfg.DefaultEmbedDims),
		envcfg.EnvChatBaseURL:    envcfg.DefaultModelBaseURL,
		envcfg.EnvChatAPIKey:     "",
		envcfg.EnvChatModel:      envcfg.DefaultChatModel,
		envcfg.EnvChatTokenParam: envcfg.DefaultChatTokenParam,
		envcfg.EnvA2ATokens:      "",
		envcfg.EnvA2ATokensFile:  envcfg.DefaultA2ATokensFile,
		envcfg.EnvA2APublicURL:   "",
	}

	// evals/ (Python, uv) reads EVAL_* directly; no Go envconfig struct
	// declares them. Pin them here so the inventory stays complete in both
	// directions. Values must match evals/README.md and example.env.
	evalVars := map[string]string{
		"EVAL_BASE_URL": "http://localhost:8000/v1",
		"EVAL_MODEL":    "mtplx-qwen36-35b-a3b-optimized-speed",
		"EVAL_API_KEY":  "",
	}
	for name, value := range evalVars {
		static[name] = true
		defaults[name] = value
	}

	// Dynamic credential names come from sources.yaml; secrets stay blank.
	cfg, err := config.Load(filepath.Join("..", "..", "sources.yaml"))
	if err != nil {
		t.Fatalf("load sources.yaml: %v", err)
	}
	credentials := make(map[string]bool)
	for _, src := range cfg.Sources {
		for _, ref := range []config.EnvRef{src.Credentials.APIBaseURL, src.Credentials.APIKey} {
			if ref.Env != "" {
				credentials[ref.Env] = true
			}
		}
	}

	known := make(map[string]bool, len(static)+len(credentials))
	for name := range static {
		known[name] = true
	}
	for name := range credentials {
		known[name] = true
	}

	for name := range known {
		got, ok := values[name]
		if !ok {
			t.Errorf("example.env is missing %s", name)
			continue
		}
		if want, ok := defaults[name]; ok && got != want {
			t.Errorf("example.env %s = %q, want %q", name, got, want)
		}
		if credentials[name] && got != "" {
			t.Errorf("example.env %s = %q, want it empty (never commit a secret)", name, got)
		}
	}
	for name := range values {
		if !known[name] {
			t.Errorf("example.env lists %s, which no struct tag or configured credential reads", name)
		}
	}
}

// envTags returns the envconfig tags of an exported struct's fields.
func envTags(spec any) []string {
	typ := reflect.TypeOf(spec)
	tags := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		if tag := typ.Field(i).Tag.Get("envconfig"); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}
