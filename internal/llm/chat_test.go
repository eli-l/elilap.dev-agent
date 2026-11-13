package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatRoundTripsTools(t *testing.T) {
	var got ChatRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		// Include a reasoning field and an unknown field: the client must
		// ignore them rather than fail the decode.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "served-model",
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":      "assistant",
					"content":   "",
					"reasoning": "thinking about it",
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]string{
							"name":      "search_writing",
							"arguments": `{"query":"harness"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL+"/v1", "secret", srv.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	params := json.RawMessage(`{"type":"object"}`)
	resp, err := c.Chat(context.Background(), ChatRequest{
		Model:           "chat-model",
		MaxTokens:       512,
		ReasoningEffort: "none",
		Messages: []ChatMessage{
			{Role: "system", Content: "be grounded"},
			{Role: "user", Content: "question"},
		},
		Tools: []Tool{{Type: "function", Function: Function{
			Name: "search_writing", Description: "search", Parameters: params,
		}}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if got.Model != "chat-model" || got.MaxTokens != 512 || len(got.Tools) != 1 {
		t.Errorf("request = %+v", got)
	}
	if got.ReasoningEffort != "none" {
		t.Errorf("reasoning_effort = %q, want the flat string the caller set", got.ReasoningEffort)
	}
	if len(got.Tools[0].Function.Parameters) == 0 {
		t.Error("tool parameters were dropped")
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "search_writing" ||
		msg.ToolCalls[0].Function.Arguments != `{"query":"harness"}` {
		t.Errorf("tool call = %+v", msg.ToolCalls[0])
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Model != "served-model" {
		t.Errorf("model = %q, want the server-reported id", resp.Model)
	}
}

func TestChatErrors(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "model not loaded", http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		c, _ := NewClient(srv.URL, "", srv.Client())
		_, err := c.Chat(context.Background(), ChatRequest{
			Model: "m", Messages: []ChatMessage{{Role: "user", Content: "q"}},
		})
		if err == nil || !strings.Contains(err.Error(), "model not loaded") {
			t.Fatalf("err = %v, want the server body", err)
		}
	})

	t.Run("no choices", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[]}`))
		}))
		defer srv.Close()
		c, _ := NewClient(srv.URL, "", srv.Client())
		_, err := c.Chat(context.Background(), ChatRequest{
			Model: "m", Messages: []ChatMessage{{Role: "user", Content: "q"}},
		})
		if err == nil || !strings.Contains(err.Error(), "no choices") {
			t.Fatalf("err = %v, want a no-choices error", err)
		}
	})

	t.Run("missing model", func(t *testing.T) {
		c := &Client{BaseURL: "http://unused"}
		if _, err := c.Chat(context.Background(), ChatRequest{
			Messages: []ChatMessage{{Role: "user", Content: "q"}},
		}); err == nil {
			t.Fatal("missing model was accepted")
		}
	})
}

// TestChatTokenParamSwitchesBudgetField pins the one wire difference a cloud
// provider needs: OpenAI's reasoning models reject max_tokens, so CHAT_TOKEN_PARAM
// moves the same budget into max_completion_tokens.
func TestChatTokenParamSwitchesBudgetField(t *testing.T) {
	t.Run("max_completion_tokens", func(t *testing.T) {
		var got ChatRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode request: %v", err)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL+"/v1", "key", srv.Client())
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		c.TokenParam = FieldMaxCompletionTokens
		if _, err := c.Chat(context.Background(), ChatRequest{
			Model:     "gpt6-luna",
			MaxTokens: 2048,
			Messages:  []ChatMessage{{Role: "user", Content: "q"}},
		}); err != nil {
			t.Fatalf("chat: %v", err)
		}
		if got.MaxTokens != 0 {
			t.Errorf("max_tokens = %d, want it omitted", got.MaxTokens)
		}
		if got.MaxCompletionTokens != 2048 {
			t.Errorf("max_completion_tokens = %d, want 2048", got.MaxCompletionTokens)
		}
	})

	t.Run("default keeps max_tokens", func(t *testing.T) {
		var got ChatRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode request: %v", err)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL+"/v1", "", srv.Client())
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		if _, err := c.Chat(context.Background(), ChatRequest{
			Model:     "local-model",
			MaxTokens: 512,
			Messages:  []ChatMessage{{Role: "user", Content: "q"}},
		}); err != nil {
			t.Fatalf("chat: %v", err)
		}
		if got.MaxTokens != 512 || got.MaxCompletionTokens != 0 {
			t.Errorf("budget fields = max_tokens %d, max_completion_tokens %d; want 512, 0",
				got.MaxTokens, got.MaxCompletionTokens)
		}
	})
}

// tokenFieldServer answers like a backend that accepts exactly one of the two
// OpenAI completion-budget fields, and counts every attempt.
func tokenFieldServer(t *testing.T, accept, reject string) (*httptest.Server, *int) {
	t.Helper()
	requests := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, ok := raw[reject]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: '` + reject +
				`' is not supported with this model. Use '` + accept + `' instead.","type":"invalid_request_error","param":"` +
				reject + `","code":"unsupported_parameter"}}`))
			return
		}
		if _, ok := raw[accept]; !ok {
			t.Errorf("request carried neither %s nor %s", accept, reject)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	return srv, requests
}

// TestChatSwitchesTokenFieldAutomatically pins the recovery path for a cloud
// model that rejects the configured completion-budget field: one rejected
// attempt, then the request is re-sent with the other field, and the choice is
// remembered so later calls do not pay for the rejection again.
func TestChatSwitchesTokenFieldAutomatically(t *testing.T) {
	t.Run("max_tokens rejected", func(t *testing.T) {
		srv, requests := tokenFieldServer(t, "max_completion_tokens", "max_tokens")
		defer srv.Close()
		c, err := NewClient(srv.URL+"/v1", "key", srv.Client())
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		for i := 0; i < 2; i++ {
			if _, err := c.Chat(context.Background(), ChatRequest{
				Model:     "gpt-6-luna",
				MaxTokens: 2048,
				Messages:  []ChatMessage{{Role: "user", Content: "q"}},
			}); err != nil {
				t.Fatalf("chat %d: %v", i+1, err)
			}
		}
		if *requests != 3 {
			t.Errorf("requests = %d, want 3 (one rejected attempt, then one per call)", *requests)
		}
	})

	t.Run("max_completion_tokens rejected", func(t *testing.T) {
		srv, requests := tokenFieldServer(t, "max_tokens", "max_completion_tokens")
		defer srv.Close()
		c, err := NewClient(srv.URL+"/v1", "", srv.Client())
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		c.TokenParam = FieldMaxCompletionTokens
		for i := 0; i < 2; i++ {
			if _, err := c.Chat(context.Background(), ChatRequest{
				Model:     "local-model",
				MaxTokens: 512,
				Messages:  []ChatMessage{{Role: "user", Content: "q"}},
			}); err != nil {
				t.Fatalf("chat %d: %v", i+1, err)
			}
		}
		if *requests != 3 {
			t.Errorf("requests = %d, want 3 (one rejected attempt, then one per call)", *requests)
		}
	})

	t.Run("unrelated 400 is not retried", func(t *testing.T) {
		requests := new(int)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*requests++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"context length exceeded"}}`))
		}))
		defer srv.Close()
		c, err := NewClient(srv.URL+"/v1", "", srv.Client())
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		_, err = c.Chat(context.Background(), ChatRequest{
			Model:     "m",
			MaxTokens: 512,
			Messages:  []ChatMessage{{Role: "user", Content: "q"}},
		})
		if err == nil || !strings.Contains(err.Error(), "context length exceeded") {
			t.Fatalf("err = %v, want the server body", err)
		}
		if *requests != 1 {
			t.Errorf("requests = %d, want 1: an unrelated 400 must not be retried", *requests)
		}
	})
}

// toolsReasoningServer mimics a provider that refuses function tools combined
// with a reasoning effort, and records the effort each attempt carried.
func toolsReasoningServer(t *testing.T, alwaysReject bool) (*httptest.Server, *[]string) {
	t.Helper()
	efforts := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools           []json.RawMessage `json:"tools"`
			ReasoningEffort string            `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		*efforts = append(*efforts, body.ReasoningEffort)
		if alwaysReject || (len(body.Tools) > 0 && body.ReasoningEffort != "" && body.ReasoningEffort != "none") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6-luna in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","type":"invalid_request_error","param":"reasoning_effort"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	return srv, efforts
}

// TestChatDropsReasoningEffortForToolCalls pins the recovery for a model that
// only accepts reasoning_effort "none" next to function tools: the tool call is
// re-sent once at "none", later tool calls go straight to "none", and a call
// with the tools withdrawn keeps its configured effort.
func TestChatDropsReasoningEffortForToolCalls(t *testing.T) {
	srv, efforts := toolsReasoningServer(t, false)
	defer srv.Close()
	c, err := NewClient(srv.URL+"/v1", "key", srv.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	tool := Tool{Type: "function", Function: Function{
		Name: "search_writing", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`),
	}}
	messages := []ChatMessage{{Role: "user", Content: "q"}}

	if _, err := c.Chat(context.Background(), ChatRequest{
		Model: "gpt-6-luna", MaxTokens: 2048, Messages: messages,
		Tools: []Tool{tool}, ReasoningEffort: "low",
	}); err != nil {
		t.Fatalf("tool call: %v", err)
	}
	if got := *efforts; len(got) != 2 || got[0] != "low" || got[1] != "none" {
		t.Fatalf("efforts = %v, want one rejected low then none", got)
	}

	*efforts = nil
	if _, err := c.Chat(context.Background(), ChatRequest{
		Model: "gpt-6-luna", MaxTokens: 2048, Messages: messages,
		Tools: []Tool{tool}, ReasoningEffort: "low",
	}); err != nil {
		t.Fatalf("second tool call: %v", err)
	}
	if got := *efforts; len(got) != 1 || got[0] != "none" {
		t.Fatalf("second tool call efforts = %v, want a single none", got)
	}

	// The tools-withdrawn final call may still reason: the provider only
	// restricts function tools.
	*efforts = nil
	if _, err := c.Chat(context.Background(), ChatRequest{
		Model: "gpt-6-luna", MaxTokens: 2048, Messages: messages, ReasoningEffort: "low",
	}); err != nil {
		t.Fatalf("tools-withdrawn call: %v", err)
	}
	if got := *efforts; len(got) != 1 || got[0] != "low" {
		t.Fatalf("tools-withdrawn efforts = %v, want low preserved", got)
	}
}

// TestChatDoesNotRetryReasoningWithoutTools keeps an unrelated refusal from
// triggering the tools-only recovery: the same error on a call that carries no
// tools is returned to the caller after one attempt.
func TestChatDoesNotRetryReasoningWithoutTools(t *testing.T) {
	srv, efforts := toolsReasoningServer(t, true)
	defer srv.Close()
	c, err := NewClient(srv.URL+"/v1", "", srv.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = c.Chat(context.Background(), ChatRequest{
		Model: "gpt-6-luna", MaxTokens: 2048,
		Messages:        []ChatMessage{{Role: "user", Content: "q"}},
		ReasoningEffort: "low",
	})
	if err == nil || !strings.Contains(err.Error(), "reasoning_effort") {
		t.Fatalf("err = %v, want the provider complaint", err)
	}
	if got := *efforts; len(got) != 1 {
		t.Errorf("attempts = %v, want exactly one (no tools, so no recovery)", got)
	}
}
