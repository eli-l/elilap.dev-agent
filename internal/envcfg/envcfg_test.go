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
	unsetFor(t, EnvDBPath, EnvLocalAIBaseURL, EnvLocalAIAPIKey, EnvEmbedModel, EnvChatModel)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		DBPath:         DefaultDBPath,
		LocalAIBaseURL: DefaultLocalAIBaseURL,
		LocalAIAPIKey:  "",
		EmbedModel:     DefaultEmbedModel,
		ChatModel:      DefaultChatModel,
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv(EnvDBPath, "/data/index.db")
	t.Setenv(EnvLocalAIBaseURL, "http://localhost:9999/v1")
	t.Setenv(EnvLocalAIAPIKey, "token")
	t.Setenv(EnvEmbedModel, "other-embed")
	t.Setenv(EnvChatModel, "other-chat")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		DBPath:         "/data/index.db",
		LocalAIBaseURL: "http://localhost:9999/v1",
		LocalAIAPIKey:  "token",
		EmbedModel:     "other-embed",
		ChatModel:      "other-chat",
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

// TestLoadTreatsEmptyAsUnset pins the .env semantics: seeding .env from
// example.env leaves defaults untouched even when values are blank.
func TestLoadTreatsEmptyAsUnset(t *testing.T) {
	t.Setenv(EnvDBPath, "")
	t.Setenv(EnvLocalAIBaseURL, "")
	t.Setenv(EnvLocalAIAPIKey, "")
	t.Setenv(EnvEmbedModel, "")
	t.Setenv(EnvChatModel, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DBPath != DefaultDBPath || cfg.LocalAIBaseURL != DefaultLocalAIBaseURL ||
		cfg.EmbedModel != DefaultEmbedModel || cfg.ChatModel != DefaultChatModel {
		t.Errorf("Load() with empty variables = %+v, want the defaults", cfg)
	}
	if cfg.LocalAIAPIKey != "" {
		t.Errorf("LocalAIAPIKey = %q, want empty", cfg.LocalAIAPIKey)
	}
}

// TestConfigTagsMatchConstants keeps the struct tags and the exported names
// from drifting: example.env, docs, and code all quote the constants.
func TestConfigTagsMatchConstants(t *testing.T) {
	want := map[string]string{
		"DBPath":         EnvDBPath,
		"LocalAIBaseURL": EnvLocalAIBaseURL,
		"LocalAIAPIKey":  EnvLocalAIAPIKey,
		"EmbedModel":     EnvEmbedModel,
		"ChatModel":      EnvChatModel,
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
	t.Setenv(EnvLocalAIBaseURL, "\thttp://localhost:9999/v1\n")
	t.Setenv(EnvLocalAIAPIKey, "  token  ")
	t.Setenv(EnvEmbedModel, " other-embed ")
	t.Setenv(EnvChatModel, " other-chat ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		DBPath:         "/data/index.db",
		LocalAIBaseURL: "http://localhost:9999/v1",
		LocalAIAPIKey:  "token",
		EmbedModel:     "other-embed",
		ChatModel:      "other-chat",
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
