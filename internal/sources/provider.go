// Package sources defines the import-provider contract and the registry that
// maps a config `type` to a provider's options parser and constructor.
//
// The interface below is the seam the registry constructs the providers
// from, and adding a new source is one package plus one sources.yaml entry.
// The Ghost and Postiz clients still wait on the Phase 0 API captures
// (docs/ghost.md, docs/postiz.md); the local markdown provider
// (internal/sources/files) ships today and needs no credentials.
package sources

import (
	"context"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// Item is a cheap list-level candidate: just enough to decide whether the
// ledger already holds this item's content.
//
// It is an alias of shared.Item: the provider implementations import the
// shared type, and package sources imports the providers.
type Item = shared.Item

// Document is the full, normalized content of one item, ready for chunking.
type Document = shared.Document

// Provider is one configured upstream source. List is cheap and Fetch is
// expensive, which is what makes an incremental ingest run affordable: only
// items whose cheap fingerprint changed from the ledger are fetched.
type Provider interface {
	// Type reports the provider type this implementation answers to, as
	// declared by a source's `type` in sources.yaml.
	Type() string

	// SourceKind reports the kind of item the provider produces, as it is
	// stored in chunks.source_kind ("ghost_post", "postiz_post",
	// "file_post"). The kind is part of the chunk id, so it is part of
	// the identity of everything the provider writes into the index.
	SourceKind() string

	// List returns cheap candidate items modified at or after since. An
	// implementation must not download full content here.
	List(ctx context.Context, since time.Time) ([]Item, error)

	// Fetch returns the full content of one item previously returned by
	// List.
	Fetch(ctx context.Context, it Item) (Document, error)
}
