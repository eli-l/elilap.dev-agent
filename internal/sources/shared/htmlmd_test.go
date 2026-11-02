package shared_test

import (
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

func TestHTMLToMarkdown(t *testing.T) {
	src := `<h2>Setup</h2>
<p>Install the <strong>binary</strong> from <a href="https://example.com">the site</a>.</p>
<ul><li>first</li><li>second</li></ul>
<pre><code class="language-go">fmt.Println("hi")</code></pre>
<h3>Details</h3>
<p>See <em>this</em>.</p>`

	md, err := shared.HTMLToMarkdown(src)
	if err != nil {
		t.Fatalf("HTMLToMarkdown() error = %v", err)
	}
	for _, want := range []string{
		"## Setup",
		"**binary**",
		"[the site](https://example.com)",
		"- first",
		"- second",
		"```go\nfmt.Println(\"hi\")\n```",
		"### Details",
		"*this*",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "<p>") || strings.Contains(md, "<h2>") {
		t.Errorf("markdown still contains HTML tags:\n%s", md)
	}
}

func TestHTMLToMarkdownSeparatesParagraphs(t *testing.T) {
	md, err := shared.HTMLToMarkdown(`<html><body><p>one</p><p>two</p></body></html>`)
	if err != nil {
		t.Fatalf("HTMLToMarkdown() error = %v", err)
	}
	if md != "one\n\ntwo" {
		t.Errorf("HTMLToMarkdown() = %q, want paragraphs separated by a blank line", md)
	}
}

func TestChunkHTMLUsesHeadings(t *testing.T) {
	body := "<h2>One</h2><p>" + strings.Repeat("alpha ", 800) + "</p><h2>Two</h2><p>" + strings.Repeat("beta ", 800) + "</p>"
	chunks, err := shared.ChunkHTML(body, shared.ChunkerConfig{TargetTokens: 400, OverlapTokens: 60}, shared.HTMLToMarkdown)
	if err != nil {
		t.Fatalf("ChunkHTML() error = %v", err)
	}
	if len(chunks) < 4 {
		t.Fatalf("ChunkHTML() returned %d chunks, want the post split by size", len(chunks))
	}
	sections := map[string]bool{}
	for _, c := range chunks {
		sections[c.Section] = true
	}
	if !sections["One"] || !sections["Two"] {
		t.Errorf("chunk sections = %v, want the H2 headings preserved", sections)
	}
}
