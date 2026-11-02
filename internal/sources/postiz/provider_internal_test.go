package postiz

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTitleOfUsesTheFirstNonEmptyLine(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "first line wins", content: "First line\nSecond line", want: "First line"},
		{name: "leading blank lines are skipped", content: "\n\n  Real title  \nbody", want: "Real title"},
		{name: "empty content has no title", content: "   \n\t\n", want: ""},
		{
			name:    "long line trims at a word boundary",
			content: strings.Repeat("word ", 40),
			want:    strings.TrimSpace(strings.Repeat("word ", 24)),
		},
		{
			// Regression: the word-boundary guard compared a byte offset with
			// a rune count, so multi-byte titles were cut far too early. The
			// exact cut depends on the last space before the 120-rune limit;
			// what matters is that it stays near the limit and on a word.
			name:    "multi-byte line keeps its rune budget",
			content: strings.Repeat("привет мир ", 20),
			want:    "multi-byte",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := titleOf(tc.content)
			if tc.want == "multi-byte" {
				if !strings.HasPrefix(tc.content, got) {
					t.Fatalf("titleOf() = %q, want a prefix of the content", got)
				}
				if trimmed := strings.TrimRight(got, " "); trimmed != got {
					t.Errorf("titleOf() = %q, want no trailing space", got)
				}
				if n := len([]rune(got)); n < 100 || n > titleMaxRunes {
					t.Errorf("titleOf() kept %d runes, want between 100 and %d", n, titleMaxRunes)
				}
				if !strings.HasSuffix(got, "мир") && !strings.HasSuffix(got, "привет") {
					t.Errorf("titleOf() = %q, want it cut at a word boundary", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("titleOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTruncateRunesRespectsRuneLimit(t *testing.T) {
	// 200 runes of 2-byte text: the limit is 120 runes, and every boundary
	// decision must stay in runes so no character is split.
	got := truncateRunes(strings.Repeat("я", 200), 120)
	if n := len([]rune(got)); n != 120 {
		t.Errorf("truncateRunes() kept %d runes, want 120", n)
	}
	for _, r := range got {
		if r != 'я' {
			t.Fatalf("truncateRunes() produced a broken rune %q", r)
		}
	}
}

func TestClientReportsAnOversizedResponse(t *testing.T) {
	// Shrink the cap so the test does not need a 64 MB body; the check
	// exists to tell "too large" apart from "malformed JSON".
	orig := maxResponseBytes
	maxResponseBytes = 16
	t.Cleanup(func() { maxResponseBytes = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"posts":[`+strings.Repeat(" ", 64)+`]}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "key", srv.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c.ListPosts(context.Background(), time.Unix(0, 0), time.Now())
	if err == nil {
		t.Fatal("ListPosts(oversized body) = nil error, want a size error")
	}
	if !strings.Contains(err.Error(), "response exceeds 16 bytes") {
		t.Errorf("ListPosts() error = %q, want it to name the size limit", err)
	}
}
