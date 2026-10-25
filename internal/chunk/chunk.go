// Package chunk splits source text into the overlapping chunks the index
// stores.
//
// The split follows the structure of the text: paragraphs first, sentences
// inside a paragraph that is too long for one chunk. Chunks are packed up
// to a target size, and consecutive chunks overlap so a sentence that
// straddles a boundary is still retrievable from one of the two chunks.
//
// The unit of size here is a word, not a model token: see Tokens.
package chunk

import (
	"strings"
	"unicode"
)

// DefaultTargetTokens is the chunk size used when the caller does not ask
// for one. It matches the default chunking policy in sources.yaml
// (target_tokens: 400).
const DefaultTargetTokens = 400

// Tokens counts the words in text.
//
// This is an approximation, not the model's tokenizer: it splits on
// whitespace. The plan treats chunk sizes as approximate for exactly this
// reason, and it keeps the chunker free of a model-specific dependency.
func Tokens(text string) int {
	return len(strings.Fields(text))
}

// Split cuts text into chunks of at most targetTokens words, with
// overlapTokens words of overlap between consecutive chunks.
//
// Paragraph boundaries are preferred over sentence boundaries, and a
// paragraph that is still too long is split on sentences. The overlap is
// the tail of the previous chunk, repeated at the head of the next one, so
// a passage that sits on a chunk boundary can still be found whole.
//
// A targetTokens of zero or less falls back to DefaultTargetTokens rather
// than failing: the chunker has no useful way to report a bad size, and
// the default is the policy the rest of the project already ships. An
// overlapTokens of zero or less means no overlap, and an overlapTokens that
// would fill a whole chunk is reduced to one word less, because otherwise
// no new text could ever enter the next chunk.
//
// Empty input returns nil. Non-empty input always returns at least one
// non-empty chunk.
func Split(text string, targetTokens, overlapTokens int) []string {
	if targetTokens <= 0 {
		targetTokens = DefaultTargetTokens
	}
	if overlapTokens < 0 {
		overlapTokens = 0
	}
	if overlapTokens >= targetTokens {
		overlapTokens = targetTokens - 1
	}

	normalized := normalize(text)
	if normalized == "" {
		return nil
	}

	chunks := pack(units(normalized, targetTokens), targetTokens, overlapTokens)
	if len(chunks) == 0 {
		return nil
	}
	return chunks
}

// unit is one indivisible piece of a chunk: a whole paragraph, a sentence
// inside a paragraph, or a run of words.
type unit struct {
	text  string
	words int
	sep   string // separator to place before this unit inside a chunk
}

// normalize folds line endings and collapses runs of blank lines, so the
// paragraph structure is the same whatever the upstream produced.
func normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text)
}

// units breaks the text into pieces that each fit in a chunk on their own:
// paragraphs when they fit, sentences inside paragraphs that do not, and
// word runs inside sentences that are still too long.
func units(text string, target int) []unit {
	var out []unit
	for _, paragraph := range strings.Split(text, "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if words := Tokens(paragraph); words <= target {
			out = append(out, unit{text: paragraph, words: words, sep: "\n\n"})
			continue
		}
		for i, sentence := range splitSentences(paragraph) {
			words := Tokens(sentence)
			if words == 0 {
				continue
			}
			sep := " "
			if i == 0 {
				sep = "\n\n"
			}
			if words <= target {
				out = append(out, unit{text: sentence, words: words, sep: sep})
				continue
			}
			// A single sentence longer than the target: split it on word
			// boundaries so that no piece is oversized.
			fields := strings.Fields(sentence)
			for start := 0; start < len(fields); start += target {
				end := min(start+target, len(fields))
				pieceSep := " "
				if i == 0 && start == 0 {
					pieceSep = sep
				}
				out = append(out, unit{
					text:  strings.Join(fields[start:end], " "),
					words: end - start,
					sep:   pieceSep,
				})
			}
		}
	}
	return out
}

// pack fills chunks with units, up to the target size, and seeds each new
// chunk with the tail of the previous one so the two overlap.
func pack(pieces []unit, target, overlap int) []string {
	var chunks []string
	var current []unit
	currentWords := 0
	hasContent := false
	var tail []string

	for _, piece := range pieces {
		if currentWords+piece.words > target {
			if hasContent {
				text := assemble(current)
				chunks = append(chunks, text)
				tail = lastWords(text, overlap)
				current = nil
				currentWords = 0
				hasContent = false
			}
			if len(tail) > 0 {
				room := target - piece.words
				if room > 0 {
					n := min(len(tail), room)
					seed := strings.Join(tail[len(tail)-n:], " ")
					current = append(current, unit{text: seed, words: n, sep: " "})
					currentWords = n
				}
			}
		}
		if currentWords+piece.words > target {
			// The overlap left no room for this piece; start clean.
			current = nil
			currentWords = 0
		}
		current = append(current, piece)
		currentWords += piece.words
		hasContent = true
	}

	if hasContent {
		chunks = append(chunks, assemble(current))
	}
	return chunks
}

// assemble joins the units of one chunk, keeping the paragraph breaks.
func assemble(pieces []unit) string {
	var b strings.Builder
	for i, piece := range pieces {
		if i > 0 {
			b.WriteString(piece.sep)
		}
		b.WriteString(piece.text)
	}
	return b.String()
}

// lastWords returns the final n words of text.
func lastWords(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	fields := strings.Fields(text)
	if len(fields) > n {
		fields = fields[len(fields)-n:]
	}
	return fields
}

// splitSentences splits a paragraph into sentences on terminal punctuation.
//
// The punctuation stays with its sentence. A period inside a word ("3.14",
// "e.g.") and an initial ("J.") do not end a sentence.
func splitSentences(paragraph string) []string {
	runes := []rune(paragraph)
	var sentences []string
	start := 0

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		j := i
		for j+1 < len(runes) && isTerminal(runes[j+1]) {
			j++
		}
		if r == '.' && isAbbreviation(runes, i) {
			i = j
			continue
		}
		k := j + 1
		for k < len(runes) && isClosing(runes[k]) {
			k++
		}
		if k < len(runes) && !unicode.IsSpace(runes[k]) {
			// The punctuation is part of a word, not a sentence end.
			i = j
			continue
		}
		if sentence := strings.TrimSpace(string(runes[start : j+1])); sentence != "" {
			sentences = append(sentences, sentence)
		}
		start = k
		i = j
	}

	if start < len(runes) {
		if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
			sentences = append(sentences, tail)
		}
	}
	if len(sentences) == 0 {
		if trimmed := strings.TrimSpace(paragraph); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}
	return sentences
}

// isTerminal reports whether r ends a sentence.
func isTerminal(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

// isClosing reports whether r closes a quote or a bracket.
func isClosing(r rune) bool {
	switch r {
	case ')', ']', '"', '\'', '”', '’', '»':
		return true
	}
	return false
}

// isAbbreviation reports whether the period at dot ends an abbreviation
// rather than a sentence.
//
// The word before the period is read as letters and digits only, so the
// final period of "e.g." sees the single letter "g" and the whole thing
// stays one sentence.
func isAbbreviation(runes []rune, dot int) bool {
	end := dot
	for end > 0 && isWordRune(runes[end-1]) {
		end--
	}
	word := strings.ToLower(string(runes[end:dot]))
	if len(word) == 1 {
		// An initial: "J. R. R. Tolkien".
		return true
	}
	switch word {
	case "mr", "mrs", "ms", "dr", "prof", "st", "vs", "etc", "fig", "inc", "ltd", "co":
		return true
	}
	return false
}

// isWordRune reports whether r can be part of a word.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
