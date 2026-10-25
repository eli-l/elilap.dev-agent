package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// TestIngestLedgerLifecycle walks the ledger-driven ingest against a
// temporary index and the local markdown provider:
//
//  1. the corpus is imported and the chunks are written;
//  2. the second run writes nothing: every item is unchanged;
//  3. a file is shortened, and the chunks it no longer produces are gone;
//  4. a dry run names the change it would make and changes nothing;
//  5. a file is deleted, and its chunks and its ledger row go with it;
//  6. a full run re-fetches and re-chunks everything.
//
// No network, no credential and no live API are involved: the corpus is a
// directory of markdown files in a temporary directory.
func TestIngestLedgerLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	corpus := filepath.Join(dir, "corpus")
	if err := os.MkdirAll(corpus, 0o750); err != nil {
		t.Fatalf("create the corpus directory: %v", err)
	}
	alphaPath := filepath.Join(corpus, "alpha.md")
	betaPath := filepath.Join(corpus, "beta.md")
	writeCorpusFile(t, alphaPath, longDocument(8))
	writeCorpusFile(t, betaPath, "# Beta Report\n\nbeta caching notes\n")

	configPath := filepath.Join(dir, "sources.yaml")
	config := fmt.Sprintf("version: 1\n\ndefaults:\n  chunking: { target_tokens: 400, overlap_tokens: 60 }\n\nsources:\n  - id: files\n    type: files\n    enabled: true\n    options: { dir: %q }\n", corpus)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write the fixture config: %v", err)
	}

	dbPath := filepath.Join(dir, "index.db")
	t.Setenv("AGENT_DB_PATH", dbPath)

	// 1. The first run imports the corpus.
	code, out := runCLI(t, "ingest", "--config", configPath)
	if code != 0 {
		t.Fatalf("ingest exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "2 item(s) seen") {
		t.Errorf("the ingest did not report the items it saw:\n%s", out)
	}
	if !strings.Contains(out, "2 item(s) fetched") {
		t.Errorf("the ingest did not report the items it fetched:\n%s", out)
	}
	if !strings.Contains(out, "5 chunk(s) written") {
		t.Errorf("the ingest did not report the chunks it wrote:\n%s", out)
	}

	first := readIndex(t, dbPath)
	if first.Chunks != 5 {
		t.Errorf("the index holds %d chunks, want 5", first.Chunks)
	}
	if len(first.Items) != 2 {
		t.Errorf("the ledger holds %d items, want 2", len(first.Items))
	}
	if !first.HasState {
		t.Errorf("the ledger holds no state for the source")
	} else if first.State.ItemCount != 2 {
		t.Errorf("the ledger state counts %d items, want 2", first.State.ItemCount)
	}
	if first.State.LastFullAt != "" {
		t.Errorf("the ledger state records a full run at %q, want none", first.State.LastFullAt)
	}
	betaChunks := itemChunks(t, dbPath, "beta.md")
	if len(betaChunks) != 1 {
		t.Fatalf("the index holds %d chunks for beta.md, want 1", len(betaChunks))
	}
	betaHash := betaChunks[0].ContentHash

	// 2. The second run writes nothing: the ledger says every item is
	// unchanged, so no item is even fetched.
	code, out = runCLI(t, "ingest", "--config", configPath)
	if code != 0 {
		t.Fatalf("the second ingest exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "0 item(s) fetched") {
		t.Errorf("the second ingest fetched an item it did not have to:\n%s", out)
	}
	if !strings.Contains(out, "0 chunk(s) written") {
		t.Errorf("the second ingest wrote a chunk:\n%s", out)
	}
	if !strings.Contains(out, "2 item(s) unchanged") {
		t.Errorf("the second ingest did not report the items as unchanged:\n%s", out)
	}
	second := readIndex(t, dbPath)
	if second.Chunks != first.Chunks {
		t.Errorf("the second ingest changed the index: %d chunks, want %d", second.Chunks, first.Chunks)
	}

	// 3. One file is shortened. The chunks it no longer produces have to
	// go: they would otherwise keep matching queries with a text the
	// file does not hold any more.
	writeCorpusFile(t, alphaPath, "# Alpha Report\n\nmarmalade now much shorter\n")
	code, out = runCLI(t, "ingest", "--config", configPath)
	if code != 0 {
		t.Fatalf("the ingest after the edit exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "removed 3 stale chunk(s) for alpha.md") {
		t.Errorf("the ingest did not report the stale chunks it removed:\n%s", out)
	}
	shortened := readIndex(t, dbPath)
	if shortened.Chunks != 2 {
		t.Errorf("the index holds %d chunks after the edit, want 2 (one per file)", shortened.Chunks)
	}
	alphaChunks := itemChunks(t, dbPath, "alpha.md")
	if len(alphaChunks) != 1 {
		t.Errorf("the index holds %d chunks for the shortened file, want 1", len(alphaChunks))
	}
	betaChunks = itemChunks(t, dbPath, "beta.md")
	if len(betaChunks) != 1 {
		t.Errorf("the index holds %d chunks for beta.md, want 1: the other item was touched", len(betaChunks))
	} else if betaChunks[0].ContentHash != betaHash {
		t.Errorf("the chunk of beta.md was rewritten: %q, want %q", betaChunks[0].ContentHash, betaHash)
	}
	hits := searchBM25(t, dbPath, "xylophone")
	if len(hits) != 0 {
		t.Errorf("the text of the shortened file is still searchable: %d hits", len(hits))
	}

	// 4. A dry run names the change it would make and changes nothing.
	writeCorpusFile(t, betaPath, "# Beta Report\n\nbeta caching notes, revised\n")
	beforeDryRun := readIndex(t, dbPath)
	code, out = runCLI(t, "ingest", "--config", configPath, "--dry-run")
	if code != 0 {
		t.Fatalf("the dry run exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "would write 1 chunk(s) for beta.md") {
		t.Errorf("the dry run did not name the change it would make:\n%s", out)
	}
	afterDryRun := readIndex(t, dbPath)
	if afterDryRun.Chunks != beforeDryRun.Chunks {
		t.Errorf("the dry run changed the index: %d chunks, want %d", afterDryRun.Chunks, beforeDryRun.Chunks)
	}
	if !reflect.DeepEqual(afterDryRun.Items, beforeDryRun.Items) {
		t.Errorf("the dry run changed the ledger:\n before: %+v\n after:  %+v",
			beforeDryRun.Items, afterDryRun.Items)
	}

	// 5. A file is deleted: its chunks and its ledger row go with it.
	if err := os.Remove(betaPath); err != nil {
		t.Fatalf("remove the file: %v", err)
	}
	code, out = runCLI(t, "ingest", "--config", configPath)
	if code != 0 {
		t.Fatalf("the ingest after the deletion exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "removed the item beta.md") {
		t.Errorf("the ingest did not report the item it removed:\n%s", out)
	}
	deleted := readIndex(t, dbPath)
	if deleted.Chunks != 1 {
		t.Errorf("the index holds %d chunks after the deletion, want 1", deleted.Chunks)
	}
	if len(deleted.Items) != 1 {
		t.Errorf("the ledger holds %d items after the deletion, want 1", len(deleted.Items))
	}
	if deleted.Items[0].ItemID != "alpha.md" {
		t.Errorf("the ledger holds %q after the deletion, want alpha.md", deleted.Items[0].ItemID)
	}
	if deleted.State.ItemCount != 1 {
		t.Errorf("the ledger state counts %d items, want 1", deleted.State.ItemCount)
	}

	// 6. A full run re-fetches and re-chunks everything, and the ledger
	// records that a full run happened.
	code, out = runCLI(t, "ingest", "--config", configPath, "--full")
	if code != 0 {
		t.Fatalf("the full ingest exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "1 item(s) fetched") {
		t.Errorf("the full run did not fetch the item:\n%s", out)
	}
	if !strings.Contains(out, "1 chunk(s) written") {
		t.Errorf("the full run did not rewrite the chunk:\n%s", out)
	}
	full := readIndex(t, dbPath)
	if full.Chunks != 1 {
		t.Errorf("the index holds %d chunks after the full run, want 1", full.Chunks)
	}
	if full.State.LastFullAt == "" {
		t.Errorf("the ledger state records no full run")
	}
	_ = ctx
}

// indexState is what the index and the ledger hold after a run.
type indexState struct {
	// Chunks is the number of chunks in the index.
	Chunks int
	// Vectors is the number of vectors in the index.
	Vectors int
	// Items are the ledger rows of the "files" source.
	Items []store.SourceItem
	// State is the ledger row of the "files" source.
	State store.SourceState
	// HasState says whether the ledger holds a row for the source.
	HasState bool
}

// readIndex reads the index and the ledger of the "files" source.
func readIndex(t *testing.T, dbPath string) indexState {
	t.Helper()
	ctx := context.Background()

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open the index: %v", err)
	}
	defer s.Close()

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	items, err := s.SourceItems(ctx, "files")
	if err != nil {
		t.Fatalf("SourceItems: %v", err)
	}
	state, ok, err := s.SourceState(ctx, "files")
	if err != nil {
		t.Fatalf("SourceState: %v", err)
	}
	return indexState{
		Chunks:   stats.Chunks,
		Vectors:  stats.Vectors,
		Items:    items,
		State:    state,
		HasState: ok,
	}
}

// itemChunks returns the chunks the index holds for one item.
func itemChunks(t *testing.T, dbPath, itemID string) []store.Chunk {
	t.Helper()
	ctx := context.Background()

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open the index: %v", err)
	}
	defer s.Close()

	chunks, err := s.ListChunks(ctx, 0, false)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	var out []store.Chunk
	for _, c := range chunks {
		if c.SourceID == itemID {
			out = append(out, c)
		}
	}
	return out
}

// searchBM25 searches the index for a word.
func searchBM25(t *testing.T, dbPath, query string) []store.Hit {
	t.Helper()
	ctx := context.Background()

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open the index: %v", err)
	}
	defer s.Close()

	hits, err := s.SearchBM25(ctx, query, 10)
	if err != nil {
		t.Fatalf("SearchBM25: %v", err)
	}
	return hits
}

// longDocument is a markdown document with enough paragraphs to be split
// into several chunks. Its first paragraph carries a word no other document
// has, so a test can prove that the text is gone from the index.
func longDocument(paragraphs int) string {
	var b strings.Builder
	b.WriteString("# Alpha Report\n\n")
	for i := 0; i < paragraphs; i++ {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if i == 0 {
			b.WriteString("xylophone ")
		}
		for j := 0; j < 120; j++ {
			fmt.Fprintf(&b, "filler%d ", j)
		}
	}
	return b.String()
}
