package sources

import (
	"fmt"

	"github.com/eli-l/elilap.dev-agent/internal/sources/files"
	"github.com/eli-l/elilap.dev-agent/internal/sources/ghost"
	"github.com/eli-l/elilap.dev-agent/internal/sources/postiz"
)

// RegisterBuiltins registers the options parsers of every provider this
// binary knows about. A new provider is one sources.yaml entry plus one line
// here; the registry is the seam that keeps that addition local.
func RegisterBuiltins(r *Registry) error {
	builtins := []struct {
		typeName string
		parser   OptionsParser
	}{
		{typeName: "ghost", parser: ghost.ParseOptions},
		{typeName: "postiz", parser: postiz.ParseOptions},
		{typeName: files.Type, parser: files.ParseOptions},
	}
	for _, b := range builtins {
		if err := r.Register(b.typeName, b.parser); err != nil {
			return err
		}
	}

	// Only the providers with a client register a constructor. The Ghost
	// and Postiz clients are still waiting on the Phase 0 API captures,
	// so their types are known but not buildable.
	if err := r.RegisterProvider(files.Type, newFilesProvider); err != nil {
		return err
	}
	return nil
}

// newFilesProvider builds the local markdown provider from its parsed
// options.
func newFilesProvider(opts any) (Provider, error) {
	o, ok := opts.(*files.Options)
	if !ok {
		return nil, fmt.Errorf("the files provider got options of type %T", opts)
	}
	return files.New(o)
}
