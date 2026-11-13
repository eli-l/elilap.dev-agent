package a2a_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/budget"
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
			Answer: "Grounded answer.",
			Citations: []agentloop.Citation{{
				SourceKind: "ghost_post", SourceURL: "https://read.elilap.dev/x",
				Title: "A post", PublishedAt: "2026-11-02T13:21:35Z", Excerpt: "the excerpt",
			}},
			Sources: []agentloop.Source{{
				Kind: "ghost_post", URL: "https://read.elilap.dev/x", Title: "A post",
				Author: "Eli", PublishedAt: "2026-11-02T13:21:35Z",
			}},
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
	if citation["source_url"] != "https://read.elilap.dev/x" || citation["excerpt"] != "the excerpt" {
		t.Errorf("citation = %v", citation)
	}
	// The internal identifier never reaches a peer, in the metadata or anywhere
	// else in the message.
	if _, ok := citation["chunk_id"]; ok {
		t.Errorf("citation exposes a chunk_id: %v", citation)
	}
	sources, ok := msg.Metadata["sources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("metadata sources = %v", msg.Metadata["sources"])
	}
	if source := sources[0].(map[string]any); source["platform"] != nil ||
		source["author"] != "Eli" || source["url"] != "https://read.elilap.dev/x" {
		t.Errorf("source = %v", source)
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
	if sources, ok := msg.Metadata["sources"].([]any); !ok || len(sources) != 0 {
		t.Errorf("sources = %v, want []", msg.Metadata["sources"])
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

// acqFunc adapts a function to a2a.Acquirer.
type acqFunc func(budget.Caller) (func(), error)

func (f acqFunc) Acquire(c budget.Caller) (func(), error) { return f(c) }

// sendBodyWithMeta is sendBody plus optional message metadata.
func sendBodyWithMeta(agentID string) string {
	meta := ""
	if agentID != "" {
		meta = `"metadata":{"agentId":` + strconvQuote(agentID) + `},`
	}
	return `{
		"jsonrpc": "2.0",
		"id": 7,
		"method": "message/send",
		"params": {
			"message": {
				"kind": "message",
				"role": "user",
				"messageId": "m-1",
				` + meta + `
				"parts": [{"kind": "text", "text": "q"}]
			}
		}
	}`
}

// TestMessageSendChargesTheBudgetAndRefuses pins the refusal shape: the real
// 429 status, a Retry-After, the scope in the error data, and no loop run.
func TestMessageSendChargesTheBudgetAndRefuses(t *testing.T) {
	var got budget.Caller
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Error("the loop ran for a refused answer")
		return agentloop.Answer{}, nil
	})
	h := a2a.NewHandler(loop)
	h.Budget = acqFunc(func(c budget.Caller) (func(), error) {
		got = c
		return nil, &budget.Refusal{Scope: budget.ScopeGlobalDaily, RetryAfter: time.Hour}
	})

	rec := post(t, h, sendBodyWithMeta("peer-7"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body.String())
	}
	if retry := rec.Header().Get("Retry-After"); retry != "3600" {
		t.Errorf("Retry-After = %q, want 3600", retry)
	}
	env := decode(t, rec)
	if env.Error == nil || env.Error.Code != a2a.CodeServerError {
		t.Fatalf("error = %+v, want the documented server error code", env.Error)
	}
	data, _ := env.Error.Data.(map[string]any)
	if data["scope"] != string(budget.ScopeGlobalDaily) {
		t.Errorf("data.scope = %v, want global_daily", data["scope"])
	}
	if data["reason"] != "quota_exceeded" {
		t.Errorf("data.reason = %v, want quota_exceeded", data["reason"])
	}
	if got.Principal.String() != "agent:peer-7" {
		t.Errorf("charged principal = %q, want agent:peer-7", got.Principal.String())
	}
	if got.Address == "" {
		t.Error("caller has no address, so the per-address gate cannot apply")
	}
}

// TestMessageSendIdentityPrecedence pins who an answer is charged to: a bearer
// token beats the self-asserted id, the metadata beats the header, and nothing
// asserted falls back to the peer address.
func TestMessageSendIdentityPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		meta   string
		header string
		token  string
		want   string
	}{
		{name: "metadata", meta: "peer-7", want: "agent:peer-7"},
		{name: "header", header: "peer-8", want: "agent:peer-8"},
		{name: "metadata beats header", meta: "peer-7", header: "peer-8", want: "agent:peer-7"},
		{name: "nothing asserted", want: "ip:192.0.2.1"},
		{name: "token wins", meta: "peer-7", token: "alice", want: "token:alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got budget.Caller
			h := a2a.NewHandler(answererFunc(func(context.Context, string) (agentloop.Answer, error) {
				return agentloop.Answer{Answer: "ok"}, nil
			}))
			h.Budget = acqFunc(func(c budget.Caller) (func(), error) {
				got = c
				return func() {}, nil
			})

			req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(sendBodyWithMeta(tc.meta)))
			req.Header.Set("Content-Type", "application/json")
			if tc.header != "" {
				req.Header.Set("X-Agent-Id", tc.header)
			}
			if tc.token != "" {
				req = req.WithContext(auth.WithToken(req.Context(), auth.Token{ID: tc.token, Value: "v"}))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if got.Principal.String() != tc.want {
				t.Errorf("charged principal = %q, want %q", got.Principal.String(), tc.want)
			}
		})
	}
}

func TestMessageSendRejectsInvalidAgentID(t *testing.T) {
	loop := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Error("the loop ran for an invalid agentId")
		return agentloop.Answer{}, nil
	})
	h := a2a.NewHandler(loop)
	h.Budget = acqFunc(func(budget.Caller) (func(), error) {
		t.Error("the budget was charged for an invalid agentId")
		return func() {}, nil
	})

	rec := post(t, h, sendBodyWithMeta("peer 7"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the JSON-RPC 200 transport", rec.Code)
	}
	env := decode(t, rec)
	if env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("error = %+v, want invalid params", env.Error)
	}
}
