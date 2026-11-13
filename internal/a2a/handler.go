package a2a

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/audit"
	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/budget"
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

// Acquirer charges one answer to a caller; *budget.Limiter implements it. A
// nil Acquirer disables the spend budget (private deployments and tests).
// The charge happens after the message is validated and immediately before the
// loop runs, so a malformed body costs no quota.
type Acquirer interface {
	Acquire(budget.Caller) (func(), error)
}

// Handler serves the A2A JSON-RPC endpoint (`message/send` and the task
// methods) for one loop. Authentication, request logging, and the embedding
// bucket are transport middleware in cmd/agent; the answer budget is here
// because it is keyed by the peer's self-asserted identity, which only the
// parsed message carries.
type Handler struct {
	Loop Answerer
	// MaxElapsed bounds one task; zero uses agentloop.DefaultMaxElapsed.
	MaxElapsed time.Duration
	// Budget charges an answer; nil disables it.
	Budget Acquirer
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
		result, rpcErr, refusal := h.sendMessage(r, req.Params)
		if refusal != nil {
			writeBudgetError(w, req.ID, refusal, budget.PeerAddress(r))
			return
		}
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
// agent Message carrying the answer plus citation metadata. A budget refusal is
// reported separately so the transport can answer 429 with a Retry-After.
func (h *Handler) sendMessage(r *http.Request, raw json.RawMessage) (*Message, *Error, *budget.Refusal) {
	var params MessageSendParams
	if len(raw) == 0 {
		return nil, &Error{Code: CodeInvalidParams, Message: "params are required"}, nil
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "params must be a message/send object"}, nil
	}
	msg := params.Message
	if msg == nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "message is required"}, nil
	}
	if msg.Kind != "message" {
		return nil, &Error{Code: CodeInvalidParams, Message: `message.kind must be "message"`}, nil
	}
	if msg.Role != "user" {
		return nil, &Error{Code: CodeInvalidParams, Message: `message.role must be "user"`}, nil
	}
	if strings.TrimSpace(msg.MessageID) == "" {
		return nil, &Error{Code: CodeInvalidParams, Message: "message.messageId is required"}, nil
	}
	// No task store exists, so a task id cannot refer to anything this service
	// answered. Reporting it is more honest than inventing a task.
	if msg.TaskID != "" {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}, nil
	}
	question, rpcErr := textFromParts(msg.Parts)
	if rpcErr != nil {
		return nil, rpcErr, nil
	}
	if len(question) > agentloop.MaxQuestionBytes {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("question must be at most %d bytes", agentloop.MaxQuestionBytes)}, nil
	}
	if rpcErr := checkOutputModes(params.Configuration); rpcErr != nil {
		return nil, rpcErr, nil
	}
	if params.Configuration != nil && len(params.Configuration.PushNotificationConfig) > 0 {
		return nil, &Error{Code: CodePushNotSupported, Message: "push notifications are not supported"}, nil
	}

	principal, rpcErr := a2aPrincipal(r, msg)
	if rpcErr != nil {
		return nil, rpcErr, nil
	}
	if h.Budget != nil {
		release, err := h.Budget.Acquire(budget.Caller{
			Principal: principal,
			Address:   budget.PeerAddress(r),
		})
		if err != nil {
			if refusal, ok := budget.AsRefusal(err); ok {
				return nil, nil, refusal
			}
			log := logging.HTTP()
			log.Error().Err(err).Msg("a2a budget unavailable")
			return nil, &Error{Code: CodeInternalError, Message: "answer failed"}, nil
		}
		defer release()
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
		Principal: principal.String(),
		TokenID:   tokenID,
		Method:    "a2a/" + MethodSendMessage,
		Question:  question,
		Elapsed:   time.Since(start),
	}
	if err != nil {
		switch {
		case errors.Is(err, agentloop.ErrBudget), errors.Is(err, context.DeadlineExceeded):
			entry.Outcome = audit.OutcomeTimeout
			audit.Answer(entry)
			return nil, &Error{Code: CodeServerError, Message: "answer timed out", Data: map[string]any{"retryable": true}}, nil
		case errors.Is(err, context.Canceled):
			// The peer disconnected. The backend did not fail, and nobody
			// will read the response; record the task without an error log.
			entry.Outcome = audit.OutcomeCanceled
			audit.Answer(entry)
			return nil, &Error{Code: CodeServerError, Message: "request canceled"}, nil
		default:
			entry.Outcome = audit.OutcomeError
			audit.Answer(entry)
			log := logging.HTTP()
			log.Error().Err(err).Msg("a2a task failed")
			return nil, &Error{Code: CodeInternalError, Message: "answer failed"}, nil
		}
	}

	entry.ToolIterations = answer.ToolIterations
	entry.Citations = len(answer.Citations)
	entry.Sources = len(answer.Sources)
	entry.Refused = answer.Refused
	if answer.Refused {
		entry.Outcome = audit.OutcomeRefused
	} else {
		entry.Outcome = audit.OutcomeAnswered
	}
	audit.Answer(entry)

	// Both reference lists are always arrays, even on a refusal, and neither
	// carries a chunk id: the id is an internal identifier that is logged but
	// never returned on any surface.
	citations := answer.Citations
	if citations == nil {
		citations = []agentloop.Citation{}
	}
	sources := answer.Sources
	if sources == nil {
		sources = []agentloop.Source{}
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
			"sources":         sources,
			"refused":         answer.Refused,
			"tool_iterations": answer.ToolIterations,
			"elapsed_ms":      answer.ElapsedMS,
		},
	}, nil, nil
}

// a2aPrincipal resolves who a peer's answer is charged to. A configured bearer
// token is the only verified identity and wins; then the self-asserted agentId
// from the message metadata or the X-Agent-Id header; then the peer address.
func a2aPrincipal(r *http.Request, msg *Message) (budget.Principal, *Error) {
	if tok, ok := auth.TokenFrom(r.Context()); ok {
		return budget.Principal{Kind: budget.KindToken, ID: tok.ID}, nil
	}
	id := ""
	if raw, ok := msg.Metadata["agentId"]; ok {
		s, ok := raw.(string)
		if !ok {
			return budget.Principal{}, &Error{Code: CodeInvalidParams, Message: "metadata.agentId must be a string"}
		}
		id = strings.TrimSpace(s)
	}
	if id == "" {
		id = strings.TrimSpace(r.Header.Get("X-Agent-Id"))
	}
	if id == "" {
		return budget.Principal{Kind: budget.KindAddr, ID: budget.PeerAddress(r)}, nil
	}
	if !budget.ValidID(id) {
		return budget.Principal{}, &Error{Code: CodeInvalidParams,
			Message: "agentId must match [A-Za-z0-9._-]{1,64}"}
	}
	return budget.Principal{Kind: budget.KindAgent, ID: id}, nil
}

// budgetLog bounds refusal warnings: /a2a is reachable without credentials, so
// one line per peer address per minute is the ceiling on this path.
var budgetLog = logging.NewThrottle(time.Minute)

// writeBudgetError writes the 429 the A2A transport uses for a budget refusal:
// the real status (not JSON-RPC 200), a Retry-After header, and the scope in
// the error data so a client can tell a minute burst from an exhausted day.
func writeBudgetError(w http.ResponseWriter, id json.RawMessage, refusal *budget.Refusal, addr string) {
	seconds := int(math.Ceil(refusal.RetryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	if budgetLog.Allow(addr) {
		log := logging.HTTP()
		log.Warn().
			Str("peer", addr).
			Str("scope", string(refusal.Scope)).
			Int("retry_after_s", seconds).
			Msg("a2a budget refused")
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeRPCError(w, http.StatusTooManyRequests, id, &Error{
		Code:    CodeServerError,
		Message: refusal.Message(),
		Data: map[string]any{
			"reason":              "quota_exceeded",
			"scope":               string(refusal.Scope),
			"retry_after_seconds": seconds,
		},
	})
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
