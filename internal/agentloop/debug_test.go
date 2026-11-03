package agentloop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

// debugLog installs a JSON logger into a buffer for the duration of one test.
func debugLog(t *testing.T, level string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := logging.Setup(logging.Config{Level: level, Format: "json"}, &buf); err != nil {
		t.Fatalf("setup logging: %v", err)
	}
	t.Cleanup(logging.Reset)
	return &buf
}

// records decodes the JSON log lines written so far, grouped by message.
func records(t *testing.T, buf *bytes.Buffer) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		msg, _ := rec["message"].(string)
		out[msg] = append(out[msg], rec)
	}
	return out
}

func TestDebugLogsTheWholeRAGTrace(t *testing.T) {
	buf := debugLog(t, "debug")
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness lock-in"}`)),
		answerResponse("Harness lock-in (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "harnesses are vendor lock-in")}}
	if _, err := newRunner(chatter, searcher).Run(context.Background(), "What is the harness take?"); err != nil {
		t.Fatalf("run: %v", err)
	}

	recs := records(t, buf)
	for _, msg := range []string{
		"rag: run starting", "rag: chat request", "rag: message",
		"rag: chat response", "rag: tool call", "rag: tool hits",
		"rag: tool result", "rag: answer",
	} {
		if len(recs[msg]) == 0 {
			t.Errorf("no %q line; got %v", msg, keys(recs))
		}
	}

	// The request header names the chat model and the one offered tool.
	header := recs["rag: chat request"][0]
	if header["model"] != "chat-model" || header["tools_offered"] != true {
		t.Errorf("request header = %v, want the chat model and tools offered", header)
	}
	if names, _ := header["tool_names"].([]any); len(names) != 1 || names[0] != agentloop.ToolName {
		t.Errorf("tool_names = %v, want [%s]", header["tool_names"], agentloop.ToolName)
	}

	// Every message the model sees is in the trace: the question, the tool call
	// echo, and the tool result with the full excerpt the model may cite.
	var sawQuestion, sawToolResult bool
	for _, rec := range recs["rag: message"] {
		content, _ := rec["content"].(string)
		if rec["role"] == "user" && strings.Contains(content, "What is the harness take?") {
			sawQuestion = true
		}
		if rec["role"] == "tool" && strings.Contains(content, idA) &&
			strings.Contains(content, "vendor lock-in") {
			sawToolResult = true
		}
	}
	if !sawQuestion || !sawToolResult {
		t.Errorf("message lines lack the question (%v) or the tool result (%v)", sawQuestion, sawToolResult)
	}

	// Both replies are traced with the served model and what it returned.
	responses := recs["rag: chat response"]
	if len(responses) != 2 {
		t.Fatalf("chat response lines = %d, want 2", len(responses))
	}
	if !strings.Contains(stringOf(responses[0]["tool_calls"]), agentloop.ToolName) {
		t.Errorf("first response tool_calls = %v, want the search call", responses[0]["tool_calls"])
	}
	if first := stringOf(responses[0]["finish_reason"]); first != "tool_calls" && first != "" {
		t.Errorf("first finish_reason = %q", first)
	}
	if !strings.Contains(stringOf(responses[1]["content"]), idA) {
		t.Errorf("second response content = %v, want the cited answer", responses[1]["content"])
	}

	// The tool result line is the retrieval-to-prompt boundary.
	if res := recs["rag: tool result"][0]; !strings.Contains(stringOf(res["tool_result"]), "vendor lock-in") {
		t.Errorf("tool result = %v, want the excerpt JSON", res["tool_result"])
	}

	// The closing line reports the enforced outcome.
	answer := recs["rag: answer"][0]
	if answer["refused"] != false || answer["tool_iterations"] != float64(1) {
		t.Errorf("answer line = %v, want a grounded answer after one iteration", answer)
	}
	if ids, _ := answer["citations"].([]any); len(ids) != 1 || ids[0] != idA {
		t.Errorf("citations = %v, want [%s]", answer["citations"], idA)
	}
}

func TestInfoLevelLogsNoRAGPayloads(t *testing.T) {
	buf := debugLog(t, "info")
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Grounded (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "an excerpt body")}}
	if _, err := newRunner(chatter, searcher).Run(context.Background(), "q"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("info level wrote payload lines:\n%s", got)
	}
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func keys(m map[string][]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
