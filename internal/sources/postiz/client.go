// Package postiz implements the Postiz public-API provider: a read-only HTTP
// client plus the sources.Provider implementation over it.
//
// The upstream response shape is captured in docs/postiz.md (live capture,
// 2026-11-02). The public API exposes one listing endpoint and no per-post
// read, so List carries the full content and Fetch serves it from the listing
// made during the same run.
package postiz

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

// postsPath is the public-API listing endpoint, relative to the API root. It
// is the only endpoint this provider reads: Postiz has no GET-by-id route
// (GET /api/public/v1/posts/<id> answers 404), which is why Fetch is served
// from the listing.
const postsPath = "/api/public/v1/posts"

// maxResponseBytes bounds one listing response. The full 1,710-post history
// measured 1.6 MB; the limit is generous enough for real growth while still
// refusing to buffer an unbounded body. It is a variable only so a test can
// shrink it, never so callers can tune it.
var maxResponseBytes int64 = 64 << 20

// Client is a minimal Postiz public-API client.
type Client struct {
	baseURL string
	apiKey  string
	hc      *http.Client
}

// NewClient builds a Postiz client from the instance base URL (e.g.
// https://postiz.example.com) and the public API key. A base URL that already
// ends in /api is accepted and normalized, so both
// "https://host" and "https://host/api" address the same instance.
func NewClient(baseURL, apiKey string, hc *http.Client) (*Client, error) {
	baseURL = normalizeBaseURL(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("postiz: base URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("postiz: invalid base URL %q", baseURL)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("postiz: API key is required")
	}
	return &Client{baseURL: baseURL, apiKey: apiKey, hc: hc}, nil
}

// normalizeBaseURL trims a trailing slash and a trailing "/api", so a config
// copied from the upstream docs ("https://host/api") and a bare instance URL
// resolve to the same API root.
func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/api")
	return strings.TrimRight(baseURL, "/")
}

// Post is one Postiz post record, as returned by the public API. The API
// returns one record per (post, platform) copy: the id is the copy's id, and
// integration.providerIdentifier names the platform.
type Post struct {
	ID             string      `json:"id"`
	Content        string      `json:"content"`
	PublishDate    time.Time   `json:"publishDate"`
	ReleaseURL     string      `json:"releaseURL"`
	ReleaseID      string      `json:"releaseId"`
	State          string      `json:"state"`
	Error          any         `json:"error"`
	IntervalInDays *int        `json:"intervalInDays"`
	Group          string      `json:"group"`
	CreationMethod string      `json:"creationMethod"`
	Settings       string      `json:"settings"` // JSON-encoded string, e.g. {"__type":"bluesky"}
	Tags           []string    `json:"tags"`
	Integration    Integration `json:"integration"`
}

// Integration identifies the social account a post was published through.
type Integration struct {
	ID                 string `json:"id"`
	ProviderIdentifier string `json:"providerIdentifier"`
	Name               string `json:"name"`
	Picture            string `json:"picture"`
}

// postsEnvelope is the response of GET /api/public/v1/posts. The endpoint
// returns every post in the requested date window and has no pagination
// metadata: page/limit are ignored upstream.
type postsEnvelope struct {
	Posts []Post `json:"posts"`
}

// ListPosts returns every post whose publishDate falls in [start, end]. The
// range is required by the API (a missing or malformed date is a 400), so an
// unbounded listing asks from the Unix epoch.
func (c *Client) ListPosts(ctx context.Context, start, end time.Time) ([]Post, error) {
	q := url.Values{}
	q.Set("startDate", start.UTC().Format(time.RFC3339))
	q.Set("endDate", end.UTC().Format(time.RFC3339))

	var env postsEnvelope
	if err := c.get(ctx, postsPath, q, &env); err != nil {
		return nil, err
	}
	return env.Posts, nil
}

// get performs an authenticated GET and decodes the JSON response into v.
func (c *Client) get(ctx context.Context, path string, query url.Values, v any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	log := logging.Sources().With().Str("provider", "postiz").Str("path", path).Logger()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	// Postiz authenticates with the bare API key; there is no Bearer prefix.
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		// The key travels in a header, so the URL in a *url.Error is safe to
		// return; keep the transport error as-is and let the caller log it.
		log.Error().Err(err).Dur("duration", time.Since(start)).Msg("request failed")
		return err
	}
	defer resp.Body.Close()

	// Read one byte past the cap so an oversized body is reported as such
	// instead of surfacing as a truncated-JSON decode error.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		log.Error().Err(err).Dur("duration", time.Since(start)).Msg("read response body failed")
		return err
	}
	if int64(len(body)) > maxResponseBytes {
		log.Error().
			Int64("limit", maxResponseBytes).
			Dur("duration", time.Since(start)).
			Msg("response exceeds the size limit")
		return fmt.Errorf("postiz: GET %s: response exceeds %d bytes", path, maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Error().
			Int("status", resp.StatusCode).
			Int("bytes", len(body)).
			Dur("duration", time.Since(start)).
			Msg("unexpected status")
		return fmt.Errorf("postiz: GET %s: unexpected status %d: %s",
			path, resp.StatusCode, truncate(string(body), 512))
	}
	log.Debug().
		Int("status", resp.StatusCode).
		Int("bytes", len(body)).
		Dur("duration", time.Since(start)).
		Msg("request done")

	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("postiz: decode %s response: %w", path, err)
	}
	return nil
}

// truncate shortens s to at most n bytes for an error message.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
