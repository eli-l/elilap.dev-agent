package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// seedBackupIndex writes one chunk into a fresh index and returns its path.
func seedBackupIndex(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	st, err := store.Open(dbPath, filepath.Join("..", "..", "migrations"), store.DefaultEmbeddingDims)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	chunk := store.Chunk{
		ChunkID: "c1", SourceKind: "ghost_post", SourceID: "uuid-1", Author: "Eli",
		PublishedAt: at, UpdatedAt: at, ContentHash: "hash-c1",
		Title: "t", Body: "body", BodyTokens: 1,
	}
	if _, _, err := st.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"},
		[]store.Chunk{chunk}); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	return dbPath
}

func TestRunBackupWritesSnapshot(t *testing.T) {
	dbPath := seedBackupIndex(t)
	out := filepath.Join(t.TempDir(), "snap.db")

	var stdout, stderr bytes.Buffer
	code := runBackup([]string{"--db", dbPath, "--out", out}, dbPath, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "backup: "+out) {
		t.Errorf("stdout = %q, want the backup path", stdout.String())
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Size() == 0 {
		t.Error("backup is empty")
	}

	copy, err := store.OpenReadOnly(out)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copy.Close()
	got, err := copy.Stats()
	if err != nil {
		t.Fatalf("stats backup: %v", err)
	}
	if got.Chunks != 1 || got.FTSRows != 1 {
		t.Errorf("backup stats = %+v; want 1 chunk and 1 fts row", got)
	}
}

func TestRunBackupDefaultPathSitsNextToIndex(t *testing.T) {
	dbPath := seedBackupIndex(t)
	var stdout, stderr bytes.Buffer
	code := runBackup([]string{"--db", dbPath}, dbPath, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	var out string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "backup: "); ok {
			out = rest
		}
	}
	if out == "" {
		t.Fatalf("stdout = %q, want a backup path", stdout.String())
	}
	if filepath.Dir(out) != filepath.Dir(dbPath) {
		t.Errorf("default backup dir = %q, want %q", filepath.Dir(out), filepath.Dir(dbPath))
	}
	if !strings.HasPrefix(filepath.Base(out), "index-") || !strings.HasSuffix(out, ".db") {
		t.Errorf("default backup name = %q, want index-<stamp>.db", filepath.Base(out))
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("stat default backup: %v", err)
	}
}

func TestRunBackupMissingIndexLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "missing.db")
	out := filepath.Join(dir, "snap.db")
	var stdout, stderr bytes.Buffer
	code := runBackup([]string{"--db", dbPath, "--out", out}, dbPath, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "does not exist") {
		t.Errorf("stderr = %q, want a missing-index error", stderr.String())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("backup file was created for a missing index (stat err = %v)", err)
	}
}

func TestRunBackupRefusesExistingDestination(t *testing.T) {
	dbPath := seedBackupIndex(t)
	out := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(out, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runBackup([]string{"--db", dbPath, "--out", out}, dbPath, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("stderr = %q, want an exists error", stderr.String())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(body) != "keep me" {
		t.Errorf("destination was modified: %q", body)
	}
}

func TestRunBackupRejectsExtraArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runBackup([]string{"extra"}, "index.db", &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
