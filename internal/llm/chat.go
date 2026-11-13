package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Chat is the tool-calling half of the OpenAI-compatible connector. The agent
// loop owns prompt assembly and budgeting; this layer only round-trips what the
// server gives it.
//
// Requests carry the search_writing tool definition, assistant messages echo
// the model's tool calls, and tool results come back as role="tool" messages
// keyed by tool_call_id. Spark-X2.6-4B-q8_0 is a reasoning model, so MaxTokens must
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
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// MaxTokens is the completion budget as the legacy OpenAI field. It is
	// what LocalAI and most compatible servers accept. Client.Chat maps it to
	// MaxCompletionTokens when TokenParam says the backend is an OpenAI
	// reasoning endpoint, which rejects max_tokens outright.
	MaxTokens int `json:"max_tokens,omitempty"`
	// MaxCompletionTokens is the replacement completion-budget field used by
	// OpenAI's reasoning models. Callers set MaxTokens; the client fills this
	// in from the configured CHAT_TOKEN_PARAM.
	MaxCompletionTokens int      `json:"max_completion_tokens,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
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
//
// Two provider quirks are recovered instead of surfacing as a failed answer:
// the completion-budget field a model accepts, and a model that refuses
// function tools combined with a reasoning effort (see the retries below).
// Both are learned, so the loop's later calls and every later request send the
// shape the backend already accepted.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if req.Model == "" {
		return ChatResponse{}, fmt.Errorf("llm: chat model is required")
	}
	if len(req.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("llm: chat request has no messages")
	}

	cur := c.applyPolicies(req)
	out, err := c.chatOnce(ctx, cur)
	if err == nil {
		return out, nil
	}

	// The backend named both budget fields, which is it telling us which one
	// it wants. Re-send with the other one and remember the answer.
	tokenSwitched := false
	if tokenFieldMismatch(err) {
		next := cur
		if switchTokenField(&next) {
			out, err = c.chatOnce(ctx, next)
			if err == nil {
				c.rememberTokenField(next)
				return out, nil
			}
			cur, tokenSwitched = next, true
		}
	}

	// The backend refuses function tools with a reasoning effort and says the
	// only accepted value is "none". Re-send at "none" and remember it: the
	// loop then keeps "none" on tool-bearing calls, while the final
	// tools-withdrawn call keeps its configured effort.
	if len(cur.Tools) > 0 && toolsReasoningMismatch(err) {
		next := cur
		next.ReasoningEffort = ReasoningNone
		out, err = c.chatOnce(ctx, next)
		if err == nil {
			c.learnedToolsReasoningNone.Store(true)
			if tokenSwitched {
				c.rememberTokenField(next)
			}
			return out, nil
		}
	}
	return out, err
}

// rememberTokenField records the field the backend accepted after a switch.
func (c *Client) rememberTokenField(req ChatRequest) {
	if req.MaxTokens > 0 {
		c.learnedTokenField.Store(FieldMaxTokens)
		return
	}
	c.learnedTokenField.Store(FieldMaxCompletionTokens)
}

// applyPolicies puts a request into the shape this backend has already
// accepted: the completion-budget field and, on tool-bearing calls, the
// reasoning effort a tools-refusing model allows.
func (c *Client) applyPolicies(req ChatRequest) ChatRequest {
	field := c.TokenParam
	if learned, ok := c.learnedTokenField.Load().(string); ok && learned != "" {
		field = learned
	}
	if field == FieldMaxCompletionTokens && req.MaxTokens > 0 {
		req.MaxCompletionTokens = req.MaxTokens
		req.MaxTokens = 0
	}
	if len(req.Tools) > 0 && c.learnedToolsReasoningNone.Load() &&
		req.ReasoningEffort != "" && req.ReasoningEffort != ReasoningNone {
		req.ReasoningEffort = ReasoningNone
	}
	return req
}

// switchTokenField moves the budget into the other OpenAI field, reporting
// whether there was a budget to move.
func switchTokenField(req *ChatRequest) bool {
	switch {
	case req.MaxTokens > 0:
		req.MaxCompletionTokens, req.MaxTokens = req.MaxTokens, 0
		return true
	case req.MaxCompletionTokens > 0:
		req.MaxTokens, req.MaxCompletionTokens = req.MaxCompletionTokens, 0
		return true
	}
	return false
}

// chatStatusError is a non-2xx reply kept as a type so Chat can recognize the
// one provider complaint it knows how to fix.
type chatStatusError struct {
	status     int
	statusText string
	body       string
}

func (e *chatStatusError) Error() string {
	return fmt.Sprintf("llm: chat returned %s: %s", e.statusText, e.body)
}

// tokenFieldMismatch reports whether err is a backend rejecting the
// completion-budget field it was sent, for example "Unsupported parameter:
// 'max_tokens' is not supported with this model. Use 'max_completion_tokens'
// instead." Both field names must appear in the body: that is what separates
// this complaint from any other 400, so an unrelated error is never retried.
func tokenFieldMismatch(err error) bool {
	var se *chatStatusError
	if !errors.As(err, &se) {
		return false
	}
	if se.status != http.StatusBadRequest && se.status != http.StatusUnprocessableEntity {
		return false
	}
	body := strings.ToLower(se.body)
	return strings.Contains(body, FieldMaxTokens) &&
		strings.Contains(body, FieldMaxCompletionTokens)
}

// toolsReasoningMismatch reports whether err is a backend refusing function
// tools combined with a reasoning effort, for example "Function tools with
// reasoning_effort are not supported for gpt-6-luna in /v1/chat/completions ...
// set reasoning_effort to 'none'." The body must name reasoning_effort, tools,
// and "not supported", so an unrelated 400 is never retried.
func toolsReasoningMismatch(err error) bool {
	var se *chatStatusError
	if !errors.As(err, &se) {
		return false
	}
	if se.status != http.StatusBadRequest && se.status != http.StatusUnprocessableEntity {
		return false
	}
	body := strings.ToLower(se.body)
	return strings.Contains(body, "reasoning_effort") &&
		strings.Contains(body, "tool") &&
		strings.Contains(body, "not supported")
}

// chatOnce performs one attempt. The error for a non-2xx reply carries the
// status and a bounded body so the caller can inspect it; the body is closed
// before returning either way.
func (c *Client) chatOnce(ctx context.Context, req ChatRequest) (ChatResponse, error) {
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
		return ChatResponse{}, &chatStatusError{
			status:     resp.StatusCode,
			statusText: resp.Status,
			body:       errorSnippet(resp.Body),
		}
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
