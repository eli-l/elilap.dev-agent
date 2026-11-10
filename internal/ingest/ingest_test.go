package ingest_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/ingest"
	"github.com/eli-l/elilap.dev-agent/internal/sources"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// fakeProvider serves a fixed list and counts fetches; it is the seam that
// keeps ingest tests offline.
type fakeProvider struct {
	items   []shared.Item
	docs    map[string]shared.Document
	fetches int
}

func (f *fakeProvider) Type() string { return "ghost" }

func (f *fakeProvider) List(context.Context, time.Time) ([]shared.Item, error) {
	return f.items, nil
}

func (f *fakeProvider) Fetch(_ context.Context, it shared.Item) (shared.Document, error) {
	f.fetches++
	return f.docs[it.ID], nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"), "../../migrations")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newRunner wires a fake "ghost" provider into a real store and registry.
func newRunner(t *testing.T, fp *fakeProvider) (*ingest.Runner, *config.Config) {
	t.Helper()
	r := sources.NewRegistry()
	if err := r.Register("ghost", func(config.Source) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("register parser: %v", err)
	}
	if err := r.RegisterConstructor("ghost", func(config.Source, any, shared.BuildContext) (shared.Provider, error) {
		return fp, nil
	}); err != nil {
		t.Fatalf("register constructor: %v", err)
	}
	cfg := &config.Config{Sources: []config.Source{{ID: "ghost", Type: "ghost", Enabled: true}}}
	return &ingest.Runner{Store: newTestStore(t), Registry: r}, cfg
}

func testItem(id string) shared.Item {
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	return shared.Item{ID: id, Ref: "ref-" + id, URL: "https://example.com/" + id, LastModified: at}
}

func testDoc(title string) shared.Document {
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	return shared.Document{
		Title:       title,
		HTML:        "<h2>Intro</h2><p>" + strings.Repeat("word ", 400) + "</p>",
		Body:        strings.Repeat("word ", 400),
		PublishedAt: at,
		UpdatedAt:   at,
		Author:      "Eli",
		Tags:        []string{"go"},
		Meta:        map[string]any{"slug": title},
	}
}

func testOptions() ingest.Options {
	return ingest.Options{
		Chunking: shared.ChunkerConfig{TargetTokens: 100, OverlapTokens: 0},
		Now:      func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) },
		Overlap:  time.Hour,
	}
}

func listChunks(t *testing.T, s *store.Store, id string) int {
	t.Helper()
	c, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	_ = c
	// Stats is index-wide; the tests use one item at a time, so chunks is
	// the per-item count.
	return c.Chunks
}

func TestRunIndexesAndSkipsUnchanged(t *testing.T) {
	ctx := context.Background()
	fp := &fakeProvider{
		items: []shared.Item{testItem("a")},
		docs:  map[string]shared.Document{"a": testDoc("Post A")},
	}
	runner, cfg := newRunner(t, fp)

	results, err := runner.Run(ctx, cfg.Sources, testOptions())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if fp.fetches != 1 || results[0].Added == 0 || results[0].Fetched != 1 {
		t.Fatalf("first run = %+v (fetches %d), want one fetched item with chunks", results[0], fp.fetches)
	}
	if got := listChunks(t, runner.Store, "a"); got != results[0].Added {
		t.Fatalf("stored chunks = %d, want %d", got, results[0].Added)
	}

	afterFirst := fp.fetches
	results, err = runner.Run(ctx, cfg.Sources, testOptions())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if fp.fetches != afterFirst {
		t.Errorf("second run fetched %d more items, want 0 (unchanged ledger row)", fp.fetches-afterFirst)
	}
	if results[0].Fetched != 0 {
		t.Errorf("second run fetched %d, want 0", results[0].Fetched)
	}
}

func TestWindowedRunDoesNotDeleteDrift(t *testing.T) {
	ctx := context.Background()
	fp := &fakeProvider{
		items: []shared.Item{testItem("a")},
		docs:  map[string]shared.Document{"a": testDoc("Post A")},
	}
	runner, cfg := newRunner(t, fp)
	if _, err := runner.Run(ctx, cfg.Sources, testOptions()); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// The windowed list is empty; that must not be read as "deleted upstream".
	fp.items = nil
	opts := testOptions()
	opts.SinceSet = true
	opts.Since = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	results, err := runner.Run(ctx, cfg.Sources, opts)
	if err != nil {
		t.Fatalf("windowed run: %v", err)
	}
	if results[0].Removed != 0 {
		t.Errorf("windowed run removed %d items, want 0", results[0].Removed)
	}
	if got := listChunks(t, runner.Store, "a"); got == 0 {
		t.Fatal("windowed run deleted the indexed chunks")
	}

	// An incremental run is still bounded by its watermark, so an item
	// outside the window is not drift.
	results, err = runner.Run(ctx, cfg.Sources, testOptions())
	if err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	if results[0].Removed != 0 {
		t.Errorf("incremental run removed %d items, want 0 (bounded list)", results[0].Removed)
	}

	// Only an unbounded full list may conclude that the item is gone.
	full := testOptions()
	full.Full = true
	results, err = runner.Run(ctx, cfg.Sources, full)
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	if results[0].Removed != 1 {
		t.Errorf("full run removed %d items, want 1", results[0].Removed)
	}
	if got := listChunks(t, runner.Store, "a"); got != 0 {
		t.Errorf("chunks after drift removal = %d, want 0", got)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	fp := &fakeProvider{
		items: []shared.Item{testItem("a")},
		docs:  map[string]shared.Document{"a": testDoc("Post A")},
	}
	runner, cfg := newRunner(t, fp)
	opts := testOptions()
	opts.DryRun = true
	results, err := runner.Run(context.Background(), cfg.Sources, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(results[0].Planned) != 1 || results[0].Planned[0] != "a" {
		t.Errorf("planned = %v, want [a]", results[0].Planned)
	}
	if fp.fetches != 0 {
		t.Errorf("dry run fetched %d items, want 0", fp.fetches)
	}
	if got := listChunks(t, runner.Store, "a"); got != 0 {
		t.Errorf("dry run wrote %d chunks, want 0", got)
	}
}

func TestBuildChunksIDsAndHashes(t *testing.T) {
	doc := testDoc("Post A")
	chunks, err := ingest.BuildChunks(doc, "ghost_post", "item-1", "https://example.com/a",
		shared.ChunkerConfig{TargetTokens: 100})
	if err != nil {
		t.Fatalf("BuildChunks() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("BuildChunks() returned %d chunks, want the body split", len(chunks))
	}
	for i, c := range chunks {
		if want := shared.ChunkID("ghost_post", "item-1", i); c.ChunkID != want {
			t.Errorf("chunk %d id = %s, want %s", i, c.ChunkID, want)
		}
		if c.ContentHash != shared.ContentHash(c.Section+"\n"+c.Body) {
			t.Errorf("chunk %d hash does not match its body", i)
		}
		if c.Author != "Eli" || c.PublishedAt.IsZero() || c.UpdatedAt.IsZero() {
			t.Errorf("chunk %d lost document metadata: %+v", i, c)
		}
	}
}

func TestBuildChunksAppliesPerKindSizing(t *testing.T) {
	// 320 tokens is ~1280 characters; the body sits between the postiz cap
	// and the configured 400-token default, so it must split under the
	// cross-post policy and stay whole under the default one.
	body := strings.Repeat("social cross post sentence ", 55) // ~1485 chars, ~372 tokens
	doc := shared.Document{
		Title:       "A cross post",
		Body:        body,
		PublishedAt: time.Date(2026, 6, 27, 20, 9, 0, 0, time.UTC),
		Author:      "Eli",
		Platform:    "linkedin",
	}
	cfg := shared.ChunkerConfig{TargetTokens: 400, OverlapTokens: 60}

	ghostChunks, err := ingest.BuildChunks(doc, "ghost_post", "item-1", "", cfg)
	if err != nil {
		t.Fatalf("BuildChunks(ghost_post) error = %v", err)
	}
	postizChunks, err := ingest.BuildChunks(doc, "postiz_post", "item-1", "", cfg)
	if err != nil {
		t.Fatalf("BuildChunks(postiz_post) error = %v", err)
	}
	if len(ghostChunks) != 1 {
		t.Fatalf("ghost_post produced %d chunks, want the 400-token default to keep it whole", len(ghostChunks))
	}
	if len(postizChunks) < 2 {
		t.Fatalf("postiz_post produced %d chunks, want the 320-token cap to split it", len(postizChunks))
	}
	for i, c := range postizChunks {
		if c.BodyTokens > 320 {
			t.Errorf("postiz chunk %d has %d tokens, want at most 320", i, c.BodyTokens)
		}
		if c.Platform != "linkedin" {
			t.Errorf("postiz chunk %d platform = %q, want linkedin", i, c.Platform)
		}
	}
}

func TestFullRunReportsNothingUnchanged(t *testing.T) {
	fp := &fakeProvider{items: []shared.Item{testItem("a")}, docs: map[string]shared.Document{"a": testDoc("A")}}
	runner, cfg := newRunner(t, fp)
	opts := testOptions()

	first, err := runner.Run(context.Background(), cfg.Sources, opts)
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if first[0].Fetched != 1 || first[0].Unchanged != 0 {
		t.Fatalf("first run = fetched %d, unchanged %d; want 1 and 0", first[0].Fetched, first[0].Unchanged)
	}

	// --full re-fetches every listed item, so "unchanged" must report 0 or it
	// double-counts items against Fetched (and against the documented
	// "unchanged is the rest of listed" invariant).
	opts.Full = true
	full, err := runner.Run(context.Background(), cfg.Sources, opts)
	if err != nil {
		t.Fatalf("full Run() error = %v", err)
	}
	if full[0].Fetched != full[0].Listed {
		t.Errorf("full run fetched %d of %d listed, want every listed item", full[0].Fetched, full[0].Listed)
	}
	if full[0].Unchanged != 0 {
		t.Errorf("full run unchanged = %d, want 0 (nothing listed was skipped)", full[0].Unchanged)
	}
	if full[0].Fetched+full[0].Unchanged != full[0].Listed {
		t.Errorf("fetched %d + unchanged %d != listed %d", full[0].Fetched, full[0].Unchanged, full[0].Listed)
	}
}
