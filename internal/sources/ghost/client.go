// Package ghost implements the Ghost Admin API provider: JWT-authenticated
// REST client plus the sources.Provider implementation over it.
//
// The upstream response shapes are documented in docs/ghost.md (Phase 0
// capture; until that lands the structs follow the published Admin API
// Content/Admin shapes and fixtures carry them).
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
)

// Client is a minimal Ghost Admin API client.
type Client struct {
	baseURL string
	keyID   string
	keySec  []byte
	hc      *http.Client
	// now is overridable in tests.
	now func() time.Time
}

// NewClient builds a Ghost Admin client from the instance base URL (e.g.
// https://ghost.example.com) and the Admin API key ("id:secret").
func NewClient(baseURL, apiKey string, hc *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("ghost: base URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("ghost: invalid base URL %q", baseURL)
	}
	id, secret, ok := strings.Cut(apiKey, ":")
	if !ok || id == "" || secret == "" {
		return nil, fmt.Errorf("ghost: API key must be <id>:<secret>")
	}
	return &Client{
		baseURL: baseURL,
		keyID:   id,
		keySec:  []byte(secret),
		hc:      hc,
		now:     func() time.Time { return time.Now() },
	}, nil
}

// errGhostStatus turns non-2xx responses into errors carrying the body.
func errGhostStatus(resp *http.Response, body []byte) error {
	return fmt.Errorf("ghost: %s %s: unexpected status %d: %s",
		resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, truncate(string(body), 512))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// get performs an authenticated GET and decodes the JSON envelope into v.
func (c *Client) get(ctx context.Context, path string, query url.Values, v any) error {
	token, err := c.adminToken()
	if err != nil {
		return err
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	log := logging.Sources().With().Str("provider", "ghost").Str("path", path).Logger()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Ghost "+token)
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
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

// Post is the subset of the Ghost Admin post resource this repo consumes.
type Post struct {
	ID          string    `json:"id"`
	UUID        string    `json:"uuid"`
	Title       string    `json:"title"`
	Slug        string    `json:"slug"`
	HTML        string    `json:"html"`
	Plaintext   string    `json:"plaintext"`
	Excerpt     string    `json:"excerpt"`
	CreatedAt   time.Time `json:"created_at"`
	PublishedAt time.Time `json:"published_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Visibility  string    `json:"visibility"`
	URL         string    `json:"url"`
	AuthorID    string    `json:"primary_author"`
	Tags        []Tag     `json:"tags"`
}

// Tag is a Ghost tag reference.
type Tag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// PostsPage is the envelope of GET /ghost/api/admin/posts/.
type PostsPage struct {
	Posts []Post `json:"posts"`
	Meta  struct {
		Pagination struct {
			Page  int `json:"page"`
			Limit int `json:"limit"`
			Pages int `json:"pages"`
			Total int `json:"total"`
			Next  int `json:"next"`
			Prev  int `json:"prev"`
		} `json:"pagination"`
	} `json:"meta"`
}

// ListPosts fetches one page of posts, newest first, with full content.
func (c *Client) ListPosts(ctx context.Context, page, limit int, updatedSince time.Time) (*PostsPage, error) {
	q := url.Values{}
	q.Set("page", fmt.Sprint(page))
	q.Set("limit", fmt.Sprint(limit))
	if !updatedSince.IsZero() {
		q.Set("updated_since", updatedSince.UTC().Format(time.RFC3339))
	}
	q.Set("formats", "html,plaintext")
	var pp PostsPage
	if err := c.get(ctx, "/ghost/api/admin/posts/", q, &pp); err != nil {
		return nil, err
	}
	return &pp, nil
}

// GetPost fetches one post by id with full html+plaintext content.
func (c *Client) GetPost(ctx context.Context, id string) (*Post, error) {
	var envelope struct {
		Posts []Post `json:"posts"`
	}
	path := "/ghost/api/admin/posts/" + url.PathEscape(id) + "/"
	if err := c.get(ctx, path, url.Values{"formats": {"html,plaintext"}}, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Posts) != 1 {
		return nil, fmt.Errorf("ghost: post %s: expected 1 post in response, got %d", id, len(envelope.Posts))
	}
	return &envelope.Posts[0], nil
}
