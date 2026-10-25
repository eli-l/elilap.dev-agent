package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// Provider reads a directory of markdown files.
//
// The directory is resolved to an absolute path when the provider is built,
// so the item ids it hands out - paths relative to that directory - stay
// stable no matter where the command is run from.
type Provider struct {
	dir     string
	pattern string
}

// New returns a provider for the given options.
//
// The directory must exist and be a directory: a mistyped path is a
// configuration error, not an empty corpus, and silently indexing nothing
// would hide the mistake.
func New(opts *Options) (*Provider, error) {
	if opts == nil {
		return nil, fmt.Errorf("files: the options are nil")
	}

	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		return nil, fmt.Errorf("files: options.dir is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("files: resolve the directory %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("files: the directory %q does not exist", abs)
		}
		return nil, fmt.Errorf("files: read the directory %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("files: %q is not a directory", abs)
	}

	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		pattern = DefaultPattern
	}

	return &Provider{dir: abs, pattern: pattern}, nil
}

// Dir returns the absolute directory the provider reads.
func (p *Provider) Dir() string { return p.dir }

// Pattern returns the glob the provider selects files with.
func (p *Provider) Pattern() string { return p.pattern }

// Type reports the provider type this implementation answers to.
func (p *Provider) Type() string { return Type }

// SourceKind reports the kind of item this provider produces.
func (p *Provider) SourceKind() string { return SourceKind }

// List returns the matching files as cheap candidates.
//
// The item id is the file's path relative to the configured directory: it is
// stable across machines, and it is what the chunk id is derived from. The
// URL is the file's absolute path, which is the canonical "where this came
// from" for a local corpus. Items modified before since are skipped, so an
// incremental run only considers what changed.
//
// The list is sorted by path, so two runs over the directory see the items
// in the same order.
func (p *Provider) List(ctx context.Context, since time.Time) ([]shared.Item, error) {
	matches, err := filepath.Glob(filepath.Join(p.dir, p.pattern))
	if err != nil {
		return nil, fmt.Errorf("files: the pattern %q is not a valid glob: %w", p.pattern, err)
	}
	sort.Strings(matches)

	var items []shared.Item
	for _, path := range matches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("files: read %q: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}
		rel, err := filepath.Rel(p.dir, path)
		if err != nil {
			return nil, fmt.Errorf("files: relativize %q: %w", path, err)
		}
		items = append(items, shared.Item{
			ID:           filepath.ToSlash(rel),
			URL:          path,
			LastModified: info.ModTime().UTC(),
			Hint:         fmt.Sprintf("%d:%d", info.ModTime().Unix(), info.Size()),
		})
	}
	return items, nil
}

// Fetch reads one file and returns its content.
//
// The body is the file's markdown as it stands: the chunker and the
// full-text index work on it directly, so nothing is rewritten on the way
// in. The title is the document's first markdown heading, and the file name
// when the document has no heading.
func (p *Provider) Fetch(ctx context.Context, it shared.Item) (shared.Document, error) {
	if err := ctx.Err(); err != nil {
		return shared.Document{}, err
	}
	if strings.TrimSpace(it.ID) == "" {
		return shared.Document{}, fmt.Errorf("files: the item id is empty")
	}

	path := filepath.Join(p.dir, filepath.FromSlash(it.ID))
	if !strings.HasPrefix(path, p.dir+string(os.PathSeparator)) {
		return shared.Document{}, fmt.Errorf("files: the item %q points outside %q", it.ID, p.dir)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return shared.Document{}, fmt.Errorf("files: read %q: %w", it.ID, err)
	}
	body := string(data)

	return shared.Document{
		Title:       Title(body, it.ID),
		Body:        body,
		PublishedAt: it.LastModified,
		Meta:        map[string]any{"path": it.ID},
	}, nil
}

// Title returns the title of a markdown document: its first heading, or the
// file name without its extension when the document has no heading.
func Title(body, id string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if title != "" {
			return title
		}
	}
	base := filepath.Base(filepath.FromSlash(id))
	return strings.TrimSuffix(base, filepath.Ext(base))
}
