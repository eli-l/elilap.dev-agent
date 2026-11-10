package web

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// elilapAdapter is the reference implementation of the Adapter seam: it is
// the host-specific strategy for elilap.dev, the site this provider was
// written for.
//
// It earns its place only where the generic passes are blind: the Astro site
// publishes no JSON-LD and no byline, and its content region differs per page
// family (case studies keep the body in <article>, service pages only have
// <main>). Everything else - chrome removal, Markdown rendering, metadata -
// stays in the shared extractor, so this file is small on purpose.
type elilapAdapter struct{}

// elilapHost is the site this adapter claims.
const elilapHost = "elilap.dev"

// elilapAuthor is the byline the site renders as plain text.
const elilapAuthor = "Eli Lap"

// Name reports the strategy name sources.yaml selects: "elilap".
func (elilapAdapter) Name() string { return "elilap" }

// Match claims elilap.dev and its www alias.
func (elilapAdapter) Match(u *url.URL) bool {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	return host == elilapHost
}

// Select returns the page body: <article> when the page family has one (case
// studies), else <main>, else <body> with the chrome removal doing the work.
// The last fallback matters: a page with neither semantic container (an Astro
// layout without <main>, or an empty placeholder route) must degrade to the
// body, not fail the extractor.
func (elilapAdapter) Select(doc *html.Node, u *url.URL) (Selection, error) {
	content := findFirstTag(doc, "article")
	if content == nil {
		content = findFirstTag(doc, "main")
	}
	if content == nil {
		content = findFirstTag(doc, "body")
	}
	if content == nil {
		return Selection{}, fmt.Errorf("elilap: %s has no <body> to extract from", u.Redacted())
	}
	return Selection{
		Node:            content,
		Author:          elilapAuthor,
		Tags:            sectionTags(u),
		RemoveSelectors: []string{".share", ".share-bar", "[data-share]", ".newsletter"},
	}, nil
}
