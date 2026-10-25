package files_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/files"
)

// source parses an inline source YAML fragment into a config.Source.
func source(t *testing.T, doc string) config.Source {
	t.Helper()
	var src config.Source
	dec := yaml.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.KnownFields(true)
	if err := dec.Decode(&src); err != nil {
		t.Fatalf("decode fixture source: %v", err)
	}
	return src
}

func TestParseOptionsRequiresADirectory(t *testing.T) {
	_, err := files.ParseOptions(source(t, "id: files\ntype: files\noptions: {}\n"))
	if err == nil {
		t.Fatal("ParseOptions accepted a source without a directory")
	}
	if !strings.Contains(err.Error(), "options.dir") {
		t.Errorf("error %q does not name the missing option", err)
	}
}

func TestParseOptionsDefaultsThePattern(t *testing.T) {
	opts, err := files.ParseOptions(source(t, "id: files\ntype: files\noptions: { dir: /tmp }\n"))
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	got, ok := opts.(*files.Options)
	if !ok {
		t.Fatalf("options type = %T, want *files.Options", opts)
	}
	if got.Dir != "/tmp" {
		t.Errorf("Dir = %q, want %q", got.Dir, "/tmp")
	}
	if got.Pattern != files.DefaultPattern {
		t.Errorf("Pattern = %q, want %q", got.Pattern, files.DefaultPattern)
	}
}

func TestParseOptionsRejectsAPatternThatIsAPath(t *testing.T) {
	_, err := files.ParseOptions(source(t, "id: files\ntype: files\noptions: { dir: /tmp, pattern: sub/*.md }\n"))
	if err == nil {
		t.Fatal("ParseOptions accepted a pattern with a path separator")
	}
	if !strings.Contains(err.Error(), "options.pattern") {
		t.Errorf("error %q does not name the offending option", err)
	}
}

func TestNewRejectsAMissingDirectory(t *testing.T) {
	_, err := files.New(&files.Options{Dir: filepath.Join(t.TempDir(), "absent")})
	if err == nil {
		t.Fatal("New accepted a directory that does not exist")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error %q does not say the directory is missing", err)
	}
}

func TestListReturnsTheMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "alpha.md"), "# Alpha\n\nalpha body\n")
	write(t, filepath.Join(dir, "beta.md"), "# Beta\n\nbeta body\n")
	write(t, filepath.Join(dir, "notes.txt"), "not markdown\n")

	p, err := files.New(&files.Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List returned %d items, want the 2 markdown files", len(items))
	}
	if items[0].ID != "alpha.md" || items[1].ID != "beta.md" {
		t.Errorf("List returned %q and %q, want alpha.md and beta.md", items[0].ID, items[1].ID)
	}
	if items[0].LastModified.IsZero() {
		t.Error("List returned an item without a modification time")
	}
	if !strings.HasSuffix(items[0].URL, "alpha.md") {
		t.Errorf("List returned the URL %q, want the path of the file", items[0].URL)
	}
}

func TestListSkipsTheItemsOlderThanSince(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alpha.md")
	write(t, path, "# Alpha\n\nalpha body\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("set the modification time: %v", err)
	}

	p, err := files.New(&files.Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	items, err := p.List(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List returned %d items, want none: the file is older than since", len(items))
	}

	items, err = p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List returned %d items, want 1", len(items))
	}
}

func TestFetchReadsTheFileAndTakesTheHeadingAsTheTitle(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "alpha.md"), "# Alpha Report\n\nthe body of the report\n")

	p, err := files.New(&files.Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	items, err := p.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	doc, err := p.Fetch(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Title != "Alpha Report" {
		t.Errorf("Title = %q, want %q", doc.Title, "Alpha Report")
	}
	if !strings.Contains(doc.Body, "the body of the report") {
		t.Errorf("Body = %q, want the content of the file", doc.Body)
	}
	if doc.PublishedAt.IsZero() {
		t.Error("the document has no publication time")
	}
}

func TestTitleFallsBackToTheFileName(t *testing.T) {
	if got := files.Title("a document without a heading\n", "notes.md"); got != "notes" {
		t.Errorf("Title = %q, want %q", got, "notes")
	}
}

// write writes one file of the fixture corpus.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
