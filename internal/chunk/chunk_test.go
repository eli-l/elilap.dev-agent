package chunk_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/chunk"
)

// longText builds a text of paragraphs, each holding sentences of a fixed
// number of words.
func longText(paragraphs, sentencesPerParagraph, wordsPerSentence int) string {
	var b strings.Builder
	for p := 0; p < paragraphs; p++ {
		for s := 0; s < sentencesPerParagraph; s++ {
			b.WriteString("Sentence about topic ")
			b.WriteString(strconv.Itoa(p))
			b.WriteString(" ")
			b.WriteString(strconv.Itoa(s))
			for w := 1; w < wordsPerSentence; w++ {
				b.WriteString(" word")
			}
			b.WriteString(". ")
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// lastWords returns the final n words of text.
func lastWords(text string, n int) []string {
	fields := strings.Fields(text)
	if len(fields) > n {
		fields = fields[len(fields)-n:]
	}
	return fields
}

func TestSplitLongTextIntoSeveralChunks(t *testing.T) {
	text := longText(6, 5, 7)

	chunks := chunk.Split(text, 40, 10)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want more than one", len(chunks))
	}
	for i, c := range chunks {
		if strings.TrimSpace(c) == "" {
			t.Errorf("chunk %d is empty", i)
		}
		if got := chunk.Tokens(c); got > 40 {
			t.Errorf("chunk %d has %d words, want at most 40", i, got)
		}
	}
}

func TestSplitKeepsEveryChunkUnderTheTarget(t *testing.T) {
	text := longText(10, 4, 9)

	for _, target := range []int{20, 37, 64, 100} {
		chunks := chunk.Split(text, target, 5)
		if len(chunks) == 0 {
			t.Fatalf("target %d: no chunks returned", target)
		}
		for i, c := range chunks {
			if got := chunk.Tokens(c); got > target {
				t.Errorf("target %d: chunk %d has %d words, want at most %d", target, i, got, target)
			}
		}
	}
}

func TestSplitMakesConsecutiveChunksOverlap(t *testing.T) {
	text := longText(6, 5, 7)
	const overlap = 10

	chunks := chunk.Split(text, 40, overlap)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want more than one", len(chunks))
	}
	for i := 0; i+1 < len(chunks); i++ {
		tail := strings.Join(lastWords(chunks[i], overlap), " ")
		if !strings.HasPrefix(chunks[i+1], tail) {
			t.Errorf("chunk %d does not start with the last %d words of chunk %d", i+1, overlap, i)
		}
	}
}

func TestSplitWithoutOverlapDoesNotRepeatTheTail(t *testing.T) {
	text := longText(6, 5, 7)

	chunks := chunk.Split(text, 40, 0)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want more than one", len(chunks))
	}
	for i := 0; i+1 < len(chunks); i++ {
		tail := strings.Join(lastWords(chunks[i], 10), " ")
		if strings.HasPrefix(chunks[i+1], tail) {
			t.Errorf("chunk %d repeats the tail of chunk %d although no overlap was asked for", i+1, i)
		}
	}
}

func TestSplitEmptyInputReturnsNil(t *testing.T) {
	if got := chunk.Split("", 40, 10); got != nil {
		t.Errorf("Split(\"\") = %v, want nil", got)
	}
	if got := chunk.Split("   \n\n\t ", 40, 10); got != nil {
		t.Errorf("Split(whitespace) = %v, want nil", got)
	}
}

func TestSplitShortTextReturnsOneChunk(t *testing.T) {
	const text = "A single short paragraph of prose."

	chunks := chunk.Split(text, 400, 60)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %v", len(chunks), chunks)
	}
	if chunks[0] != text {
		t.Errorf("chunk = %q, want %q", chunks[0], text)
	}
}

func TestSplitFallsBackToTheDefaultTarget(t *testing.T) {
	text := longText(40, 5, 7)

	chunks := chunk.Split(text, 0, 10)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want more than one", len(chunks))
	}
	for i, c := range chunks {
		if got := chunk.Tokens(c); got > chunk.DefaultTargetTokens {
			t.Errorf("chunk %d has %d words, want at most %d", i, got, chunk.DefaultTargetTokens)
		}
	}
}

func TestSplitWithAnOverlapAsLargeAsTheTargetStillMakesProgress(t *testing.T) {
	text := longText(4, 5, 7)

	chunks := chunk.Split(text, 20, 20)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want more than one", len(chunks))
	}
	for i, c := range chunks {
		if strings.TrimSpace(c) == "" {
			t.Errorf("chunk %d is empty", i)
		}
		if got := chunk.Tokens(c); got > 20 {
			t.Errorf("chunk %d has %d words, want at most 20", i, got)
		}
	}
}

func TestSplitSplitsAVeryLongSentence(t *testing.T) {
	words := make([]string, 120)
	for i := range words {
		words[i] = "word"
	}
	text := strings.Join(words, " ")

	chunks := chunk.Split(text, 30, 5)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want more than one", len(chunks))
	}
	for i, c := range chunks {
		if got := chunk.Tokens(c); got > 30 {
			t.Errorf("chunk %d has %d words, want at most 30", i, got)
		}
	}
}

func TestSplitKeepsParagraphBoundaries(t *testing.T) {
	const text = "First paragraph here.\n\nSecond paragraph here.\n\nThird paragraph here."

	chunks := chunk.Split(text, 400, 60)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %v", len(chunks), chunks)
	}
	if !strings.Contains(chunks[0], "\n\n") {
		t.Errorf("chunk lost its paragraph breaks: %q", chunks[0])
	}
}

func TestSplitKeepsAParagraphBreakInsideALongerChunk(t *testing.T) {
	text := longText(4, 5, 7)

	chunks := chunk.Split(text, 40, 10)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want more than one", len(chunks))
	}
	found := false
	for _, c := range chunks {
		if strings.Contains(c, "\n\n") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no chunk kept a paragraph break: %q", chunks)
	}
}

func TestSplitKeepsEverySentence(t *testing.T) {
	text := longText(4, 5, 7)
	chunks := chunk.Split(text, 40, 10)

	joined := strings.Join(chunks, "\n")
	for p := 0; p < 4; p++ {
		for s := 0; s < 5; s++ {
			sentence := "Sentence about topic " + strconv.Itoa(p) + " " + strconv.Itoa(s)
			if !strings.Contains(joined, sentence) {
				t.Errorf("sentence %q is missing from the chunks", sentence)
			}
		}
	}
}

func TestTokensCountsWords(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{text: "", want: 0},
		{text: "   ", want: 0},
		{text: "one", want: 1},
		{text: "one two three", want: 3},
		{text: "one\ntwo\n\nthree", want: 3},
		{text: "  spaced   out  ", want: 2},
	}
	for _, tc := range cases {
		if got := chunk.Tokens(tc.text); got != tc.want {
			t.Errorf("Tokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}
