package main

import (
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
)

func TestFormatProgress(t *testing.T) {
	tests := []struct {
		name              string
		total, done, busy int
		elapsed           time.Duration
		want              []string
		absent            []string
	}{
		{
			name:  "in flight batch",
			total: 886, done: 128, busy: 32,
			elapsed: 12 * time.Second,
			want:    []string{"128/886 chunks (14%)", "32 in flight", "elapsed 12.0s", "10.7 chunks/s", "eta 1m11s"},
		},
		{
			name:  "between batches",
			total: 100, done: 50, busy: 0,
			elapsed: 2 * time.Minute,
			want:    []string{"50/100 chunks (50%)", "elapsed 2m00s", "0.4 chunks/s"},
			absent:  []string{"in flight"},
		},
		{
			name:  "complete",
			total: 25, done: 25, busy: 0,
			elapsed: 90 * time.Second,
			want:    []string{"done 25/25 chunks in 1m30s", "0.3 chunks/s"},
			absent:  []string{"eta"},
		},
		{
			name:  "nothing to do",
			total: 0,
			want:  []string{"nothing to do"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatProgress(tc.total, tc.done, tc.busy, tc.elapsed)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("formatProgress = %q, want it to contain %q", got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("formatProgress = %q, want it to omit %q", got, absent)
				}
			}
		})
	}
}

func TestProgressPrinterPadsAndTerminates(t *testing.T) {
	var buf strings.Builder
	p := newProgressPrinter(&buf)
	p.update(embed.Progress{Total: 100, Done: 90, InFlight: 5})
	p.update(embed.Progress{Total: 100, Done: 100}) // shorter line must erase the tail
	p.stop()

	out := buf.String()
	if !strings.HasPrefix(out, "\r") {
		t.Errorf("output = %q, want it to start with a carriage return", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output = %q, want it to end with a newline", out)
	}
	if strings.Count(out, "\r") < 2 {
		t.Errorf("output = %q, want one redraw per update", out)
	}
	if strings.Count(out, "done 100/100") != 1 {
		t.Errorf("output = %q, want the final state rendered once", out)
	}
}
