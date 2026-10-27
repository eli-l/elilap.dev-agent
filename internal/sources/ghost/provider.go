package ghost

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// Provider implements shared.Provider over the Ghost Admin API.
type Provider struct {
	client *Client
	opts   *Options
}

// NewProvider constructs the Ghost provider from parsed options and
// resolved credentials. The API key must already be resolved from the
// environment by the caller (config layer never prints values).
func NewProvider(opts *Options, apiURL, apiKey string, hc *http.Client) (*Provider, error) {
	client, err := NewClient(apiURL, apiKey, hc)
	if err != nil {
		return nil, err
	}
	return &Provider{client: client, opts: opts}, nil
}

// Type reports the config type string.
func (p *Provider) Type() string { return "ghost" }

// List returns cheap candidate items modified at or after since. Ghost posts
// come back with full html in the list response, but List only carries the
// fingerprint fields (uuid, updated_at, url); Fetch pulls the body.
func (p *Provider) List(ctx context.Context, since time.Time) ([]shared.Item, error) {
	var items []shared.Item
	page := 1
	for {
		pp, err := p.client.ListPosts(ctx, page, p.opts.Limit, since)
		if err != nil {
			return nil, err
		}
		for _, post := range pp.Posts {
			if !p.opts.IncludeDrafts && post.PublishedAt.IsZero() {
				continue // drafts have no published_at
			}
			items = append(items, shared.Item{
				ID:           post.UUID,
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
	return items, nil
}

// Fetch returns the full, normalized content of one post previously
// returned by List. Ghost's list response already carries html; Fetch
// re-requests the single post for a fresh, authoritative body.
func (p *Provider) Fetch(ctx context.Context, it shared.Item) (shared.Document, error) {
	post, err := p.client.GetPost(ctx, it.ID)
	if err != nil {
		return shared.Document{}, err
	}
	doc := shared.Document{
		Title:       post.Title,
		Body:        post.Plaintext,
		HTML:        post.HTML,
		PublishedAt: post.PublishedAt,
	}
	for _, t := range post.Tags {
		doc.Tags = append(doc.Tags, t.Name)
	}
	doc.Meta = map[string]any{
		"ghost_id":   post.ID,
		"slug":       post.Slug,
		"visibility": post.Visibility,
		"updated_at": post.UpdatedAt.Format(time.RFC3339),
		"excerpt":    post.Excerpt,
	}
	return doc, nil
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
