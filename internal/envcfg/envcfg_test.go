package envcfg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// unsetFor removes names for the duration of the test and restores them
// afterwards, so a developer's exported variables cannot skew the assertions.
func unsetFor(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		prev, ok := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(name, prev)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	unsetFor(t, EnvDBPath, EnvEmbedBaseURL, EnvEmbedAPIKey, EnvEmbedModel, EnvEmbedDims,
		EnvChatBaseURL, EnvChatAPIKey, EnvChatModel, EnvChatTokenParam,
		EnvA2ATokens, EnvA2ATokensFile, EnvA2APublicURL)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		DBPath:         DefaultDBPath,
		EmbedBaseURL:   DefaultModelBaseURL,
		EmbedModel:     DefaultEmbedModel,
		EmbedDims:      DefaultEmbedDims,
		ChatBaseURL:    DefaultModelBaseURL,
		ChatModel:      DefaultChatModel,
		ChatTokenParam: DefaultChatTokenParam,
		A2ATokensFile:  DefaultA2ATokensFile,
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

// TestLoadReadsEnvironment pins the independent endpoints: one role may live
// on a cloud provider while the other stays on the local host.
func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv(EnvDBPath, "/data/index.db")
	t.Setenv(EnvEmbedBaseURL, "http://localhost:9999/v1")
	t.Setenv(EnvEmbedAPIKey, "embed-token")
	t.Setenv(EnvEmbedModel, "other-embed")
	t.Setenv(EnvEmbedDims, "1536")
	t.Setenv(EnvChatBaseURL, "https://api.example/v1")
	t.Setenv(EnvChatAPIKey, "chat-token")
	t.Setenv(EnvChatModel, "gpt6-luna")
	t.Setenv(EnvChatTokenParam, "max_completion_tokens")
	t.Setenv(EnvA2ATokens, "alice:"+strings.Repeat("a", 32))
	t.Setenv(EnvA2ATokensFile, "./tokens.txt")
	t.Setenv(EnvA2APublicURL, "https://agent.example/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		DBPath:         "/data/index.db",
		EmbedBaseURL:   "http://localhost:9999/v1",
		EmbedAPIKey:    "embed-token",
		EmbedModel:     "other-embed",
		EmbedDims:      1536,
		ChatBaseURL:    "https://api.example/v1",
		ChatAPIKey:     "chat-token",
		ChatModel:      "gpt6-luna",
		ChatTokenParam: "max_completion_tokens",
		A2ATokens:      "alice:" + strings.Repeat("a", 32),
		A2ATokensFile:  "./tokens.txt",
		A2APublicURL:   "https://agent.example",
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

// TestLoadTreatsEmptyAsUnset pins the .env semantics: seeding .env from
// example.env leaves defaults untouched even when values are blank.
func TestLoadTreatsEmptyAsUnset(t *testing.T) {
	t.Setenv(EnvDBPath, "")
	t.Setenv(EnvEmbedBaseURL, "")
	t.Setenv(EnvEmbedAPIKey, "")
	t.Setenv(EnvEmbedModel, "")
	t.Setenv(EnvEmbedDims, "")
	t.Setenv(EnvChatBaseURL, "")
	t.Setenv(EnvChatAPIKey, "")
	t.Setenv(EnvChatModel, "")
	t.Setenv(EnvChatTokenParam, "")
	t.Setenv(EnvA2ATokens, "")
	t.Setenv(EnvA2ATokensFile, "")
	t.Setenv(EnvA2APublicURL, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DBPath != DefaultDBPath || cfg.EmbedBaseURL != DefaultModelBaseURL ||
		cfg.EmbedModel != DefaultEmbedModel || cfg.EmbedDims != DefaultEmbedDims {
		t.Errorf("Load() with empty variables = %+v, want the model defaults", cfg)
	}
	if cfg.ChatBaseURL != DefaultModelBaseURL || cfg.ChatModel != DefaultChatModel ||
		cfg.ChatTokenParam != DefaultChatTokenParam {
		t.Errorf("Load() with empty variables = %+v, want the chat defaults", cfg)
	}
	if cfg.A2ATokens != "" || cfg.A2APublicURL != "" {
		t.Errorf("A2ATokens/A2APublicURL = %q/%q, want empty", cfg.A2ATokens, cfg.A2APublicURL)
	}
	if cfg.EmbedAPIKey != "" || cfg.ChatAPIKey != "" {
		t.Errorf("EmbedAPIKey/ChatAPIKey = %q/%q, want empty", cfg.EmbedAPIKey, cfg.ChatAPIKey)
	}
}

// TestLoadRejectsBadTokenParam keeps a typo from reaching the model backend,
// where it would surface as a 400 in the middle of an answer.
func TestLoadRejectsBadTokenParam(t *testing.T) {
	unsetFor(t, EnvChatTokenParam)
	t.Setenv(EnvChatTokenParam, "max-tokens")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an unknown CHAT_TOKEN_PARAM")
	} else if !strings.Contains(err.Error(), EnvChatTokenParam) {
		t.Errorf("error = %q, want it to name %s", err, EnvChatTokenParam)
	}
}

// TestConfigTagsMatchConstants keeps the struct tags and the exported names
// from drifting: example.env, docs, and code all quote the constants.
func TestConfigTagsMatchConstants(t *testing.T) {
	want := map[string]string{
		"DBPath":         EnvDBPath,
		"EmbedBaseURL":   EnvEmbedBaseURL,
		"EmbedAPIKey":    EnvEmbedAPIKey,
		"EmbedModel":     EnvEmbedModel,
		"EmbedDims":      EnvEmbedDims,
		"ChatBaseURL":    EnvChatBaseURL,
		"ChatAPIKey":     EnvChatAPIKey,
		"ChatModel":      EnvChatModel,
		"ChatTokenParam": EnvChatTokenParam,
		"A2ATokens":      EnvA2ATokens,
		"A2ATokensFile":  EnvA2ATokensFile,
		"A2APublicURL":   EnvA2APublicURL,
	}
	typ := reflect.TypeOf(Config{})
	if typ.NumField() != len(want) {
		t.Fatalf("Config has %d fields, want %d", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		got := f.Tag.Get("envconfig")
		if got != want[f.Name] {
			t.Errorf("field %s: envconfig tag = %q, want %q", f.Name, got, want[f.Name])
		}
	}
}

// TestLoadTrimsValues keeps stray whitespace from leaking into a path or URL;
// DefaultOr is the single normalization point.
func TestLoadTrimsValues(t *testing.T) {
	t.Setenv(EnvDBPath, "  /data/index.db  ")
	t.Setenv(EnvEmbedBaseURL, "\thttp://localhost:9999/v1\n")
	t.Setenv(EnvEmbedAPIKey, "  embed-token  ")
	t.Setenv(EnvEmbedModel, " other-embed ")
	t.Setenv(EnvEmbedDims, " 1536 ")
	t.Setenv(EnvChatBaseURL, "\thttps://api.example/v1\n")
	t.Setenv(EnvChatAPIKey, "  chat-token  ")
	t.Setenv(EnvChatModel, " gpt6-luna ")
	t.Setenv(EnvChatTokenParam, " max_completion_tokens ")
	t.Setenv(EnvA2ATokens, "  ")
	t.Setenv(EnvA2ATokensFile, "  ./tokens.txt  ")
	t.Setenv(EnvA2APublicURL, "  https://agent.example///  ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		DBPath:         "/data/index.db",
		EmbedBaseURL:   "http://localhost:9999/v1",
		EmbedAPIKey:    "embed-token",
		EmbedModel:     "other-embed",
		EmbedDims:      1536,
		ChatBaseURL:    "https://api.example/v1",
		ChatAPIKey:     "chat-token",
		ChatModel:      "gpt6-luna",
		ChatTokenParam: "max_completion_tokens",
		A2ATokensFile:  "./tokens.txt",
		A2APublicURL:   "https://agent.example",
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadDotEnv(t *testing.T) {
	const (
		loaded = "ENVCFG_TEST_LOADED"
		real   = "ENVCFG_TEST_REAL_WINS"
	)
	unsetFor(t, loaded)
	t.Setenv(real, "from-process")

	path := writeEnvFile(t, loaded+"=from-file\n"+real+"=from-file\n")
	ok, err := loadDotEnv(path)
	if err != nil {
		t.Fatalf("loadDotEnv() error = %v", err)
	}
	if !ok {
		t.Error("loadDotEnv() reported no file loaded")
	}
	if got := os.Getenv(loaded); got != "from-file" {
		t.Errorf("%s = %q, want from-file", loaded, got)
	}
	if got := os.Getenv(real); got != "from-process" {
		t.Errorf("%s = %q, want the real environment to win", real, got)
	}
}

func TestLoadDotEnvMissingFileIsFine(t *testing.T) {
	ok, err := loadDotEnv(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("loadDotEnv() error = %v, want nil", err)
	}
	if ok {
		t.Error("loadDotEnv() reported a missing file as loaded")
	}
}

func TestLoadDotEnvUnreadableFileFails(t *testing.T) {
	dir := t.TempDir()
	_, err := loadDotEnv(dir) // a directory, not a file
	if err == nil {
		t.Fatal("loadDotEnv() error = nil, want a read error")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %q, want it to name %q", err, dir)
	}
}

func TestLoadDotEnvMalformedFileFails(t *testing.T) {
	// godotenv's parser echoes the offending line, so a malformed credential
	// line must not reach the caller (main prints the error).
	path := writeEnvFile(t, "GHOST_API_KEY=\"s3cret-unterminated\n")
	_, err := loadDotEnv(path)
	if err == nil {
		t.Fatal("loadDotEnv() error = nil, want a parse error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name %q", err, path)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaked the malformed line: %q", err)
	}
}

func TestLookup(t *testing.T) {
	const name = "ENVCFG_TEST_LOOKUP"
	unsetFor(t, name)

	if _, ok := Lookup(name); ok {
		t.Fatalf("Lookup(%s) reported a value before it was set", name)
	}
	t.Setenv(name, "value")
	v, ok := Lookup(name)
	if !ok || v != "value" {
		t.Errorf("Lookup(%s) = %q, %v; want value, true", name, v, ok)
	}
}
