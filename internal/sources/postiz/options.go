// Package postiz holds the Postiz API provider's configuration. The network
// client waits on the Phase 0 API capture in docs/postiz.md.
package postiz

import (
	"errors"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// Options is the Postiz provider's `options:` block in sources.yaml.
type Options struct {
	// Platforms restricts ingestion to these social platforms.
	Platforms []string `yaml:"platforms"`
	// IncludeScheduled also imports posts whose status is scheduled.
	IncludeScheduled bool `yaml:"include_scheduled"`
}

// ParseOptions strict-decodes and validates a source's options for the
// Postiz provider. It is registered in the sources registry as type
// "postiz".
func ParseOptions(cfg config.Source) (any, error) {
	var o Options
	if err := cfg.OptionsInto(&o); err != nil {
		return nil, err
	}
	if len(o.Platforms) == 0 {
		return nil, errors.New("options.platforms must list at least one platform")
	}
	return &o, nil
}
