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
		Model:     "chat-model",
		MaxTokens: 512,
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
