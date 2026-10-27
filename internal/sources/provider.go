// Package sources defines the import-provider contract and the registry
// that maps a config `type` to a provider's option parser and constructor.
//
// The canonical Provider/Item/Document types live in internal/sources/shared
// so provider packages (ghost, postiz) can implement them without an import
// cycle; this package re-exports them as aliases.
package sources

import (
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// Provider is one configured upstream source (see shared.Provider).
type Provider = shared.Provider

// Item is a cheap list-level candidate (see shared.Item).
type Item = shared.Item

// Document is the full normalized content of one item (see shared.Document).
type Document = shared.Document
