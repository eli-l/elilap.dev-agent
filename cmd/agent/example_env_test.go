package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/joho/godotenv"
)

// TestExampleEnvIsComplete enforces the repository rule that every environment
// variable the agent reads is listed in example.env with an empty value
// (secrets) or its default: a new variable lands in code and example.env in
// the same change. The inventory is derived from the envconfig struct tags and
// sources.yaml, so adding a field without listing it fails; a stale entry
// fails too.
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
		envcfg.EnvLocalAIBaseURL: envcfg.DefaultLocalAIBaseURL,
		envcfg.EnvLocalAIAPIKey:  "",
		envcfg.EnvEmbedModel:     envcfg.DefaultEmbedModel,
		envcfg.EnvChatModel:      envcfg.DefaultChatModel,
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
