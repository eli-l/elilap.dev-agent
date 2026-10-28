// Package config loads and validates the static import configuration
// (sources.yaml).
//
// The configuration declares which import providers exist and how they are
// configured; it never holds secrets. Every credential is a { env: VAR }
// reference that is resolved from the process environment at runtime. See
// PLAN.md §2.6 and docs/sources-config.md for the authoritative schema.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"gopkg.in/yaml.v3"
)

// SupportedVersion is the only config schema version this loader accepts.
const SupportedVersion = 1

// Config is the whole sources.yaml document.
type Config struct {
	Version  int      `yaml:"version"`
	Defaults Defaults `yaml:"defaults"`
	Sources  []Source `yaml:"sources"`
}

// Defaults holds settings shared by every provider unless a provider
// overrides them.
type Defaults struct {
	Chunking  Chunking `yaml:"chunking"`
	UserAgent string   `yaml:"user_agent"`
}

// Chunking is the default chunk sizing policy, in tokens.
type Chunking struct {
	TargetTokens  int `yaml:"target_tokens"`
	OverlapTokens int `yaml:"overlap_tokens"`
}

// Source declares one configured provider instance. Options is kept as a
// raw YAML node because its schema depends on Type; decode it through
// OptionsInto.
type Source struct {
	ID          string      `yaml:"id"`
	Type        string      `yaml:"type"`
	Enabled     bool        `yaml:"enabled"`
	Credentials Credentials `yaml:"credentials"`
	Options     yaml.Node   `yaml:"options"`
}

// Credentials names the environment variables that carry a provider's
// endpoint and secret. Values are never stored in the config file.
type Credentials struct {
	APIBaseURL EnvRef `yaml:"api_url"`
	APIKey     EnvRef `yaml:"api_key"`
}

// EnvRef references an environment variable by name. An empty Env means the
// credential is not used by this provider.
type EnvRef struct {
	Env string `yaml:"env"`
}

// Load reads, strictly decodes, and validates the configuration at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse %s: %w", path, errors.New("empty config file"))
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate checks the document structurally: a known schema version, valid
// chunking defaults, a unique non-empty id and a non-empty type per source,
// and well-formed credential names. It never reads the environment.
func (c *Config) Validate() error {
	if c.Version != SupportedVersion {
		return fmt.Errorf("unsupported config version %d: supported version is %d", c.Version, SupportedVersion)
	}

	chunking := c.Defaults.Chunking
	if chunking.TargetTokens <= 0 {
		return fmt.Errorf("defaults.chunking.target_tokens must be greater than 0")
	}
	if chunking.OverlapTokens < 0 || chunking.OverlapTokens >= chunking.TargetTokens {
		return fmt.Errorf("defaults.chunking.overlap_tokens must be non-negative and less than target_tokens")
	}

	seen := make(map[string]bool, len(c.Sources))
	for i, src := range c.Sources {
		if src.ID == "" {
			return fmt.Errorf("sources[%d]: id is required", i)
		}
		if src.Type == "" {
			return fmt.Errorf("source %q: type is required", src.ID)
		}
		if seen[src.ID] {
			return fmt.Errorf("duplicate source id %q", src.ID)
		}
		seen[src.ID] = true

		refs := []struct {
			field string
			ref   EnvRef
		}{
			{"credentials.api_url", src.Credentials.APIBaseURL},
			{"credentials.api_key", src.Credentials.APIKey},
		}
		for _, r := range refs {
			if r.ref.Env == "" {
				continue
			}
			if !validEnvName(r.ref.Env) {
				return fmt.Errorf("source %q: %s: %q is not a valid environment variable name", src.ID, r.field, r.ref.Env)
			}
		}
	}
	return nil
}

// OptionsInto strict-decodes the source's raw options node into v, which
// must be a pointer to the provider's option struct. Unknown option keys and
// type mismatches are errors.
func (s Source) OptionsInto(v any) error {
	if s.Options.Kind == 0 || s.Options.Tag == "!!null" {
		return nil
	}

	raw, err := yaml.Marshal(&s.Options)
	if err != nil {
		return fmt.Errorf("source %q: re-encode options: %w", s.ID, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("invalid options: %w", err)
	}
	return nil
}

// Enabled returns the enabled sources in file order.
func (c *Config) Enabled() []Source {
	var out []Source
	for _, src := range c.Sources {
		if src.Enabled {
			out = append(out, src)
		}
	}
	return out
}

// MissingEnv returns the names - never the values - of the environment
// variables referenced by credentials that are not usable, in first-reference
// order and without duplicates: unset, or set to an empty/blank value. Empty
// counts as missing because a .env copied from example.env contains blank
// credential lines, and the credential resolver treats blank as missing too.
// It is the only function here that consults the environment; the lookup
// itself goes through envcfg, which is also what loads .env at startup.
func (c *Config) MissingEnv() []string {
	var missing []string
	seen := make(map[string]bool, 2*len(c.Sources))
	for _, src := range c.Sources {
		for _, ref := range []EnvRef{src.Credentials.APIBaseURL, src.Credentials.APIKey} {
			name := ref.Env
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			if v, ok := envcfg.Lookup(name); !ok || strings.TrimSpace(v) == "" {
				missing = append(missing, name)
			}
		}
	}
	return missing
}

// validEnvName reports whether name looks like a POSIX environment variable
// name: ASCII letters, digits, and underscores, not starting with a digit.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
