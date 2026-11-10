package web

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// Adapter is a host-specific extraction strategy: the seam for a website the
// generic passes cannot read (content split over several elements, a reader
// layout that keeps the article in an unidentified <div>, a byline the page
// only renders as text, ...).
//
// An adapter never fetches or renders: it chooses the content region and
// supplies the metadata overrides the page does not expose machine-readably.
// The shared renderer still removes chrome, converts the region to Markdown,
// and fills anything the adapter leaves empty. Registering one is two steps:
// implement this interface in a file next to this one and add it to
// DefaultAdapters; options.strategy then accepts its Name.
type Adapter interface {
	// Name is the strategy name sources.yaml selects.
	Name() string
	// Match reports whether this adapter claims the URL. Only called when
	// options.strategy is "auto".
	Match(u *url.URL) bool
	// Select returns the content region and metadata overrides. Returning a
	// zero Selection is an error: the caller falls back to the generic chain.
	Select(doc *html.Node, u *url.URL) (Selection, error)
}

// Selection is one adapter's answer: the content region plus the fields the
// page does not publish in a machine-readable form.
type Selection struct {
	Node            *html.Node
	Title           string
	Author          string
	Tags            []string
	RemoveSelectors []string
}

// DefaultAdapters returns the adapters this binary knows, in match order.
// Adding a site is one Adapter implementation plus one entry here - the same
// seam shape as the provider registry.
func DefaultAdapters() []Adapter {
	return []Adapter{elilapAdapter{}}
}

// selectAdapter picks an adapter by name, or - when strategy is auto - the
// first adapter that claims the URL.
func selectAdapter(adapters []Adapter, strategy string, u *url.URL) (Adapter, bool) {
	if strategy != StrategyAuto {
		for _, a := range adapters {
			if a.Name() == strategy {
				return a, true
			}
		}
		return nil, false
	}
	for _, a := range adapters {
		if a.Match(u) {
			return a, true
		}
	}
	return nil, false
}

// adapterByName looks an adapter up by its strategy name.
func adapterByName(adapters []Adapter, name string) (Adapter, bool) {
	for _, a := range adapters {
		if a.Name() == name {
			return a, true
		}
	}
	return nil, false
}

// compileSelector compiles one CSS selector, naming the field in the error.
func compileSelector(field, sel string) (cascadia.Selector, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return nil, fmt.Errorf("%s: selector is empty", field)
	}
	compiled, err := cascadia.Compile(sel)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is not a valid CSS selector: %w", field, sel, err)
	}
	return compiled, nil
}

// validateSelector checks selector syntax when it is non-empty; an empty
// selector means "use the strategy default".
func validateSelector(field, sel string) error {
	if strings.TrimSpace(sel) == "" {
		return nil
	}
	_, err := compileSelector(field, sel)
	return err
}

// queryFirst returns the first element matching sel under root, or nil.
func queryFirst(root *html.Node, sel cascadia.Selector) *html.Node {
	if root == nil || sel == nil {
		return nil
	}
	return cascadia.Query(root, sel)
}

// queryAll returns every element matching sel under root.
func queryAll(root *html.Node, sel cascadia.Selector) []*html.Node {
	if root == nil || sel == nil {
		return nil
	}
	return cascadia.QueryAll(root, sel)
}

// unlink removes n (and its subtree) from the tree.
func unlink(n *html.Node) {
	if n == nil || n.Parent == nil {
		return
	}
	n.Parent.RemoveChild(n)
}

// findFirstTag returns the first element with the given tag name under root.
func findFirstTag(root *html.Node, tag string) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && root.Data == tag {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirstTag(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// findElements collects every element with the given tag name under root.
func findElements(root *html.Node, tag string, out []*html.Node) []*html.Node {
	if root == nil {
		return out
	}
	if root.Type == html.ElementNode && root.Data == tag {
		out = append(out, root)
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		out = findElements(c, tag, out)
	}
	return out
}

// textOf returns the visible text under n. Script, style, and other
// non-prose elements contribute nothing, matching shared.HTMLToMarkdown's
// skip list so a char count here means the same thing as a chunk later.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && isSilentTag(node.Data) {
			return
		}
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			b.WriteByte(' ')
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// isSilentTag reports whether tag carries text that must never reach a chunk.
func isSilentTag(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "template", "svg", "iframe", "head", "meta", "link":
		return true
	}
	return false
}

// renderHTML serializes a node's subtree as HTML.
func renderHTML(n *html.Node) (string, error) {
	if n == nil {
		return "", errors.New("render html: nil node")
	}
	var b strings.Builder
	if err := html.Render(&b, n); err != nil {
		return "", fmt.Errorf("render html: %w", err)
	}
	return b.String(), nil
}

// markdownOf converts a content subtree to Markdown through the shared
// renderer, so the provider never carries a second HTML-to-text path.
func markdownOf(n *html.Node) (string, error) {
	raw, err := renderHTML(n)
	if err != nil {
		return "", err
	}
	md, err := shared.HTMLToMarkdown(raw)
	if err != nil {
		return "", fmt.Errorf("html to markdown: %w", err)
	}
	return md, nil
}
