// Package ghost holds the Ghost Admin API provider's configuration. The
// network client waits on the Phase 0 API capture in docs/ghost.md.
package ghost

import (
	"errors"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// Options is the Ghost provider's `options:` block in sources.yaml.
type Options struct {
	// IncludeDrafts also imports posts that are still drafts.
	IncludeDrafts bool `yaml:"include_drafts"`
	// Limit caps how many posts a single list request asks for.
	Limit int `yaml:"limit"`
}

// ParseOptions strict-decodes and validates a source's options for the Ghost
// provider. It is registered in the sources registry as type "ghost".
func ParseOptions(cfg config.Source) (any, error) {
	var o Options
	if err := cfg.OptionsInto(&o); err != nil {
		return nil, err
	}
	if o.Limit <= 0 {
		return nil, errors.New("options.limit must be greater than 0")
	}
	return &o, nil
}
