// Package files is the local-markdown provider: it reads a directory of
// markdown files and presents them as upstream items.
//
// It exists for two reasons. The first is practical: the Ghost and Postiz
// clients still wait on the Phase 0 API captures, so without a provider that
// needs no credentials there is no way to run the ingest -> embed -> query
// pipeline at all. The second is that it exercises the claim in PLAN.md -
// adding a provider is one sources.yaml entry plus one package - with a
// provider that actually ships.
package files

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// Type is the provider type name used in sources.yaml.
const Type = "files"

// SourceKind is the source_kind recorded for the items this provider
// returns. The chunk id is derived from it, so it is part of the identity
// of everything this provider writes into the index.
const SourceKind = "file_post"

// DefaultPattern is the glob used when the config does not name one.
const DefaultPattern = "*.md"

// Options is the files provider's `options:` block in sources.yaml.
type Options struct {
	// Dir is the directory to read markdown files from. It is required.
	Dir string `yaml:"dir"`
	// Pattern is the glob that selects the files inside Dir. It is
	// optional and defaults to DefaultPattern.
	Pattern string `yaml:"pattern"`
}

// ParseOptions strict-decodes and validates a source's options for the files
// provider. It is registered in the sources registry as type "files".
func ParseOptions(cfg config.Source) (any, error) {
	var o Options
	if err := cfg.OptionsInto(&o); err != nil {
		return nil, err
	}

	o.Dir = strings.TrimSpace(o.Dir)
	if o.Dir == "" {
		return nil, errors.New("options.dir is required: the directory of markdown files to index")
	}

	o.Pattern = strings.TrimSpace(o.Pattern)
	if o.Pattern == "" {
		o.Pattern = DefaultPattern
	}
	if strings.ContainsRune(o.Pattern, filepath.Separator) || strings.Contains(o.Pattern, "/") {
		return nil, fmt.Errorf("options.pattern %q must be a file name glob inside options.dir, not a path", o.Pattern)
	}

	return &o, nil
}
