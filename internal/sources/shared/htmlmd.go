package shared

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// HTMLToMarkdown renders an HTML fragment as Markdown good enough to chunk
// on: headings become ATX headings, paragraphs are separated by blank lines,
// lists keep their bullets, and <pre> blocks become fenced code. Inline
// emphasis and links are preserved. It is the htmlToMD implementation the
// chunker expects (ChunkHTML), so Ghost posts split on their real headings
// instead of their plaintext rendering.
func HTMLToMarkdown(src string) (string, error) {
	root, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}
	r := &mdRenderer{}
	r.blocks(root)
	return r.finish(), nil
}

// mdRenderer accumulates block-level Markdown. Block writers append two
// newlines so the output is always paragraph-separated; finish() collapses
// the runs.
type mdRenderer struct {
	b strings.Builder
}

func (r *mdRenderer) write(s string) { r.b.WriteString(s) }

// block appends one block, keeping blank lines between blocks.
func (r *mdRenderer) block(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if r.b.Len() > 0 {
		if !strings.HasSuffix(r.b.String(), "\n\n") {
			r.write("\n\n")
		}
	}
	r.write(s)
	r.write("\n\n")
}

// finish normalizes blank-line runs and right-trims prose lines. Lines inside
// a fenced code block are left byte-for-byte untouched so code formatting
// (blank lines, trailing spacing) survives.
func (r *mdRenderer) finish() string {
	lines := strings.Split(r.b.String(), "\n")
	var out []string
	blank := 0
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			blank = 0
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, strings.TrimRight(line, " "))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// blocks walks a node's children and renders the block-level ones. Inline
// content that appears outside a paragraph is still collected into one
// paragraph so no text is dropped.
func (r *mdRenderer) blocks(n *html.Node) {
	var inline strings.Builder
	flush := func() {
		if s := strings.TrimSpace(inline.String()); s != "" {
			r.block(s)
		}
		inline.Reset()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			inline.WriteString(c.Data)
			continue
		}
		if c.Type != html.ElementNode {
			continue
		}
		if isSkipElement(c.Data) {
			continue
		}
		if isBlockElement(c.Data) {
			flush()
			r.renderBlock(c)
			continue
		}
		inline.WriteString(inlineMarkdown(c))
	}
	flush()
}

// renderBlock renders one block-level element.
func (r *mdRenderer) renderBlock(n *html.Node) {
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		r.block(strings.Repeat("#", level) + " " + strings.TrimSpace(textContent(n)))
	case "p":
		r.block(inlineMarkdownChildren(n))
	case "pre":
		lang := ""
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || c.Data != "code" {
				continue
			}
			for _, cls := range strings.Fields(attr(c, "class")) {
				if strings.HasPrefix(cls, "language-") {
					lang = strings.TrimPrefix(cls, "language-")
					break
				}
			}
		}
		code := strings.TrimRight(textContent(n), "\n")
		r.block("```" + lang + "\n" + code + "\n```")
	case "blockquote":
		var q mdRenderer
		q.blocks(n)
		quoted := q.finish()
		if quoted == "" {
			return
		}
		lines := strings.Split(quoted, "\n")
		for i, line := range lines {
			if line == "" {
				lines[i] = ">"
			} else {
				lines[i] = "> " + line
			}
		}
		r.block(strings.Join(lines, "\n"))
	case "ul", "ol":
		r.block(listMarkdown(n))
	case "hr":
		r.block("---")
	case "figure", "figcaption":
		r.blocks(n)
	case "table":
		r.block(tableMarkdown(n))
	default:
		// div, section, article, header, footer, nav, main, body, html,
		// etc.: recurse so their block children are not lost.
		if hasBlockChild(n) {
			r.blocks(n)
			return
		}
		r.block(strings.TrimSpace(inlineMarkdownChildren(n)))
	}
}

// listMarkdown renders a ul/ol, flattening nested lists with indentation.
func listMarkdown(n *html.Node) string {
	var lines []string
	idx := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "li" {
			continue
		}
		idx++
		prefix := "- "
		if n.Data == "ol" {
			prefix = fmt.Sprintf("%d. ", idx)
		}
		var text strings.Builder
		for gc := c.FirstChild; gc != nil; gc = gc.NextSibling {
			if gc.Type == html.ElementNode && (gc.Data == "ul" || gc.Data == "ol") {
				continue
			}
			text.WriteString(inlineMarkdown(gc))
		}
		if t := strings.TrimSpace(text.String()); t != "" {
			lines = append(lines, prefix+t)
		}
		// Nested lists render on their own indented lines.
		for gc := c.FirstChild; gc != nil; gc = gc.NextSibling {
			if gc.Type == html.ElementNode && (gc.Data == "ul" || gc.Data == "ol") {
				for _, line := range strings.Split(listMarkdown(gc), "\n") {
					lines = append(lines, "  "+line)
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// inlineMarkdown renders an inline node (or a block used inline) as a single
// line of Markdown.
func inlineMarkdown(n *html.Node) string {
	switch n.Type {
	case html.TextNode:
		return n.Data
	case html.ElementNode:
		if isSkipElement(n.Data) {
			return ""
		}
		switch n.Data {
		case "a":
			href := attr(n, "href")
			text := strings.TrimSpace(inlineMarkdownChildren(n))
			if href == "" || text == "" {
				return text
			}
			return "[" + text + "](" + href + ")"
		case "strong", "b":
			return "**" + strings.TrimSpace(inlineMarkdownChildren(n)) + "**"
		case "em", "i":
			return "*" + strings.TrimSpace(inlineMarkdownChildren(n)) + "*"
		case "code":
			return "`" + strings.TrimSpace(textContent(n)) + "`"
		case "br":
			return "\n"
		case "img":
			alt, src := attr(n, "alt"), attr(n, "src")
			if src == "" {
				return ""
			}
			return "![" + alt + "](" + src + ")"
		default:
			return inlineMarkdownChildren(n)
		}
	}
	return ""
}

// inlineMarkdownChildren renders every child of n inline.
func inlineMarkdownChildren(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(inlineMarkdown(c))
	}
	return b.String()
}

// textContent returns the concatenated text nodes under n.
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && isSkipElement(n.Data) {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// tableMarkdown renders table rows as pipe-separated lines, preserving cell
// and row boundaries that plain text concatenation would lose.
func tableMarkdown(n *html.Node) string {
	var lines []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			var cells []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
					cells = append(cells, strings.TrimSpace(inlineMarkdownChildren(c)))
				}
			}
			if len(cells) > 0 {
				lines = append(lines, strings.Join(cells, " | "))
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(lines, "\n")
}

// attr returns the value of the named attribute, or "".
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// hasBlockChild reports whether n contains a block-level descendant, which
// decides whether a generic container is rendered by recursion or inline.
func hasBlockChild(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && isBlockElement(c.Data) {
			return true
		}
	}
	return false
}

// isSkipElement reports whether tag carries no indexable prose and must not
// contribute text to a chunk.
func isSkipElement(tag string) bool {
	switch tag {
	case "script", "style", "title", "noscript", "template", "head", "meta", "link":
		return true
	}
	return false
}

// isBlockElement reports whether tag is rendered as its own block.
func isBlockElement(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "body", "div", "dl", "dd",
		"dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2",
		"h3", "h4", "h5", "h6", "header", "hr", "html", "li", "main", "nav",
		"ol", "p", "pre", "section", "table", "ul":
		return true
	}
	return false
}
