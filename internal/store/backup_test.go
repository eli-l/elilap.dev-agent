package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

func TestBackupFromReadOnlyHandleCopiesIndex(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "index.db")

	w, err := store.Open(src, "../../migrations")
	if err != nil {
		t.Fatalf("open writable store: %v", err)
	}
	k := store.ItemKey{Kind: "ghost_post", ID: "uuid-1"}
	if _, _, err := w.ReplaceItemChunks(k, []store.Chunk{testChunk("c1")}); err != nil {
		w.Close()
		t.Fatalf("seed chunk: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writable store: %v", err)
	}

	ro, err := store.OpenReadOnly(src)
	if err != nil {
		t.Fatalf("open read-only store: %v", err)
	}
	defer ro.Close()

	// The default CLI layout puts the snapshot next to the index, so exercise
	// a fresh nested directory as well.
	dest := filepath.Join(dir, "backups", "index-20261111T120000Z.db")
	if err := ro.Backup(dest); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	for _, sibling := range []string{dest + "-wal", dest + "-shm"} {
		if _, err := os.Stat(sibling); !os.IsNotExist(err) {
			t.Errorf("backup left %s (stat err = %v); VACUUM INTO should be self-contained", sibling, err)
		}
	}

	copy, err := store.OpenReadOnly(dest)
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

func TestBackupRefusesExistingDestination(t *testing.T) {
	s := newTestStore(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(dest, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	err := s.Backup(dest)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Backup = %v; want an exists error", err)
	}
	body, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("read destination: %v", readErr)
	}
	if string(body) != "not a database" {
		t.Errorf("destination was modified: %q", body)
	}
}

func TestBackupRejectsDirectoryAndEmptyPath(t *testing.T) {
	s := newTestStore(t)
	if err := s.Backup(t.TempDir()); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("Backup(directory) = %v; want a directory error", err)
	}
	if err := s.Backup(""); err == nil {
		t.Error("Backup(\"\") = nil; want an error")
	}
}
