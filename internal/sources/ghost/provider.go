package ghost

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// postAPI is the listing/fetching pair Provider needs, implemented by both
// the Admin client (JWT) and the Content client (query key).
type postAPI interface {
	ListPosts(ctx context.Context, page, limit int, updatedSince time.Time) (*PostsPage, error)
	GetPost(ctx context.Context, id string) (*Post, error)
}

// Provider implements shared.Provider over a Ghost instance. Whether it talks
// to the Admin API or the read-only Content API depends on the configured key
// shape: "<id>:<secret>" is an Admin key, a bare key is a Content key.
type Provider struct {
	client postAPI
	opts   *Options
}

// NewProvider constructs the Ghost provider from parsed options and
// resolved credentials. The API key must already be resolved from the
// environment by the caller (config layer never prints values).
func NewProvider(opts *Options, apiURL, apiKey string, hc *http.Client) (*Provider, error) {
	var (
		client postAPI
		err    error
	)
	if strings.Contains(apiKey, ":") {
		client, err = NewClient(apiURL, apiKey, hc)
	} else {
		log := logging.Sources()
		log.Info().Msg("using Ghost Content API (key is not an Admin key)")
		if opts.IncludeDrafts {
			// The Content API only ever returns published posts; silently
			// ignoring the option would look like a corpus gap.
			log.Warn().Bool("include_drafts", true).
				Msg("include_drafts has no effect with a Content API key: only published posts are returned")
		}
		client, err = NewContentClient(apiURL, apiKey, hc)
	}
	if err != nil {
		return nil, err
	}
	return &Provider{client: client, opts: opts}, nil
}

// NewFromSource is the registry constructor: it resolves the source's
// credential env references and builds a Provider from parsed options.
func NewFromSource(src config.Source, opts any, bc shared.BuildContext) (shared.Provider, error) {
	o, ok := opts.(*Options)
	if !ok {
		return nil, fmt.Errorf("ghost: options are %T, want *ghost.Options", opts)
	}
	apiURL, apiKey, err := resolveCredentials(src)
	if err != nil {
		return nil, err
	}
	return NewProvider(o, apiURL, apiKey, bc.Client())
}

// Type reports the config type string.
func (p *Provider) Type() string { return "ghost" }

// List returns cheap candidate items modified at or after since. Ghost posts
// come back with full html in the list response, but List only carries the
// fingerprint fields (uuid, updated_at, url); Fetch pulls the body.
func (p *Provider) List(ctx context.Context, since time.Time) ([]shared.Item, error) {
	log := logging.Sources().With().Str("provider", p.Type()).Logger()
	var items []shared.Item
	page := 1
	for {
		pp, err := p.client.ListPosts(ctx, page, p.opts.Limit, since)
		if err != nil {
			return nil, err
		}
		log.Debug().
			Int("page", page).
			Int("pages", pp.Meta.Pagination.Pages).
			Int("posts", len(pp.Posts)).
			Msg("listed posts page")
		for _, post := range pp.Posts {
			if !p.opts.IncludeDrafts && post.PublishedAt.IsZero() {
				continue // drafts have no published_at
			}
			items = append(items, shared.Item{
				ID:           post.UUID,
				Ref:          post.ID,
				URL:          post.URL,
				ETag:         "",
				LastModified: post.UpdatedAt,
				Hint:         fmt.Sprintf("updated_at=%s", post.UpdatedAt.Format(time.RFC3339)),
			})
		}
		if pp.Meta.Pagination.Next == 0 || page >= pp.Meta.Pagination.Pages {
			break
		}
		page = pp.Meta.Pagination.Next
	}
	log.Info().
		Time("since", since).
		Int("items", len(items)).
		Msg("listed posts")
	return items, nil
}

// Fetch returns the full, normalized content of one post previously
// returned by List. Ghost's list response already carries html; Fetch
// re-requests the single post for a fresh, authoritative body.
func (p *Provider) Fetch(ctx context.Context, it shared.Item) (shared.Document, error) {
	log := logging.Sources().With().Str("provider", p.Type()).Str("item_id", it.ID).Logger()
	if it.Ref == "" {
		// Ghost rejects reads by uuid (422); only the 24-hex id in Ref works,
		// so fail clearly instead of sending a request that cannot succeed.
		return shared.Document{}, fmt.Errorf("ghost: item %s has no fetch ref (Ghost rejects uuid reads)", it.ID)
	}
	post, err := p.client.GetPost(ctx, it.Ref)
	if err != nil {
		log.Error().Err(err).Msg("fetch post failed")
		return shared.Document{}, err
	}
	doc := shared.Document{
		Title:       post.Title,
		Body:        post.Plaintext,
		HTML:        post.HTML,
		PublishedAt: post.PublishedAt,
		UpdatedAt:   post.UpdatedAt,
		Author:      authorName(post),
	}
	for _, t := range post.Tags {
		doc.Tags = append(doc.Tags, t.Name)
	}
	doc.Meta = map[string]any{
		"ghost_id":   post.ID,
		"slug":       post.Slug,
		"status":     post.Status,
		"visibility": post.Visibility,
		"updated_at": post.UpdatedAt.Format(time.RFC3339),
		"excerpt":    post.Excerpt,
	}
	log.Debug().
		Str("slug", post.Slug).
		Int("html_bytes", len(post.HTML)).
		Int("plaintext_bytes", len(post.Plaintext)).
		Int("tags", len(doc.Tags)).
		Msg("fetched post")
	return doc, nil
}

// authorName returns the post's primary author display name, falling back
// to the first author in the list.
func authorName(post *Post) string {
	if post.PrimaryAuthor != nil && post.PrimaryAuthor.Name != "" {
		return post.PrimaryAuthor.Name
	}
	if len(post.Authors) > 0 {
		return post.Authors[0].Name
	}
	return ""
}

// resolveCredentials reads the env var names referenced by the source's
// credentials block. Missing variables produce a clear error naming the
// variable - never the value.
func resolveCredentials(src config.Source) (apiURL, apiKey string, err error) {
	apiURL, err = resolveEnvRef(src.Credentials.APIBaseURL)
	if err != nil {
		return "", "", err
	}
	apiKey, err = resolveEnvRef(src.Credentials.APIKey)
	if err != nil {
		return "", "", err
	}
	return apiURL, apiKey, nil
}
