package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes content into a temp dir and returns the path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

const validConfig = `version: 1

defaults:
  chunking: { target_tokens: 400, overlap_tokens: 60 }
  user_agent: "elilap-dev-agent/0.1 (+https://elilap.dev)"

sources:
  - id: ghost
    type: ghost
    enabled: true
    credentials:
      api_url: { env: GHOST_API_URL }
      api_key: { env: GHOST_API_KEY }
    options: { include_drafts: false, limit: 500 }

  - id: postiz
    type: postiz
    enabled: false
    credentials:
      api_url: { env: POSTIZ_API_URL }
      api_key: { env: POSTIZ_API_KEY }
    options: { platforms: [instagram, linkedin, x], include_scheduled: false }
`

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr string
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "valid file loads",
			file: validConfig,
			check: func(t *testing.T, cfg *Config) {
				if cfg.Version != 1 {
					t.Errorf("Version = %d, want 1", cfg.Version)
				}
				if cfg.Defaults.Chunking.TargetTokens != 400 || cfg.Defaults.Chunking.OverlapTokens != 60 {
					t.Errorf("Chunking = %+v, want 400/60", cfg.Defaults.Chunking)
				}
				if len(cfg.Sources) != 2 {
					t.Fatalf("len(Sources) = %d, want 2", len(cfg.Sources))
				}
				if got := cfg.Sources[0]; got.ID != "ghost" || got.Type != "ghost" || !got.Enabled {
					t.Errorf("Sources[0] = %+v, want an enabled ghost source", got)
				}
				if got := cfg.Sources[0].Credentials.APIKey.Env; got != "GHOST_API_KEY" {
					t.Errorf("api_key env = %q, want GHOST_API_KEY", got)
				}
				enabled := cfg.Enabled()
				if len(enabled) != 1 || enabled[0].ID != "ghost" {
					t.Errorf("Enabled() = %+v, want just ghost", enabled)
				}
			},
		},
		{
			name:    "unknown top-level key errors",
			file:    validConfig + "\nbogus_key: 1\n",
			wantErr: "bogus_key",
		},
		{
			name: "unknown key inside a source errors",
			file: `version: 1
sources:
  - id: ghost
    type: ghost
    enabled: true
    typo_option: true
`,
			wantErr: "typo_option",
		},
		{
			name:    "unknown version errors",
			file:    strings.Replace(validConfig, "version: 1", "version: 2", 1),
			wantErr: "version 2",
		},
		{
			name:    "missing version errors",
			file:    strings.Replace(validConfig, "version: 1\n\n", "", 1),
			wantErr: "version",
		},
		{
			name: "duplicate ids error",
			file: `version: 1
sources:
  - id: ghost
    type: ghost
    enabled: true
  - id: ghost
    type: postiz
    enabled: true
`,
			wantErr: "duplicate source id",
		},
		{
			name: "empty id errors",
			file: `version: 1
sources:
  - type: ghost
    enabled: true
`,
			wantErr: "id is required",
		},
		{
			name: "empty type errors",
			file: `version: 1
sources:
  - id: ghost
    enabled: true
`,
			wantErr: "type is required",
		},
		{
			name: "malformed env name errors",
			file: `version: 1
sources:
  - id: ghost
    type: ghost
    credentials:
      api_key: { env: "GHOST API KEY" }
`,
			wantErr: "not a valid environment variable name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.file))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() = nil error, want error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load() = nil error for a missing file, want an error")
	}
}

func TestOptionsInto(t *testing.T) {
	type ghostOptions struct {
		IncludeDrafts bool `yaml:"include_drafts"`
		Limit         int  `yaml:"limit"`
	}

	tests := []struct {
		name    string
		options string
		wantErr string
		want    ghostOptions
	}{
		{
			name:    "known keys decode",
			options: "{ include_drafts: true, limit: 25 }",
			want:    ghostOptions{IncludeDrafts: true, Limit: 25},
		},
		{
			name:    "absent options are allowed",
			options: "",
		},
		{
			name:    "unknown option key is rejected",
			options: "{ include_drafts: false, limit: 25, typo: 1 }",
			wantErr: "typo",
		},
		{
			name:    "wrong value type is rejected",
			options: "{ limit: many }",
			wantErr: "cannot unmarshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := "version: 1\nsources:\n  - id: ghost\n    type: ghost\n    enabled: true\n"
			if tc.options != "" {
				file += "    options: " + tc.options + "\n"
			}
			cfg, err := Load(writeConfig(t, file))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			var got ghostOptions
			err = cfg.Sources[0].OptionsInto(&got)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("OptionsInto() = nil error, want error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("OptionsInto() error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("OptionsInto() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("OptionsInto() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMissingEnv(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	const setVar = "CONFIG_TEST_SET_VAR"
	const unsetVar = "CONFIG_TEST_UNSET_VAR"
	t.Setenv(setVar, "a-value-that-must-not-appear")

	cfg.Sources = []Source{
		{ID: "set", Type: "ghost", Credentials: Credentials{APIKey: EnvRef{Env: setVar}}},
		{ID: "unset", Type: "ghost", Credentials: Credentials{APIKey: EnvRef{Env: unsetVar}}},
	}

	unsetEnv(t, unsetVar)

	missing := cfg.MissingEnv()
	if len(missing) != 1 {
		t.Fatalf("MissingEnv() = %v, want exactly one entry", missing)
	}
	if missing[0] != unsetVar {
		t.Errorf("MissingEnv()[0] = %q, want the env var name %q", missing[0], unsetVar)
	}
	if strings.Contains(strings.Join(missing, " "), "a-value-that-must-not-appear") {
		t.Error("MissingEnv() leaked a credential value")
	}
}

// unsetEnv removes name for the duration of the test, restoring the previous
// state afterwards.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
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
