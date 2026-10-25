package sources

import (
	"errors"
	"fmt"
	"sort"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// OptionsParser strict-decodes one source's raw options into that provider's
// typed options value and validates it. Providers return their parsed
// options as a concrete struct pointer, ready for constructing a client.
type OptionsParser func(cfg config.Source) (any, error)

// Registry maps a config `type` name to the parser for that provider's
// options. It is the single place that decides whether a configured type is
// known, so an unknown type or bad options is a startup error rather than a
// runtime surprise. A zero Registry is not usable; call NewRegistry.
type Registry struct {
	parsers map[string]OptionsParser
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{parsers: make(map[string]OptionsParser)}
}

// Register adds a provider type and its options parser. Registering the same
// type twice is an error, so a provider cannot silently shadow another.
func (r *Registry) Register(typeName string, p OptionsParser) error {
	if typeName == "" {
		return errors.New("register provider: type name is required")
	}
	if p == nil {
		return fmt.Errorf("register provider %q: parser is nil", typeName)
	}
	if _, ok := r.parsers[typeName]; ok {
		return fmt.Errorf("register provider %q: type already registered", typeName)
	}
	r.parsers[typeName] = p
	return nil
}

// KnownTypes returns the registered type names in sorted order.
func (r *Registry) KnownTypes() []string {
	types := make([]string, 0, len(r.parsers))
	for typeName := range r.parsers {
		types = append(types, typeName)
	}
	sort.Strings(types)
	return types
}

// ParseOptions parses and validates the options of one configured source.
// An unknown type is an error that lists the known types.
func (r *Registry) ParseOptions(src config.Source) (any, error) {
	p, ok := r.parsers[src.Type]
	if !ok {
		return nil, fmt.Errorf("source %q: unknown provider type %q (known types: %v)",
			src.ID, src.Type, r.KnownTypes())
	}
	opts, err := p(src)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", src.ID, err)
	}
	return opts, nil
}

// ValidateAll parses the options of every configured source and reports all
// failures together, so one run surfaces every config problem instead of
// only the first.
func (r *Registry) ValidateAll(cfg *config.Config) error {
	var errs []error
	for _, src := range cfg.Sources {
		if _, err := r.ParseOptions(src); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
