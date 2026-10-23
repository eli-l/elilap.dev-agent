// Package sources defines the import-provider contract and the registry that
// maps a config `type` to a provider's option parser.
//
// This chunk contains no Provider implementations: the Ghost and Postiz
// clients wait on the Phase 0 API captures (docs/ghost.md, docs/postiz.md).
// The interface below is the seam the registry will construct them from, and
// adding a new source (for example the deferred website provider) is one
// package plus one sources.yaml entry.
package sources

import (
	"context"
	"time"
)

// Provider is one configured upstream source. List is cheap and Fetch is
// expensive, which is what makes an incremental ingest run affordable: only
// items whose cheap fingerprint changed from the ledger are fetched.
type Provider interface {
	// Type reports the provider type this implementation answers to, as
	// declared by a source's `type` in sources.yaml.
	Type() string

	// List returns cheap candidate items modified at or after since. An
	// implementation must not download full content here.
	List(ctx context.Context, since time.Time) ([]Item, error)

	// Fetch returns the full content of one item previously returned by
	// List.
	Fetch(ctx context.Context, it Item) (Document, error)
}

// Item is a cheap list-level candidate: just enough to decide whether the
// ledger already holds this item's content.
type Item struct {
	// ID is the upstream identifier (Ghost uuid, Postiz post id). It is the
	// ledger key together with the source's config id.
	ID string
	// URL is the canonical upstream URL, when the provider knows it.
	URL string
	// ETag is an upstream entity tag, when the provider exposes one.
	ETag string
	// LastModified is the upstream modification time, zero when unknown.
	LastModified time.Time
	// Hint is a provider-specific cheap fingerprint (an updated_at string,
	// a status, a precomputed hash) used to detect unchanged items.
	Hint string
}

// Document is the full, normalized content of one item, ready for chunking.
type Document struct {
	// Title is the document title, empty for untitled items.
	Title string
	// Body is the normalized plain-text or Markdown body used for chunking
	// and full-text indexing.
	Body string
	// HTML is the original HTML body when the upstream returned one.
	HTML string
	// PublishedAt is the upstream publication time.
	PublishedAt time.Time
	// Tags are the upstream tags or categories.
	Tags []string
	// Platform is the social platform for cross-posted items and empty for
	// blog posts.
	Platform string
	// Meta carries any provider-specific fields worth keeping alongside
	// the document (status, release id, thread root, ...).
	Meta map[string]any
}
