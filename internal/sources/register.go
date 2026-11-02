package sources

import (
	"github.com/eli-l/elilap.dev-agent/internal/sources/ghost"
	"github.com/eli-l/elilap.dev-agent/internal/sources/postiz"
)

// RegisterBuiltins registers the options parser and constructor of every
// provider this binary knows about. A new provider is one sources.yaml entry
// plus the registrations here; the registry is the seam that keeps that
// addition local.
func RegisterBuiltins(r *Registry) error {
	builtins := []struct {
		typeName    string
		parser      OptionsParser
		constructor Constructor
	}{
		{typeName: "ghost", parser: ghost.ParseOptions, constructor: ghost.NewFromSource},
		// Postiz has options but no client yet; ingest reports it as not
		// implemented instead of pretending to import it.
		{typeName: "postiz", parser: postiz.ParseOptions},
	}
	for _, b := range builtins {
		if err := r.Register(b.typeName, b.parser); err != nil {
			return err
		}
		if b.constructor == nil {
			continue
		}
		if err := r.RegisterConstructor(b.typeName, b.constructor); err != nil {
			return err
		}
	}
	return nil
}
