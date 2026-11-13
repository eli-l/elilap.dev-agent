// Package llm is the OpenAI-compatible HTTP connector: embeddings today, chat
// completions when the agent loop lands (PLAN.md Decision 3).
//
// It is deliberately a thin net/http client, not an SDK. Any server that
// speaks the OpenAI wire format works by changing the base URL, and retry
// behavior is the caller's choice via the injected *http.Client (the CLI
// passes the shared retrying transport, tests pass httptest's client).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds one request when the caller supplies no client.
const DefaultTimeout = 120 * time.Second

// maxErrorBody caps how much of a non-2xx response is quoted in an error.
const maxErrorBody = 512

// Completion-budget request fields. The OpenAI wire format has two: the
// legacy max_tokens that LocalAI and most compatible servers accept, and
// max_completion_tokens, which OpenAI's reasoning models require (a request
// carrying max_tokens is rejected with a 400). CHAT_TOKEN_PARAM selects the
// one Client.Chat sends.
const (
	// FieldMaxTokens is the OpenAI-compatible default.
	FieldMaxTokens = "max_tokens"
	// FieldMaxCompletionTokens is what OpenAI reasoning models accept.
	FieldMaxCompletionTokens = "max_completion_tokens"
	// ReasoningNone is the flat reasoning_effort value that switches reasoning
	// off. Some providers accept it as the only effort with function tools
	// ("Function tools with reasoning_effort are not supported ... set
	// reasoning_effort to 'none'"), which Chat honors automatically.
	ReasoningNone = "none"
)

// Client talks to an OpenAI-compatible endpoint rooted at BaseURL, including
// the /v1 suffix (for example http://host:8080/v1).
type Client struct {
	// BaseURL is the API root without a trailing slash.
	BaseURL string
	// APIKey is the optional bearer token; empty means no Authorization header.
	APIKey string
	// HTTP is the underlying client; nil means a client with DefaultTimeout.
	HTTP *http.Client
	// TokenParam names the completion-budget field Chat emits: FieldMaxTokens
	// (the zero value, and what LocalAI takes) or FieldMaxCompletionTokens for
	// an OpenAI reasoning endpoint. Embeddings ignore it.
	TokenParam string
	// learnedTokenField records the field this backend accepted after an
	// automatic switch (see Chat), so later requests skip the rejected one.
	// It is an atomic.Value because agent serve answers concurrently.
	learnedTokenField atomic.Value
	// learnedToolsReasoningNone records that this backend refuses function
	// tools with any reasoning effort but "none"; tool-bearing requests are
	// then sent at ReasoningNone without a rejected attempt.
	learnedToolsReasoningNone atomic.Bool
}

// NewClient normalizes the endpoint and returns a connector. An empty API key
// is legitimate (LocalAI needs none).
func NewClient(baseURL, apiKey string, hc *http.Client) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("llm: base URL is required")
	}
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{BaseURL: base, APIKey: strings.TrimSpace(apiKey), HTTP: hc}, nil
}

// embeddingRequest is the OpenAI /v1/embeddings payload. No encoding_format:
// float is the default and the one every compatible server supports.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingResponse is the subset of the reply the client needs. Items carry
// an explicit index because servers may answer out of order.
type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per input, in input order. It is an error for the
// server to return a different count, an out-of-range index, or an empty
// vector; dimension policy (768) belongs to the caller that owns the schema.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if model == "" {
		return nil, fmt.Errorf("llm: embed model is required")
	}
	if len(inputs) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embeddingRequest{Model: model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("llm: marshal embeddings request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: build embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: embeddings request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("llm: embeddings returned %s: %s",
			resp.Status, errorSnippet(resp.Body))
	}

	var out embeddingResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("llm: decode embeddings response: %w", err)
	}
	if len(out.Data) != len(inputs) {
		return nil, fmt.Errorf("llm: embeddings returned %d vectors for %d inputs",
			len(out.Data), len(inputs))
	}

	vectors := make([][]float32, len(inputs))
	for _, item := range out.Data {
		if item.Index < 0 || item.Index >= len(inputs) {
			return nil, fmt.Errorf("llm: embeddings index %d out of range (batch %d)", item.Index, len(inputs))
		}
		if vectors[item.Index] != nil {
			return nil, fmt.Errorf("llm: embeddings returned index %d twice", item.Index)
		}
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("llm: embeddings returned an empty vector at index %d", item.Index)
		}
		vectors[item.Index] = item.Embedding
	}
	return vectors, nil
}

// errorSnippet quotes a bounded, single-line slice of an error body so a
// server's HTML error page does not flood the log or the terminal.
func errorSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, maxErrorBody))
	s := strings.Join(strings.Fields(string(b)), " ")
	if s == "" {
		return "(empty body)"
	}
	if len(b) >= maxErrorBody {
		s += "…"
	}
	return s
}
