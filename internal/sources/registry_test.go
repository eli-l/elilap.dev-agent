package sources_test

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources"
	"github.com/eli-l/elilap.dev-agent/internal/sources/ghost"
	"github.com/eli-l/elilap.dev-agent/internal/sources/postiz"
)

// source parses an inline source YAML fragment into a config.Source.
func source(t *testing.T, doc string) config.Source {
	t.Helper()
	var src config.Source
	dec := yaml.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.KnownFields(true)
	if err := dec.Decode(&src); err != nil {
		t.Fatalf("decode fixture source: %v", err)
	}
	return src
}

func TestRegistryKnownTypes(t *testing.T) {
	r := sources.NewRegistry()
	if err := sources.RegisterBuiltins(r); err != nil {
		t.Fatalf("RegisterBuiltins() error = %v", err)
	}

	got := strings.Join(r.KnownTypes(), ",")
	if want := "files,ghost,postiz"; got != want {
		t.Errorf("KnownTypes() = %q, want %q", got, want)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	r := sources.NewRegistry()
	if err := r.Register("ghost", ghost.ParseOptions); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	err := r.Register("ghost", ghost.ParseOptions)
	if err == nil {
		t.Fatal("second Register() = nil error, want a duplicate-type error")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("Register() error = %q, want it to mention an existing registration", err)
	}
}

func TestParseOptions(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		wantErr  string
		wantType string
		check    func(t *testing.T, opts any)
	}{
		{
			name: "ghost options parse",
			doc: `
id: ghost
type: ghost
enabled: true
options: { include_drafts: false, limit: 500 }
`,
			wantType: "ghost",
			check: func(t *testing.T, opts any) {
				got, ok := opts.(*ghost.Options)
				if !ok {
					t.Fatalf("options type = %T, want *ghost.Options", opts)
				}
				if got.Limit != 500 || got.IncludeDrafts {
					t.Errorf("options = %+v, want limit 500 and drafts excluded", got)
				}
			},
		},
		{
			name: "postiz options parse",
			doc: `
id: postiz
type: postiz
enabled: true
options: { platforms: [instagram, linkedin, x], include_scheduled: false }
`,
			wantType: "postiz",
			check: func(t *testing.T, opts any) {
				got, ok := opts.(*postiz.Options)
				if !ok {
					t.Fatalf("options type = %T, want *postiz.Options", opts)
				}
				if len(got.Platforms) != 3 || got.Platforms[0] != "instagram" {
					t.Errorf("platforms = %v, want instagram, linkedin, x", got.Platforms)
				}
				if got.IncludeScheduled {
					t.Error("IncludeScheduled = true, want false")
				}
			},
		},
		{
			name: "ghost rejects limit zero",
			doc: `
id: ghost
type: ghost
options: { limit: 0 }
`,
			wantErr: "options.limit",
		},
		{
			name: "ghost rejects unknown option key",
			doc: `
id: ghost
type: ghost
options: { limit: 10, typo_limit: 5 }
`,
			wantErr: "typo_limit",
		},
		{
			name: "postiz rejects empty platforms",
			doc: `
id: postiz
type: postiz
options: { platforms: [] }
`,
			wantErr: "options.platforms",
		},
		{
			name: "postiz rejects unknown option key",
			doc: `
id: postiz
type: postiz
options: { platforms: [x], mystery: true }
`,
			wantErr: "mystery",
		},
		{
			name:    "unknown type lists known types",
			doc:     "id: site\ntype: website\n",
			wantErr: "known types: [files ghost postiz]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := sources.NewRegistry()
			if err := sources.RegisterBuiltins(r); err != nil {
				t.Fatalf("RegisterBuiltins() error = %v", err)
			}

			opts, err := r.ParseOptions(source(t, tc.doc))

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseOptions() = nil error, want error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseOptions() error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseOptions() error = %v", err)
			}
			if tc.check != nil {
				tc.check(t, opts)
			}
		})
	}
}

func TestValidateAll(t *testing.T) {
	parse := func(t *testing.T, doc string) *config.Config {
		t.Helper()
		var cfg config.Config
		if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
			t.Fatalf("decode fixture config: %v", err)
		}
		return &cfg
	}

	r := sources.NewRegistry()
	if err := sources.RegisterBuiltins(r); err != nil {
		t.Fatalf("RegisterBuiltins() error = %v", err)
	}

	t.Run("all valid", func(t *testing.T) {
		cfg := parse(t, "version: 1\nsources:\n  - id: ghost\n    type: ghost\n    options: { limit: 100 }\n")
		if err := r.ValidateAll(cfg); err != nil {
			t.Fatalf("ValidateAll() error = %v, want nil", err)
		}
	})

	t.Run("collects every error", func(t *testing.T) {
		cfg := parse(t, `version: 1
sources:
  - id: ghost
    type: ghost
    options: { limit: 0 }
  - id: postiz
    type: postiz
    options: { platforms: [] }
  - id: site
    type: website
`)
		err := r.ValidateAll(cfg)
		if err == nil {
			t.Fatal("ValidateAll() = nil error, want the collected errors")
		}
		for _, want := range []string{"options.limit", "options.platforms", "website"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ValidateAll() error = %q, want it to contain %q", err, want)
			}
		}
	})
}
