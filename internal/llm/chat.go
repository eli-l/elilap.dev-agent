package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Chat is the tool-calling half of the OpenAI-compatible connector. The agent
// loop owns prompt assembly and budgeting; this layer only round-trips what the
// server gives it.
//
// Requests carry the search_writing tool definition, assistant messages echo
// the model's tool calls, and tool results come back as role="tool" messages
// keyed by tool_call_id. spark-x2.5-4b is a reasoning model, so MaxTokens must
// budget for reasoning tokens as well as the visible answer.
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []Tool        `json:"tools,omitempty"`
	// ToolChoice is "auto" or "none"; empty leaves the backend default.
	ToolChoice string `json:"tool_choice,omitempty"`
	// ReasoningEffort is a flat OpenAI `reasoning_effort` string, not a nested
	// object: /v1/chat/completions has no `reasoning` field, and a nested object
	// is accepted and silently ignored (the model then reasons at its own
	// config default, which for spark-x2.6 is unbounded). Empty means "model
	// config"; the llama-cpp template behind spark-x2.5/2.6 accepts only
	// "none", "low", and "high" - anything else ("medium", wrong casing) is
	// an HTTP 500 from the backend, so callers must pick from that set.
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	MaxTokens       int      `json:"max_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

// ChatMessage is one OpenAI chat message. ToolCalls is set on assistant
// messages, ToolCallID on tool results; Content is empty on a pure tool call.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is one assistant-requested function invocation. Arguments is a JSON
// object encoded as a string, per the OpenAI wire format.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction names the function and carries its JSON-encoded arguments.
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool declares one callable function to the model.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function is the OpenAI function definition. Parameters is a JSON Schema.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatResponse is the subset of /v1/chat/completions the loop needs. Model is
// the id the server reports having served the request, which is kept because a
// debug run has to show which model actually answered (a gateway may route
// elsewhere). Usage is kept for diagnostics; reasoning is intentionally ignored
// (the visible content is what gets grounded).
type ChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Chat sends one chat completion request and returns the first choice. A reply
// without choices is an error; the caller never gets a silent empty answer.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if req.Model == "" {
		return ChatResponse{}, fmt.Errorf("llm: chat model is required")
	}
	if len(req.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("llm: chat request has no messages")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm: marshal chat request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm: build chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm: chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ChatResponse{}, fmt.Errorf("llm: chat returned %s: %s",
			resp.Status, errorSnippet(resp.Body))
	}

	var out ChatResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
	if err := dec.Decode(&out); err != nil {
		return ChatResponse{}, fmt.Errorf("llm: decode chat response: %w", err)
	}
	if len(out.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("llm: chat returned no choices")
	}
	return out, nil
}
