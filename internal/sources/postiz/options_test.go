package postiz_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/postiz"
)

// parseOptions loads a one-source config file with the given options block
// and runs the registered Postiz options parser over it.
func parseOptions(t *testing.T, options string) (*postiz.Options, error) {
	t.Helper()
	doc := "version: 1\ndefaults:\n  chunking: { target_tokens: 400, overlap_tokens: 60 }\nsources:\n  - id: postiz\n    type: postiz\n    enabled: true\n"
	if options != "" {
		doc += "    options: " + options + "\n"
	}
	path := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	parsed, err := postiz.ParseOptions(cfg.Sources[0])
	if err != nil {
		return nil, err
	}
	opts, ok := parsed.(*postiz.Options)
	if !ok {
		t.Fatalf("options type = %T, want *postiz.Options", parsed)
	}
	return opts, nil
}

func TestParseOptionsNormalizesPlatforms(t *testing.T) {
	opts, err := parseOptions(t, `{ platforms: [LinkedIn, " x ", linkedin], include_scheduled: true }`)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	if got, want := strings.Join(opts.Platforms, ","), "linkedin,x"; got != want {
		t.Errorf("platforms = %q, want %q (trimmed, lower-cased, de-duplicated)", got, want)
	}
	if !opts.IncludeScheduled {
		t.Error("include_scheduled = false, want true")
	}
}

func TestParseOptionsDefaultsToPublishedOnly(t *testing.T) {
	opts, err := parseOptions(t, `{ platforms: [linkedin] }`)
	if err != nil {
		t.Fatalf("ParseOptions() error = %v", err)
	}
	if opts.IncludeScheduled {
		t.Error("include_scheduled = true, want the zero value false")
	}
}

func TestParseOptionsRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		options string
		wantErr string
	}{
		{name: "missing platforms", options: `{ include_scheduled: false }`, wantErr: "options.platforms"},
		{name: "empty platforms", options: `{ platforms: [] }`, wantErr: "options.platforms"},
		{name: "blank platform", options: `{ platforms: [linkedin, "  "] }`, wantErr: "must not be blank"},
		{name: "unknown option key", options: `{ platforms: [linkedin], platform: x }`, wantErr: "platform"},
		{name: "wrong type", options: `{ platforms: linkedin }`, wantErr: "cannot unmarshal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseOptions(t, tc.options)
			if err == nil {
				t.Fatalf("ParseOptions() = nil error, want one containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseOptions() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseOptionsRequiresAnOptionsBlock(t *testing.T) {
	if _, err := parseOptions(t, ""); err == nil {
		t.Fatal("ParseOptions(no options) = nil error, want platforms to be required")
	}
}
