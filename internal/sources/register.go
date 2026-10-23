package sources

import (
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
	}
	for _, b := range builtins {
		if err := r.Register(b.typeName, b.parser); err != nil {
			return err
		}
	}
	return nil
}
