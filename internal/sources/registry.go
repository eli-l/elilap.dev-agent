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

// ErrNoClient is the error NewProvider returns for a provider type whose
// client does not exist yet. The Ghost and Postiz clients wait on the Phase
// 0 API captures; a caller can tell that case apart from a configuration
// error and decide what to do with it.
var ErrNoClient = errors.New("no client implementation yet")

// Constructor builds a live provider from a source's parsed options.
//
// The registry holds one constructor per provider type: the options parser
// validates the configuration, the constructor turns it into the client the
// ingestor talks to. A type without a constructor is a type whose client
// does not exist yet - the Ghost and Postiz clients are still waiting on
// the Phase 0 API captures - and NewProvider says so instead of pretending
// the provider exists.
type Constructor func(opts any) (Provider, error)

// Registry maps a config `type` name to the parser for that provider's
// options. It is the single place that decides whether a configured type is
// known, so an unknown type or bad options is a startup error rather than a
// runtime surprise. A zero Registry is not usable; call NewRegistry.
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

// RegisterProvider adds the constructor of a provider type.
//
// The type must already have an options parser: a client without a schema
// for its configuration would accept anything, which is exactly the silent
// misconfiguration the registry exists to prevent.
func (r *Registry) RegisterProvider(typeName string, c Constructor) error {
	if typeName == "" {
		return errors.New("register provider: type name is required")
	}
	if c == nil {
		return fmt.Errorf("register provider %q: constructor is nil", typeName)
	}
	if _, ok := r.parsers[typeName]; !ok {
		return fmt.Errorf("register provider %q: no options parser is registered for this type", typeName)
	}
	if _, ok := r.constructors[typeName]; ok {
		return fmt.Errorf("register provider %q: a constructor is already registered", typeName)
	}
	r.constructors[typeName] = c
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

// NewProvider builds the provider of one configured source.
//
// The source's options are parsed and validated first, so a bad option is
// reported before any client is built. A type whose client does not exist
// yet is an error that says so: the ingestor must not silently skip a
// source the user asked for.
func (r *Registry) NewProvider(src config.Source) (Provider, error) {
	opts, err := r.ParseOptions(src)
	if err != nil {
		return nil, err
	}
	c, ok := r.constructors[src.Type]
	if !ok {
		return nil, fmt.Errorf("source %q: provider type %q: %w", src.ID, src.Type, ErrNoClient)
	}
	p, err := c(opts)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", src.ID, err)
	}
	return p, nil
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
