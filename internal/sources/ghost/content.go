package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// ContentClient is the read-only Ghost Content API client: a 26-character
// hex key passed as a query parameter, published posts only, no JWT. It is
// the fallback used when the configured key is not an Admin key
// ("<id>:<secret>"), which is what a public, read-only mirror such as
// read.elilap.dev exposes.
//
// It intentionally implements the same ListPosts/GetPost pair as Client so
// Provider can hold either behind postAPI.
type ContentClient struct {
	baseURL string
	key     string
	hc      *http.Client
}

// NewContentClient builds a Content API client from the instance base URL and
// the Content API key.
func NewContentClient(baseURL, key string, hc *http.Client) (*ContentClient, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("ghost: base URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("ghost: invalid base URL %q", baseURL)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("ghost: content API key is required")
	}
	return &ContentClient{baseURL: baseURL, key: key, hc: hc}, nil
}

// get performs a key-authenticated GET and decodes the JSON envelope into v.
func (c *ContentClient) get(ctx context.Context, path string, query url.Values, v any) error {
	query.Set("key", c.key)
	u := c.baseURL + path + "?" + query.Encode()
	log := logging.Sources().With().Str("provider", "ghost").Str("path", path).Logger()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		// The key travels in the query string, so a *url.Error from the
		// transport must be redacted before it is logged or returned.
		err = shared.RedactError(err)
		log.Error().Err(err).Dur("duration", time.Since(start)).Msg("request failed")
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		log.Error().Err(err).Dur("duration", time.Since(start)).Msg("read response body failed")
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Error().
			Int("status", resp.StatusCode).
			Int("bytes", len(body)).
			Dur("duration", time.Since(start)).
			Msg("unexpected status")
		return errGhostStatus(resp, body)
	}
	log.Debug().
		Int("status", resp.StatusCode).
		Int("bytes", len(body)).
		Dur("duration", time.Since(start)).
		Msg("request done")
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("ghost: decode %s response: %w", path, err)
	}
	return nil
}

// contentMaxLimit is the largest page the Content API accepts.
const contentMaxLimit = 100

// ListPosts fetches one page of published posts, newest first. The Content
// API calls the incremental bound `filter`, not `updated_since`, and clamps
// the page size to 100.
func (c *ContentClient) ListPosts(ctx context.Context, page, limit int, updatedSince time.Time) (*PostsPage, error) {
	if limit > contentMaxLimit {
		limit = contentMaxLimit
	}
	q := url.Values{}
	q.Set("page", fmt.Sprint(page))
	q.Set("limit", fmt.Sprint(limit))
	q.Set("formats", "html,plaintext")
	q.Set("include", "tags,authors")
	if !updatedSince.IsZero() {
		q.Set("filter", "updated_at:>='"+updatedSince.UTC().Format("2006-01-02 15:04:05")+"'")
	}
	var pp PostsPage
	if err := c.get(ctx, "/ghost/api/content/posts/", q, &pp); err != nil {
		return nil, err
	}
	return &pp, nil
}

// GetPost fetches one post by id with full html+plaintext content.
func (c *ContentClient) GetPost(ctx context.Context, id string) (*Post, error) {
	var envelope struct {
		Posts []Post `json:"posts"`
	}
	path := "/ghost/api/content/posts/" + url.PathEscape(id) + "/"
	q := url.Values{"formats": {"html,plaintext"}, "include": {"tags,authors"}}
	if err := c.get(ctx, path, q, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Posts) != 1 {
		return nil, fmt.Errorf("ghost: post %s: expected 1 post in response, got %d", id, len(envelope.Posts))
	}
	return &envelope.Posts[0], nil
}
