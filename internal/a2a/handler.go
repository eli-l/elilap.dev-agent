package a2a

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/audit"
	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

const (
	// MaxRequestBody caps one JSON-RPC body; a task is a question, not a
	// document.
	MaxRequestBody = 64 << 10
	// textPlain is the one output mode this agent produces.
	textPlain = "text/plain"
)

// Answerer is the grounded loop entry point; *agentloop.Runner implements it.
type Answerer interface {
	Run(ctx context.Context, question string) (agentloop.Answer, error)
}

// Handler serves the A2A JSON-RPC endpoint (`message/send` and the task
// methods) for one loop. Authentication, rate limiting, and request logging
// are transport middleware in cmd/agent, so the handler stays testable with a
// stub answerer.
type Handler struct {
	Loop Answerer
	// MaxElapsed bounds one task; zero uses agentloop.DefaultMaxElapsed.
	MaxElapsed time.Duration
}

// NewHandler builds the JSON-RPC handler around a loop.
func NewHandler(loop Answerer) *Handler { return &Handler{Loop: loop} }

// ServeHTTP implements one A2A JSON-RPC request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeTransportError(w, http.StatusMethodNotAllowed, CodeInvalidRequest, "use POST")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeTransportError(w, http.StatusUnsupportedMediaType, CodeInvalidRequest,
			"Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBody)
	dec := json.NewDecoder(r.Body)
	var req Request
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeTransportError(w, http.StatusRequestEntityTooLarge, CodeInvalidRequest, "request body too large")
			return
		}
		writeRPCError(w, http.StatusOK, nil, &Error{Code: CodeParseError, Message: "invalid JSON payload"})
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeTransportError(w, http.StatusRequestEntityTooLarge, CodeInvalidRequest, "request body too large")
			return
		}
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodeInvalidRequest,
			Message: "body must contain exactly one JSON-RPC request"})
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodeInvalidRequest,
			Message: `jsonrpc must be "2.0"`})
		return
	}
	if strings.TrimSpace(req.Method) == "" {
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodeInvalidRequest,
			Message: "method is required"})
		return
	}
	// A2A requests require an id; without one this is a JSON-RPC
	// notification, and the spec forbids replying to those. Reject it at the
	// transport layer instead of pretending it was a request.
	if len(req.ID) == 0 {
		writeTransportError(w, http.StatusBadRequest, CodeInvalidRequest, "id is required")
		return
	}
	if !validID(req.ID) {
		writeRPCError(w, http.StatusOK, nil, &Error{Code: CodeInvalidRequest,
			Message: "id must be a string, a number, or null"})
		return
	}

	switch req.Method {
	case MethodSendMessage:
		result, rpcErr := h.sendMessage(r, req.Params)
		if rpcErr != nil {
			writeRPCError(w, http.StatusOK, req.ID, rpcErr)
			return
		}
		writeRPCResult(w, req.ID, result)
	case MethodGetTask, MethodCancelTask:
		// This service answers each message synchronously and keeps no task
		// store: it never returns a Task, so no task id can be known here.
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodeTaskNotFound, Message: "Task not found"})
	case MethodStreamMessage, MethodResubscribe:
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodeUnsupported,
			Message: "streaming is not supported"})
	case MethodPushConfigSet, MethodPushConfigGet, MethodPushConfigList, MethodPushConfigDelete:
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodePushNotSupported,
			Message: "push notifications are not supported"})
	case MethodExtendedCard:
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodeExtendedCardNotConfigured,
			Message: "authenticated extended card is not configured"})
	default:
		writeRPCError(w, http.StatusOK, req.ID, &Error{Code: CodeMethodNotFound, Message: "Method not found"})
	}
}

// sendMessage runs the grounded loop for one peer message and returns an A2A
// agent Message carrying the answer plus citation metadata.
func (h *Handler) sendMessage(r *http.Request, raw json.RawMessage) (*Message, *Error) {
	var params MessageSendParams
	if len(raw) == 0 {
		return nil, &Error{Code: CodeInvalidParams, Message: "params are required"}
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "params must be a message/send object"}
	}
	msg := params.Message
	if msg == nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "message is required"}
	}
	if msg.Kind != "message" {
		return nil, &Error{Code: CodeInvalidParams, Message: `message.kind must be "message"`}
	}
	if msg.Role != "user" {
		return nil, &Error{Code: CodeInvalidParams, Message: `message.role must be "user"`}
	}
	if strings.TrimSpace(msg.MessageID) == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: "message.messageId is required"}
	}
	// No task store exists, so a task id cannot refer to anything this service
	// answered. Reporting it is more honest than inventing a task.
	if msg.TaskID != "" {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	question, rpcErr := textFromParts(msg.Parts)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if len(question) > agentloop.MaxQuestionBytes {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("question must be at most %d bytes", agentloop.MaxQuestionBytes)}
	}
	if rpcErr := checkOutputModes(params.Configuration); rpcErr != nil {
		return nil, rpcErr
	}
	if params.Configuration != nil && len(params.Configuration.PushNotificationConfig) > 0 {
		return nil, &Error{Code: CodePushNotSupported, Message: "push notifications are not supported"}
	}

	maxElapsed := h.MaxElapsed
	if maxElapsed <= 0 {
		maxElapsed = agentloop.DefaultMaxElapsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxElapsed)
	defer cancel()

	start := time.Now()
	answer, err := h.Loop.Run(ctx, question)
	tokenID := ""
	if tok, ok := auth.TokenFrom(r.Context()); ok {
		tokenID = tok.ID
	}
	entry := audit.Entry{
		TokenID:  tokenID,
		Method:   "a2a/" + MethodSendMessage,
		Question: question,
		Elapsed:  time.Since(start),
	}
	if err != nil {
		switch {
		case errors.Is(err, agentloop.ErrBudget), errors.Is(err, context.DeadlineExceeded):
			entry.Outcome = audit.OutcomeTimeout
			audit.Answer(entry)
			return nil, &Error{Code: CodeServerError, Message: "answer timed out", Data: map[string]any{"retryable": true}}
		case errors.Is(err, context.Canceled):
			// The peer disconnected. The backend did not fail, and nobody
			// will read the response; record the task without an error log.
			entry.Outcome = audit.OutcomeCanceled
			audit.Answer(entry)
			return nil, &Error{Code: CodeServerError, Message: "request canceled"}
		default:
			entry.Outcome = audit.OutcomeError
			audit.Answer(entry)
			log := logging.HTTP()
			log.Error().Err(err).Msg("a2a task failed")
			return nil, &Error{Code: CodeInternalError, Message: "answer failed"}
		}
	}

	entry.ToolIterations = answer.ToolIterations
	entry.Citations = len(answer.Citations)
	entry.Refused = answer.Refused
	if answer.Refused {
		entry.Outcome = audit.OutcomeRefused
	} else {
		entry.Outcome = audit.OutcomeAnswered
	}
	audit.Answer(entry)

	citations := answer.Citations
	if citations == nil {
		citations = []agentloop.Citation{}
	}
	contextID := strings.TrimSpace(msg.ContextID)
	if contextID == "" {
		contextID = newID()
	}
	return &Message{
		Kind:      "message",
		Role:      "agent",
		MessageID: newID(),
		ContextID: contextID,
		Parts:     []Part{{Kind: "text", Text: answer.Answer}},
		Metadata: map[string]any{
			"citations":       citations,
			"refused":         answer.Refused,
			"tool_iterations": answer.ToolIterations,
			"elapsed_ms":      answer.ElapsedMS,
		},
	}, nil
}

// textFromParts joins the peer's text parts into one question. Files and
// structured data are not part of this agent's input modes, and naming the
// unsupported kind tells the client what went wrong.
func textFromParts(parts []Part) (string, *Error) {
	if len(parts) == 0 {
		return "", &Error{Code: CodeInvalidParams, Message: "message.parts must not be empty"}
	}
	texts := make([]string, 0, len(parts))
	for i, part := range parts {
		if part.Kind != "text" {
			return "", &Error{
				Code:    CodeContentType,
				Message: "Incompatible content types",
				Data: map[string]any{
					"part":                  i,
					"kind":                  part.Kind,
					"supported_input_modes": []string{textPlain},
				},
			}
		}
		if text := strings.TrimSpace(part.Text); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return "", &Error{Code: CodeInvalidParams, Message: "message.parts must contain text"}
	}
	return strings.Join(texts, "\n\n"), nil
}

// checkOutputModes rejects a request that cannot accept a text answer.
func checkOutputModes(cfg *MessageSendConfig) *Error {
	if cfg == nil || len(cfg.AcceptedOutputModes) == 0 {
		return nil
	}
	for _, raw := range cfg.AcceptedOutputModes {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		switch strings.ToLower(mediaType) {
		case textPlain, "text/*", "*/*":
			return nil
		}
	}
	return &Error{
		Code:    CodeContentType,
		Message: "Incompatible content types",
		Data:    map[string]any{"supported_output_modes": []string{textPlain}},
	}
}

// validID accepts the JSON-RPC id shapes: string, number, or null.
func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return false
	}
	switch v.(type) {
	case nil, string, float64:
		return true
	default:
		return false
	}
}

// newID returns a random 128-bit identifier as hex. A2A only requires an
// opaque unique string per message and context.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; a panic is better
		// than a silently duplicated id.
		panic("a2a: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// writeRPCResult writes a successful JSON-RPC response.
func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: id, Result: result})
}

// writeRPCError writes a JSON-RPC error response with the given HTTP status.
// Protocol-level errors use 200; transport failures (auth, rate limit, body
// size, content type) use their real status.
func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, rpcErr *Error) {
	if id == nil {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: id, Error: rpcErr})
}

// writeTransportError is the shared shape for a failure that happens before
// the JSON-RPC request is understood.
func writeTransportError(w http.ResponseWriter, status, code int, message string) {
	writeRPCError(w, status, nil, &Error{Code: code, Message: message})
}
