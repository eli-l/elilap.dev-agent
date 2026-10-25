// Package embed is the embedding client: it turns text into vectors with an
// OpenAI-compatible model server (LocalAI in the reference environment).
//
// The client speaks plain net/http against {base}/embeddings — no SDK — so
// any OpenAI-compatible server works by changing the base URL. It refuses to
// hand back a vector whose width does not match the index schema
// (chunks_vec.embedding is float[768]), because a wrong-width vector would
// only fail later, inside the store, far from its cause.
//
// The API key, if one is configured, is only ever sent as an Authorization
// header. It is never logged and never quoted back in an error.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Dimensions is the vector width the index stores. A vector of any
	// other width is an error, not a value to store.
	Dimensions = 768

	// DefaultModel is the embedding model the reference model server serves.
	DefaultModel = "embeddinggemma-300m"

	// DefaultTimeout bounds a single HTTP request.
	DefaultTimeout = 60 * time.Second

	// maxBodyBytes caps how much of a response body is read, so a
	// misbehaving server cannot exhaust memory.
	maxBodyBytes = 64 << 20

	// errorBodyBytes is how much of a failing response body is quoted back
	// to the caller in the error message.
	errorBodyBytes = 200
)

// Client embeds text with an OpenAI-compatible embeddings endpoint.
//
// A Client is safe for concurrent use: the embedded *http.Client is.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// New returns a Client for the given OpenAI-compatible base URL.
//
// baseURL is the API root, for example http://localhost:8080/v1 — the
// "/embeddings" path is appended by the client. apiKey may be empty, in
// which case no Authorization header is sent; the reference model server
// needs no key. An empty model falls back to DefaultModel.
//
// An unusable base URL is an error that names the offending value, so the
// mistake is visible in the logs without leaking anything secret.
func New(baseURL, apiKey, model string) (*Client, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil, errors.New("embed: base URL is required (set LOCALAI_BASE_URL)")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("embed: base URL %q is not a valid URL: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("embed: base URL %q must use the http or https scheme", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("embed: base URL %q must include a host", baseURL)
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}
	return &Client{
		baseURL:    strings.TrimRight(trimmed, "/"),
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: DefaultTimeout},
	}, nil
}

// WithTimeout returns a copy of the client whose HTTP timeout is d.
// A non-positive d falls back to DefaultTimeout.
func (c *Client) WithTimeout(d time.Duration) *Client {
	if d <= 0 {
		d = DefaultTimeout
	}
	clone := *c
	clone.httpClient = &http.Client{Timeout: d}
	return &clone
}

// Model reports the embedding model this client asks for.
func (c *Client) Model() string { return c.model }

// BaseURL reports the API root this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// embedRequest is the OpenAI-compatible embeddings request body.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the OpenAI-compatible embeddings response body.
type embedResponse struct {
	Data []embedDatum `json:"data"`
}

// embedDatum is one vector in the response. Index is optional: servers that
// omit it are assumed to answer in request order.
type embedDatum struct {
	Index     *int      `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// Embed returns one vector per input text, in the same order as the input.
//
// An empty input returns an empty result and no error — there is nothing to
// ask the server. Every failure names what went wrong: a non-200 status
// carries the status and an excerpt of the response body, a malformed
// response names the missing piece, and a wrong-width vector names both
// widths. The API key never appears in an error message.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	payload, err := json.Marshal(embedRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embed: encode request: %w", err)
	}

	endpoint := c.baseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("embed: build request for %s: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request to %s failed: %w", endpoint, c.redactErr(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("embed: read response from %s: %w", endpoint, c.redactErr(err))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: %s returned status %d: %s", endpoint, resp.StatusCode, c.excerpt(body))
	}

	var parsed embedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("embed: decode response from %s: %w", endpoint, c.redactErr(err))
	}
	if parsed.Data == nil {
		return nil, fmt.Errorf("embed: response from %s is missing %q", endpoint, "data")
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embed: response from %s has %d vectors, expected %d", endpoint, len(parsed.Data), len(texts))
	}

	ordered := orderByIndex(parsed.Data)
	vectors := make([][]float32, 0, len(ordered))
	for i, datum := range ordered {
		if len(datum.Embedding) != Dimensions {
			return nil, fmt.Errorf("embed: vector %d has %d dimensions, expected %d", i, len(datum.Embedding), Dimensions)
		}
		vectors = append(vectors, datum.Embedding)
	}
	return vectors, nil
}

// orderByIndex puts the response vectors back into request order.
//
// The OpenAI-compatible shape carries an explicit index per vector, and a
// server is free to answer out of order. When every vector carries an index
// that forms a permutation of the request positions, the index wins;
// otherwise the server's own order is taken as the request order.
func orderByIndex(data []embedDatum) []embedDatum {
	ordered := make([]embedDatum, len(data))
	copy(ordered, data)

	seen := make([]bool, len(ordered))
	for _, datum := range ordered {
		if datum.Index == nil {
			return ordered
		}
	}
	for _, datum := range ordered {
		idx := *datum.Index
		if idx < 0 || idx >= len(ordered) || seen[idx] {
			return ordered
		}
		seen[idx] = true
	}
	byIndex := make([]embedDatum, len(ordered))
	for _, datum := range ordered {
		byIndex[*datum.Index] = datum
	}
	return byIndex
}

// excerpt renders the first errorBodyBytes of a failing response body, with
// the API key redacted if the server echoed it back.
func (c *Client) excerpt(body []byte) string {
	if len(body) > errorBodyBytes {
		body = body[:errorBodyBytes]
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "(empty body)"
	}
	return c.redact(text)
}

// redact removes the API key from any text that is about to be shown to a
// human. The key is a secret; the fact that a request failed is not.
func (c *Client) redact(text string) string {
	if c.apiKey == "" {
		return text
	}
	return strings.ReplaceAll(text, c.apiKey, "[redacted]")
}

// redactErr returns an error whose message is safe to print.
func (c *Client) redactErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(c.redact(err.Error()))
}
