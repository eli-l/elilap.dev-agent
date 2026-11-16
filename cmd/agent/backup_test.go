package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/requests"
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
	code := runBackup([]string{"--db", dbPath, "--out", out}, envcfg.Config{DBPath: dbPath}, &stdout, &stderr)
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
	code := runBackup([]string{"--db", dbPath}, envcfg.Config{DBPath: dbPath}, &stdout, &stderr)
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
	code := runBackup([]string{"--db", dbPath, "--out", out}, envcfg.Config{DBPath: dbPath}, &stdout, &stderr)
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
	code := runBackup([]string{"--db", dbPath, "--out", out}, envcfg.Config{DBPath: dbPath}, &stdout, &stderr)
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

// TestBackupSnapshotsTheRequestStore pins the pairing: the request store is
// original data, resolved from AGENT_REQUESTS_DB_PATH (rtCfg) rather than from
// the index directory, and it lands beside the index snapshot.
func TestBackupSnapshotsTheRequestStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedBackupIndex(t)
	reqPath := filepath.Join(dir, "requests.db")
	reqStore, err := requests.Open(reqPath)
	if err != nil {
		t.Fatalf("open requests: %v", err)
	}
	if _, _, err := reqStore.Create(requests.CreateInput{Question: "q", Principal: "agent:a"}); err != nil {
		t.Fatalf("create request: %v", err)
	}
	reqStore.Close()

	out := filepath.Join(dir, "index-backup.db")
	var stdout, stderr bytes.Buffer
	code := runBackup([]string{"--db", dbPath, "--out", out},
		envcfg.Config{DBPath: dbPath, RequestsDBPath: reqPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("backup exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "requests-backup:") {
		t.Errorf("backup output does not report the request snapshot: %s", stdout.String())
	}
	matches, err := filepath.Glob(filepath.Join(dir, "requests-*.db"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("requests snapshots = %v, %v; want one", matches, err)
	}
	copied, err := requests.Open(matches[0])
	if err != nil {
		t.Fatalf("open request snapshot: %v", err)
	}
	defer copied.Close()
	if n, err := copied.Pending(); err != nil || n != 1 {
		t.Errorf("snapshot pending = %d, %v; want 1", n, err)
	}
}

// TestBackupRequestSnapshotNamesAreUnique pins the pairing when two snapshots
// are taken in the same second: the request snapshot gets its own free name
// instead of failing after the index snapshot already succeeded.
func TestBackupRequestSnapshotNamesAreUnique(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedBackupIndex(t)
	reqStore, err := requests.Open(filepath.Join(dir, "requests.db"))
	if err != nil {
		t.Fatalf("open requests: %v", err)
	}
	reqStore.Close()

	cfg := envcfg.Config{DBPath: dbPath, RequestsDBPath: filepath.Join(dir, "requests.db")}
	for _, name := range []string{"a.db", "b.db"} {
		var stdout, stderr bytes.Buffer
		if code := runBackup([]string{"--db", dbPath, "--out", filepath.Join(dir, name)}, cfg,
			&stdout, &stderr); code != 0 {
			t.Fatalf("backup %s exit = %d, stderr = %s", name, code, stderr.String())
		}
	}
	matches, err := filepath.Glob(filepath.Join(dir, "requests-*.db"))
	if err != nil || len(matches) != 2 {
		t.Errorf("request snapshots = %v, %v; want two distinct files", matches, err)
	}
}

// TestUniqueBackupPathSurvivesAnUnprobeableName pins the non-ENOENT path: an
// output directory that cannot be stat'ed must return a candidate (so Backup
// reports the error) instead of minting names forever.
func TestUniqueBackupPathSurvivesAnUnprobeableName(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can stat inside a 0000 directory")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	done := make(chan string, 1)
	go func() { done <- uniqueBackupPath(locked, "requests-stamp") }()
	select {
	case got := <-done:
		if got == "" {
			t.Error("uniqueBackupPath returned an empty path")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("uniqueBackupPath spun on an unprobeable directory")
	}
}

func TestRunBackupRejectsExtraArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runBackup([]string{"extra"}, envcfg.Config{DBPath: "index.db"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
