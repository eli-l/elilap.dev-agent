package shared

import (
	"strings"
)

// Chunk is one chunker output: a slice of text plus its heading path.
type Chunk struct {
	// Text is the chunk body: normalized Markdown/plain text.
	Text string
	// Section is the heading path ("Intro > Setup") the chunk sits under,
	// empty when the text has no headings (e.g. social posts).
	Section string
	// Tokens is the estimated token count of Text.
	Tokens int
}

// ChunkerConfig sizes the split. TargetTokens must be positive; OverlapTokens
// must be non-negative and strictly smaller than TargetTokens.
type ChunkerConfig struct {
	TargetTokens  int
	OverlapTokens int
}

// ChunkHTML splits Ghost post HTML into token-sized chunks: it walks the
// document, splits on H2 headings (falling back to H3), preserves code
// blocks intact, and renders to Markdown. Posts smaller than the target are
// returned as one chunk (PLAN.md: a <800-token post is not fragmented).
//
// htmlToMD is injected so the dependency on golang.org/x/net/html rendering
// stays out of the core chunking logic and can be stubbed in tests.
func ChunkHTML(html string, cfg ChunkerConfig, htmlToMD func(string) (string, error)) ([]Chunk, error) {
	md, err := htmlToMD(html)
	if err != nil {
		return nil, err
	}
	return ChunkMarkdown(md, cfg), nil
}

// ChunkMarkdown splits already-rendered Markdown by headings, then by token
// budget. It is the single split implementation both the HTML and the plain
// text paths go through.
func ChunkMarkdown(md string, cfg ChunkerConfig) []Chunk {
	md = strings.TrimSpace(md)
	if md == "" {
		return nil
	}

	if EstimateTokens(md) <= cfg.TargetTokens {
		return []Chunk{{Text: md, Tokens: EstimateTokens(md)}}
	}

	var out []Chunk
	for _, sec := range splitByHeadings(md) {
		out = append(out, splitSection(sec.text, sec.heading, cfg)...)
	}
	if len(out) == 0 { // headings split produced nothing (blank doc)
		out = []Chunk{{Text: md, Tokens: EstimateTokens(md)}}
	}
	return out
}

// headingSec is one H2/H3-delimited section of the markdown text.
type headingSec struct {
	heading string // e.g. "Intro > Setup"; empty for preamble
	text    string // body text under the heading, without the heading line
}

// splitByHeadings groups markdown into sections delimited by ## (preferred)
// or ### (fallback when no ## exists). Text before the first heading keeps an
// empty heading.
func splitByHeadings(md string) []headingSec {
	lines := strings.Split(md, "\n")
	depth := headingDepth(md)
	if depth == 0 {
		return []headingSec{{text: md}}
	}

	var secs []headingSec
	var cur strings.Builder
	curHeading := ""
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			secs = append(secs, headingSec{heading: curHeading, text: t})
			cur.Reset()
		}
	}
	for _, line := range lines {
		if d := atxDepth(line); d == depth {
			flush()
			title := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if curHeading == "" {
				curHeading = title
			} else {
				curHeading = curHeading + " > " + title
			}
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	flush()
	return secs
}

// headingDepth returns 2 when the doc uses H2 sectioning, 3 when only H3 is
// present, and 0 when no sectioning heading exists.
func headingDepth(md string) int {
	hasH2, hasH3 := false, false
	for _, line := range strings.Split(md, "\n") {
		switch atxDepth(line) {
		case 2:
			hasH2 = true
		case 3:
			hasH3 = true
		}
	}
	switch {
	case hasH2:
		return 2
	case hasH3:
		return 3
	default:
		return 0
	}
}

// atxDepth returns the ATX heading level of line (1-6), or 0.
func atxDepth(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(line) || line[n] != ' ' {
		return 0
	}
	return n
}

// splitSection splits one heading section's text on the token budget: whole
// paragraphs first, hard-wrapped only when a single paragraph exceeds the
// budget. Overlap re-includes the tail of the previous chunk when configured.
func splitSection(text, heading string, cfg ChunkerConfig) []Chunk {
	paras := strings.Split(text, "\n\n")
	var out []Chunk
	var cur strings.Builder
	curTokens := 0

	appendChunk := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, Chunk{Text: t, Section: heading, Tokens: EstimateTokens(t)})
		}
		cur.Reset()
		curTokens = 0
	}

	for _, p := range paras {
		pTokens := EstimateTokens(p)
		if pTokens > cfg.TargetTokens {
			// Hard-wrap oversized paragraphs by lines so code blocks inside
			// a section survive without being cut mid-line.
			appendChunk()
			for _, piece := range hardWrap(p, cfg) {
				out = append(out, Chunk{Text: piece, Section: heading, Tokens: EstimateTokens(piece)})
			}
			continue
		}
		if curTokens > 0 && curTokens+pTokens > cfg.TargetTokens {
			appendChunk()
			if cfg.OverlapTokens > 0 {
				overlap := tailOverlap(cur.String(), cfg.OverlapTokens)
				if overlap != "" {
					cur.WriteString(overlap)
					curTokens = EstimateTokens(overlap)
				}
			}
		}
		cur.WriteString(p)
		cur.WriteString("\n\n")
		curTokens += pTokens
	}
	appendChunk()
	return out
}

// hardWrap splits one oversized paragraph by lines into budget-sized pieces.
func hardWrap(para string, cfg ChunkerConfig) []string {
	var pieces []string
	var cur strings.Builder
	curTokens := 0
	for _, line := range strings.Split(para, "\n") {
		lineTokens := EstimateTokens(line)
		if curTokens > 0 && curTokens+lineTokens > cfg.TargetTokens {
			pieces = append(pieces, strings.TrimSpace(cur.String()))
			cur.Reset()
			curTokens = 0
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		curTokens += lineTokens
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		pieces = append(pieces, t)
	}
	return pieces
}

// tailOverlap returns the last lines of prev worth about overlapTokens.
func tailOverlap(prev string, overlapTokens int) string {
	lines := strings.Split(strings.TrimRight(prev, "\n"), "\n")
	var kept []string
	tokens := 0
	for i := len(lines) - 1; i >= 0 && tokens < overlapTokens; i-- {
		t := EstimateTokens(lines[i])
		kept = append([]string{lines[i]}, kept...)
		tokens += t
	}
	return strings.Join(kept, "\n") + "\n"
}
