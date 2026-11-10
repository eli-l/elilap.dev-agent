package sources

import (
	"errors"
	"fmt"
	"sort"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// OptionsParser strict-decodes one source's raw options into that provider's
// typed options value and validates it. Providers return their parsed
// options as a concrete struct pointer, ready for constructing a client.
type OptionsParser func(cfg config.Source) (any, error)

// Constructor builds a live provider from one configured source, its parsed
// options, and the run-wide build context (HTTP client, User-Agent).
// Credentials are resolved inside the provider from the source's { env: VAR }
// references, so no secret ever passes through this package.
type Constructor func(src config.Source, opts any, bc shared.BuildContext) (shared.Provider, error)

// Registry maps a config `type` name to the parser for that provider's
// options and to its constructor. It is the single place that decides
// whether a configured type is known, so an unknown type or bad options is
// a startup error rather than a runtime surprise. A zero Registry is not
// usable; call NewRegistry.
type Registry struct {
	parsers      map[string]OptionsParser
	constructors map[string]Constructor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		parsers:      make(map[string]OptionsParser),
		constructors: make(map[string]Constructor),
	}
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

// RegisterConstructor adds the live-provider constructor for a type. The
// options parser must already be registered: a constructor without one could
// never be reached. Registering twice is an error, like Register.
func (r *Registry) RegisterConstructor(typeName string, c Constructor) error {
	if typeName == "" {
		return errors.New("register constructor: type name is required")
	}
	if c == nil {
		return fmt.Errorf("register constructor %q: constructor is nil", typeName)
	}
	if _, ok := r.parsers[typeName]; !ok {
		return fmt.Errorf("register constructor %q: no options parser registered first", typeName)
	}
	if _, ok := r.constructors[typeName]; ok {
		return fmt.Errorf("register constructor %q: type already registered", typeName)
	}
	r.constructors[typeName] = c
	return nil
}

// Build constructs the live provider for one configured source. An unknown
// type or a type without a constructor (a declared provider whose client has
// not landed yet) is an error naming the source.
func (r *Registry) Build(src config.Source, opts any, bc shared.BuildContext) (shared.Provider, error) {
	c, ok := r.constructors[src.Type]
	if !ok {
		return nil, fmt.Errorf("source %q: provider type %q has no constructor (not implemented)", src.ID, src.Type)
	}
	p, err := c(src, opts, bc)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", src.ID, err)
	}
	return p, nil
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
