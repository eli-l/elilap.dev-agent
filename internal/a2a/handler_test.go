package a2a_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
)

// answererFunc adapts a function to a2a.Answerer.
type answererFunc func(context.Context, string) (agentloop.Answer, error)

func (f answererFunc) Run(ctx context.Context, q string) (agentloop.Answer, error) { return f(ctx, q) }

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// envelope is the subset of a JSON-RPC response these tests inspect.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *a2a.Error      `json:"error"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return env
}

func sendBody(question string) string {
	return `{
		"jsonrpc": "2.0",
		"id": 7,
		"method": "message/send",
		"params": {
			"message": {
				"kind": "message",
				"role": "user",
				"messageId": "m-1",
				"parts": [{"kind": "text", "text": ` + strconvQuote(question) + `}]
			}
		}
	}`
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestSendMessageReturnsAgentMessage(t *testing.T) {
	var got string
	loop := answererFunc(func(_ context.Context, q string) (agentloop.Answer, error) {
		got = q
		return agentloop.Answer{
			Answer:         "Grounded answer.",
			Citations:      []agentloop.Citation{{ChunkID: "c1", SourceURL: "https://read.elilap.dev/x"}},
			ToolIterations: 2,
			ElapsedMS:      1234,
		}, nil
	})
	rec := post(t, a2a.NewHandler(loop), sendBody("what about harnesses?"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got != "what about harnesses?" {
		t.Errorf("loop question = %q", got)
	}
	env := decode(t, rec)
	if env.Error != nil {
		t.Fatalf("error = %+v", env.Error)
	}
	if string(env.ID) != "7" {
		t.Errorf("id = %s, want the request id 7", env.ID)
	}
	var msg a2a.Message
	if err := json.Unmarshal(env.Result, &msg); err != nil {
		t.Fatalf("decode message: %v (%s)", err, env.Result)
	}
	if msg.Kind != "message" || msg.Role != "agent" {
		t.Errorf("message = %+v, want an agent message", msg)
	}
	if msg.MessageID == "" || msg.ContextID == "" {
		t.Errorf("message ids = %q/%q, want generated ids", msg.MessageID, msg.ContextID)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text != "Grounded answer." {
		t.Errorf("parts = %+v", msg.Parts)
	}
	citations, ok := msg.Metadata["citations"].([]any)
	if !ok || len(citations) != 1 {
		t.Fatalf("metadata citations = %v", msg.Metadata["citations"])
	}
	citation := citations[0].(map[string]any)
	if citation["chunk_id"] != "c1" || citation["source_url"] != "https://read.elilap.dev/x" {
		t.Errorf("citation = %v", citation)
	}
	if msg.Metadata["refused"] != false || msg.Metadata["tool_iterations"] != float64(2) {
		t.Errorf("metadata = %v", msg.Metadata)
	}
}

func TestSendMessageEchoesPeerContextID(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{Answer: "ok"}, nil
	})
	body := strings.Replace(sendBody("q"), `"messageId": "m-1"`, `"messageId": "m-1", "contextId": "ctx-9"`, 1)
	env := decode(t, post(t, a2a.NewHandler(loop), body))
	var msg a2a.Message
	if err := json.Unmarshal(env.Result, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.ContextID != "ctx-9" {
		t.Errorf("contextId = %q, want the peer's ctx-9", msg.ContextID)
	}
}

func TestSendMessageRefusal(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{Answer: agentloop.Refusal, Refused: true}, nil
	})
	env := decode(t, post(t, a2a.NewHandler(loop), sendBody("off corpus")))
	var msg a2a.Message
	if err := json.Unmarshal(env.Result, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.Parts[0].Text != agentloop.Refusal || msg.Metadata["refused"] != true {
		t.Errorf("refusal = %+v", msg)
	}
	citations, ok := msg.Metadata["citations"].([]any)
	if !ok || len(citations) != 0 {
		t.Errorf("citations = %v, want []", msg.Metadata["citations"])
	}
}

func TestSendMessageRejectsBadMessages(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
	}{
		{
			name: "agent role",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"agent","messageId":"m","parts":[{"kind":"text","text":"q"}]}}}`,
			code: a2a.CodeInvalidParams,
		},
		{
			name: "no message",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{}}`,
			code: a2a.CodeInvalidParams,
		},
		{
			name: "no parts",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[]}}}`,
			code: a2a.CodeInvalidParams,
		},
		{
			name: "file part",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"file","file":{"uri":"https://x","mimeType":"text/plain"}}]}}}`,
			code: a2a.CodeContentType,
		},
		{
			name: "blank text",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"   "}]}}}`,
			code: a2a.CodeInvalidParams,
		},
		{
			name: "missing message id",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","parts":[{"kind":"text","text":"q"}]}}}`,
			code: a2a.CodeInvalidParams,
		},
		{
			name: "task id",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","taskId":"t-1","parts":[{"kind":"text","text":"q"}]}}}`,
			code: a2a.CodeTaskNotFound,
		},
		{
			name: "output mode",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"q"}]},"configuration":{"acceptedOutputModes":["image/png"]}}}`,
			code: a2a.CodeContentType,
		},
		{
			name: "push notification",
			body: `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"q"}]},"configuration":{"pushNotificationConfig":{"url":"https://x"}}}}`,
			code: a2a.CodePushNotSupported,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
				called = true
				return agentloop.Answer{}, nil
			})
			env := decode(t, post(t, a2a.NewHandler(loop), tc.body))
			if env.Error == nil || env.Error.Code != tc.code {
				t.Fatalf("error = %+v, want code %d", env.Error, tc.code)
			}
			if called {
				t.Error("the loop ran for a rejected message")
			}
		})
	}
}

func TestSendMessageRejectsOversizedQuestion(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Error("the loop ran for an oversized question")
		return agentloop.Answer{}, nil
	})
	question := strings.Repeat("x", agentloop.MaxQuestionBytes+1)
	env := decode(t, post(t, a2a.NewHandler(loop), sendBody(question)))
	if env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Fatalf("error = %+v, want invalid params", env.Error)
	}
}

func TestSendMessageMapsLoopFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{name: "budget", err: agentloop.ErrBudget, code: a2a.CodeServerError},
		{name: "deadline", err: context.DeadlineExceeded, code: a2a.CodeServerError},
		{name: "backend", err: errors.New("chat returned 503"), code: a2a.CodeInternalError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
				return agentloop.Answer{}, tc.err
			})
			rec := post(t, a2a.NewHandler(loop), sendBody("q"))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 for a protocol error", rec.Code)
			}
			env := decode(t, rec)
			if env.Error == nil || env.Error.Code != tc.code {
				t.Fatalf("error = %+v, want code %d", env.Error, tc.code)
			}
			if strings.Contains(string(env.Result), "chat returned") {
				t.Error("the backend error leaked into the response")
			}
		})
	}
}

func TestUnsupportedMethods(t *testing.T) {
	tests := []struct {
		method string
		code   int
	}{
		{a2a.MethodGetTask, a2a.CodeTaskNotFound},
		{a2a.MethodCancelTask, a2a.CodeTaskNotFound},
		{a2a.MethodStreamMessage, a2a.CodeUnsupported},
		{a2a.MethodResubscribe, a2a.CodeUnsupported},
		{a2a.MethodPushConfigSet, a2a.CodePushNotSupported},
		{a2a.MethodExtendedCard, a2a.CodeExtendedCardNotConfigured},
		{"tasks/unknown", a2a.CodeMethodNotFound},
	}
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	})
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			env := decode(t, post(t, a2a.NewHandler(loop),
				`{"jsonrpc":"2.0","id":"x","method":"`+tc.method+`","params":{"id":"t-1"}}`))
			if env.Error == nil || env.Error.Code != tc.code {
				t.Fatalf("error = %+v, want code %d", env.Error, tc.code)
			}
			if string(env.ID) != `"x"` {
				t.Errorf("id = %s, want the string id echoed", env.ID)
			}
		})
	}
}

func TestTransportErrors(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Error("the loop ran for a rejected transport request")
		return agentloop.Answer{}, nil
	})
	h := a2a.NewHandler(loop)

	t.Run("method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/a2a", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("status/allow = %d/%q", rec.Code, rec.Header().Get("Allow"))
		}
	})
	t.Run("content type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(sendBody("q")))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d, want 415", rec.Code)
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		env := decode(t, post(t, h, `{`))
		if env.Error == nil || env.Error.Code != a2a.CodeParseError {
			t.Errorf("error = %+v, want parse error", env.Error)
		}
	})
	t.Run("wrong jsonrpc", func(t *testing.T) {
		env := decode(t, post(t, h, `{"jsonrpc":"1.0","id":1,"method":"message/send","params":{}}`))
		if env.Error == nil || env.Error.Code != a2a.CodeInvalidRequest {
			t.Errorf("error = %+v, want invalid request", env.Error)
		}
	})
	t.Run("bad id", func(t *testing.T) {
		env := decode(t, post(t, h, `{"jsonrpc":"2.0","id":{"x":1},"method":"message/send","params":{}}`))
		if env.Error == nil || env.Error.Code != a2a.CodeInvalidRequest {
			t.Errorf("error = %+v, want invalid request", env.Error)
		}
	})
}

func TestDefaultMaxElapsedIsApplied(t *testing.T) {
	h := a2a.NewHandler(answererFunc(func(ctx context.Context, _ string) (agentloop.Answer, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the loop context has no deadline")
		}
		return agentloop.Answer{Answer: "ok"}, nil
	}))
	post(t, h, sendBody("q"))
}

// TestMessageTypesRoundTrip keeps the wire types honest: an agent message
// marshals the fields A2A v0.3 requires.
func TestMessageTypesRoundTrip(t *testing.T) {
	msg := a2a.Message{
		Kind: "message", Role: "agent", MessageID: "m", ContextID: "c",
		Parts:    []a2a.Part{{Kind: "text", Text: "hi"}},
		Metadata: map[string]any{"refused": false},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"kind":"message"`, `"role":"agent"`, `"messageId":"m"`, `"contextId":"c"`, `"parts":[{"kind":"text","text":"hi"}]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("message JSON %s does not contain %s", b, want)
		}
	}
}

func TestSendMessageOutputModes(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{Answer: "ok"}, nil
	})
	body := func(modes string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"q"}]},"configuration":{"acceptedOutputModes":[` + modes + `]}}}`
	}
	for _, modes := range []string{`"text/plain"`, `"text/plain; charset=utf-8"`, `"text/*"`, `"*/*"`} {
		env := decode(t, post(t, a2a.NewHandler(loop), body(modes)))
		if env.Error != nil {
			t.Errorf("modes %s rejected: %+v", modes, env.Error)
		}
	}
	for _, modes := range []string{`"image/png"`, `"text/markdown"`, `"not a media type"`} {
		env := decode(t, post(t, a2a.NewHandler(loop), body(modes)))
		if env.Error == nil || env.Error.Code != a2a.CodeContentType {
			t.Errorf("modes %s = %+v, want content-type error", modes, env.Error)
		}
	}
}

func TestSendMessageOversizedBodyIs413(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Error("the loop ran for an oversized body")
		return agentloop.Answer{}, nil
	})
	body := `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"` +
		strings.Repeat("x", a2a.MaxRequestBody+100) + `"}]}}}`
	rec := post(t, a2a.NewHandler(loop), body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	env := decode(t, rec)
	if env.Error == nil || env.Error.Code != a2a.CodeInvalidRequest {
		t.Errorf("error = %+v, want invalid request", env.Error)
	}
}

func TestRequestWithoutIDIsRejectedAsNotification(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Error("the loop ran for an id-less notification")
		return agentloop.Answer{}, nil
	})
	rec := post(t, a2a.NewHandler(loop),
		`{"jsonrpc":"2.0","method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"m","parts":[{"kind":"text","text":"q"}]}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	env := decode(t, rec)
	if env.Error == nil || env.Error.Code != a2a.CodeInvalidRequest {
		t.Errorf("error = %+v, want invalid request", env.Error)
	}
}

func TestOversizedTrailingDataIs413(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Error("the loop ran for an oversized body")
		return agentloop.Answer{}, nil
	})
	body := sendBody("q") + strings.Repeat(" ", a2a.MaxRequestBody)
	rec := post(t, a2a.NewHandler(loop), body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
	}
}
