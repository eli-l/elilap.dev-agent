// Package shared holds the types every import provider speaks: the cheap
// list-level candidate and the full document.
//
// They live here, and not in package sources, because the provider
// implementations import them. Package sources imports the providers - the
// registry maps a config type to a constructor - so the shared types have to
// sit below both, or the import graph would have a cycle.
package shared

import "time"

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
