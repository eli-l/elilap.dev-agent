package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// runEmbedTest invokes the command with a throwaway environment and returns
// its exit code plus captured stdout/stderr.
func runEmbedTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runEmbed(args, envcfg.Config{
		DBPath:     filepath.Join(t.TempDir(), "unused.db"),
		EmbedModel: envcfg.DefaultEmbedModel,
	}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunEmbedRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "batch", args: []string{"--batch", "0"}, want: "--batch must be positive"},
		{name: "negative limit", args: []string{"--limit", "-1"}, want: "--limit must not be negative"},
		{name: "timeout", args: []string{"--timeout", "0s"}, want: "--timeout must be positive"},
		{name: "positional", args: []string{"nonsense"}, want: "unexpected argument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runEmbedTest(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2 (usage error)", code)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
		})
	}
}

func TestRunEmbedDryRunRequiresIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	var stdout, stderr bytes.Buffer
	code := runEmbed([]string{"--db", dbPath, "--dry-run"},
		envcfg.Config{DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 for a missing index", code)
	}
	// A dry run must never create or migrate the database it is inspecting.
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want the file to remain absent", dbPath, err)
	}
	if !strings.Contains(stderr.String(), "needs an existing index") {
		t.Errorf("stderr = %q, want the missing-index hint", stderr.String())
	}
}

func TestRunEmbedDryRunLimitCapsPending(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	st, err := store.Open(dbPath, filepath.Join("..", "..", "migrations"), store.DefaultEmbeddingDims)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	chunks := make([]store.Chunk, 3)
	for i := range chunks {
		chunks[i] = store.Chunk{
			ChunkID: "c" + string(rune('0'+i)), SourceKind: "ghost_post", SourceID: "uuid-1",
			Author: "Eli", PublishedAt: now, UpdatedAt: now,
			ContentHash: "hash", Body: "body", BodyTokens: 1,
		}
	}
	if _, _, err := st.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}, chunks); err != nil {
		t.Fatalf("seed: %v", err)
	}
	st.Close()

	var stdout, stderr bytes.Buffer
	code := runEmbed([]string{"--db", dbPath, "--dry-run", "--limit", "2"},
		envcfg.Config{DBPath: dbPath, EmbedModel: envcfg.DefaultEmbedModel}, &stdout, &stderr)
	// The dry run opens read-only and never needs the model host.
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pending:    2") {
		t.Errorf("stdout = %q, want the pending count capped at --limit 2", stdout.String())
	}
}
