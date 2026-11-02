package postiz

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// Options is the Postiz provider's `options:` block in sources.yaml.
type Options struct {
	// Platforms restricts ingestion to these social platforms, by Postiz
	// provider identifier (linkedin, x, bluesky, threads, mastodon, ...).
	// Values are normalized to lower case when parsed.
	Platforms []string `yaml:"platforms"`
	// IncludeScheduled also imports scheduled (QUEUE) posts whose publish
	// time has already passed. Future-scheduled posts are never imported.
	IncludeScheduled bool `yaml:"include_scheduled"`
}

// ParseOptions strict-decodes and validates a source's options for the Postiz
// provider. It is registered in the sources registry as type "postiz".
//
// Platforms are trimmed, lower-cased, and de-duplicated so a config written
// as `[LinkedIn, linkedin]` matches the upstream identifiers consistently and
// produces a stable config hash.
func ParseOptions(cfg config.Source) (any, error) {
	var o Options
	if err := cfg.OptionsInto(&o); err != nil {
		return nil, err
	}

	platforms := make([]string, 0, len(o.Platforms))
	seen := make(map[string]bool, len(o.Platforms))
	for i, raw := range o.Platforms {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			return nil, fmt.Errorf("options.platforms[%d] must not be blank", i)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		platforms = append(platforms, p)
	}
	o.Platforms = platforms

	if len(o.Platforms) == 0 {
		return nil, errors.New("options.platforms must list at least one platform")
	}
	return &o, nil
}

// platformSet returns the configured platforms as a lookup set.
func (o *Options) platformSet() map[string]bool {
	set := make(map[string]bool, len(o.Platforms))
	for _, p := range o.Platforms {
		set[p] = true
	}
	return set
}
