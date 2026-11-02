package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/ingest"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

var testSince = time.Date(2026, 11, 1, 20, 11, 31, 0, time.UTC)

// nearNoOp is the rerun that used to read "indexed 2 item(s) ... fetched 0":
// the candidates were listed, nothing was written.
var nearNoOp = ingest.Result{
	SourceID: "postiz", Kind: "postiz_post", Since: testSince,
	Listed: 2, Fetched: 0, Unchanged: 2,
}

func TestPrintIngestResultsText(t *testing.T) {
	tests := []struct {
		name   string
		result ingest.Result
		dryRun bool
		want   string
	}{
		{
			name:   "near no-op rerun",
			result: nearNoOp,
			want:   "postiz: since 2026-11-01T20:11:31Z: listed 2, indexed 0, unchanged 2, chunks +0/~0, removed 0",
		},
		{
			name: "unbounded full run",
			result: ingest.Result{
				SourceID: "ghost", Kind: "ghost_post", Listed: 126, Fetched: 126, Added: 588,
			},
			want: "ghost: since all: listed 126, indexed 126, unchanged 0, chunks +588/~0, removed 0",
		},
		{
			name: "drift removal",
			result: ingest.Result{
				SourceID: "ghost", Kind: "ghost_post", Listed: 1, Fetched: 1, Added: 3, Updated: 2, Removed: 2,
				Planned: []string{"a", "-b", "-c"},
			},
			want: "ghost: since all: listed 1, indexed 1, unchanged 0, chunks +3/~2, removed 2",
		},
		{
			name: "dry run shows the plan against the listing",
			result: ingest.Result{
				SourceID: "postiz", Kind: "postiz_post", Since: testSince,
				Listed: 8, Planned: []string{"a", "b", "-c"},
			},
			dryRun: true,
			want:   "postiz: would index 2 of 8 listed item(s) since 2026-11-01T20:11:31Z, remove 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printIngestResults(&buf, []ingest.Result{tc.result}, tc.dryRun, logging.FormatText)
			got := strings.TrimSpace(buf.String())
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("summary = %q, want it to start with %q", got, tc.want)
			}
		})
	}
}

func TestPrintIngestResultsJSON(t *testing.T) {
	tests := []struct {
		name       string
		result     ingest.Result
		dryRun     bool
		want       map[string]any
		wantAbsent []string
	}{
		{
			name:   "near no-op rerun",
			result: nearNoOp,
			want: map[string]any{
				"source_id": "postiz", "source_kind": "postiz_post",
				"since":  "2026-11-01T20:11:31Z",
				"listed": 2.0, "indexed": 0.0, "unchanged": 2.0,
				"chunks_added": 0.0, "chunks_updated": 0.0, "removed": 0.0,
				"dry_run": false,
			},
		},
		{
			// Result.Planned is filled on every run, but a real run's line
			// must not carry the whole listing as if it were a plan.
			name: "a real run does not report a plan",
			result: ingest.Result{
				SourceID: "ghost", Kind: "ghost_post", Listed: 2, Fetched: 2, Added: 5,
				Planned: []string{"a", "b"},
			},
			want:       map[string]any{"source_id": "ghost", "listed": 2.0, "indexed": 2.0, "dry_run": false},
			wantAbsent: []string{"planned", "planned_removals"},
		},
		{
			name: "unbounded listing has a null since",
			result: ingest.Result{
				SourceID: "ghost", Kind: "ghost_post", Listed: 126, Fetched: 126, Added: 588,
			},
			want: map[string]any{
				"source_id": "ghost", "source_kind": "ghost_post", "since": nil,
				"listed": 126.0, "indexed": 126.0,
				"chunks_added": 588.0, "dry_run": false,
			},
		},
		{
			name: "dry run carries the plan",
			result: ingest.Result{
				SourceID: "postiz", Kind: "postiz_post", Since: testSince,
				Listed: 8, Planned: []string{"a", "b", "-c"},
			},
			dryRun: true,
			want: map[string]any{
				"source_id": "postiz", "listed": 8.0, "indexed": 0.0, "dry_run": true,
				"planned": []any{"a", "b"}, "planned_removals": []any{"c"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printIngestResults(&buf, []ingest.Result{tc.result}, tc.dryRun, logging.FormatJSON)

			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("JSON summary = %d line(s), want one object per source: %q", len(lines), buf.String())
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
				t.Fatalf("summary is not JSON: %v (%q)", err, lines[0])
			}
			for key, want := range tc.want {
				if !equalJSON(got[key], want) {
					t.Errorf("%s = %#v, want %#v (line: %s)", key, got[key], want, lines[0])
				}
			}
			for _, key := range tc.wantAbsent {
				if _, ok := got[key]; ok {
					t.Errorf("summary has %q on a non-dry run: %s", key, lines[0])
				}
			}
		})
	}
}

// equalJSON compares decoded JSON values, which are map/slice/float/string/nil.
func equalJSON(got, want any) bool {
	switch w := want.(type) {
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !equalJSON(g[i], w[i]) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}
