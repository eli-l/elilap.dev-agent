package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/audit"
	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/budget"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/requests"
)

// submissionKey is the message metadata field that turns a question into an
// explicit deferred request. It is only honoured when the deployment opened a
// request store; otherwise the call is `-32004`.
const submissionKey = "knowledgeRequest"

// maxPushTokenBytes matches the A2A PushNotificationConfig token, a shared
// secret echoed on delivery.
const maxPushTokenBytes = 256

// submissionSpec is the parsed `message.metadata.knowledgeRequest` object.
type submissionSpec struct {
	Reason         string
	Context        string
	IdempotencyKey string
	Origin         string
}

// submissionFrom reads and validates the submission marker. The boolean says
// whether the peer asked for a deferred request at all; a malformed marker is
// an error rather than silently falling through to an answer the peer did not
// want.
func submissionFrom(msg *Message) (submissionSpec, bool, *Error) {
	raw, ok := msg.Metadata[submissionKey]
	if !ok || raw == nil {
		return submissionSpec{}, false, nil
	}
	spec := submissionSpec{}
	switch v := raw.(type) {
	case bool:
		if !v {
			return submissionSpec{}, false, nil
		}
	case map[string]any:
		for key, value := range v {
			switch key {
			case "reason":
				s, ok := value.(string)
				if !ok {
					return spec, true, invalidParam("metadata.knowledgeRequest.reason must be a string")
				}
				spec.Reason = strings.TrimSpace(s)
			case "context":
				s, ok := value.(string)
				if !ok {
					return spec, true, invalidParam("metadata.knowledgeRequest.context must be a string")
				}
				spec.Context = strings.TrimSpace(s)
			case "idempotencyKey":
				s, ok := value.(string)
				if !ok {
					return spec, true, invalidParam("metadata.knowledgeRequest.idempotencyKey must be a string")
				}
				spec.IdempotencyKey = strings.TrimSpace(s)
			case "origin":
				s, ok := value.(string)
				if !ok {
					return spec, true, invalidParam("metadata.knowledgeRequest.origin must be a string")
				}
				spec.Origin = strings.TrimSpace(s)
			default:
				return spec, true, invalidParam("metadata.knowledgeRequest has an unknown field: " + key)
			}
		}
	default:
		return spec, true, invalidParam("metadata.knowledgeRequest must be an object or true")
	}
	switch {
	case len(spec.Reason) > requests.MaxReasonBytes:
		return spec, true, invalidParam(fmt.Sprintf("metadata.knowledgeRequest.reason must be at most %d bytes", requests.MaxReasonBytes))
	case len(spec.Context) > requests.MaxContextBytes:
		return spec, true, invalidParam(fmt.Sprintf("metadata.knowledgeRequest.context must be at most %d bytes", requests.MaxContextBytes))
	case spec.IdempotencyKey != "" && !validSubmissionKey(spec.IdempotencyKey):
		return spec, true, invalidParam("metadata.knowledgeRequest.idempotencyKey must match [A-Za-z0-9._-]{1,128}")
	}
	switch spec.Origin {
	case "":
		spec.Origin = requests.OriginPeerRequest
	case requests.OriginInsufficientEvidence, requests.OriginPartial, requests.OriginPeerRequest:
	default:
		return spec, true, invalidParam("metadata.knowledgeRequest.origin must be insufficient_evidence, partial, or peer_request")
	}
	return spec, true, nil
}

// validSubmissionKey keeps the idempotency key a safe opaque identifier: it is
// an index key, and the budget/audit ids use the same alphabet.
func validSubmissionKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// submitRequest persists an explicit deferred question and returns the A2A
// Task that represents it. A submission is a write, not an answer: it is
// charged to the submission budget (when enabled), never to the answer budget,
// and it runs no model call.
func (h *Handler) submitRequest(r *http.Request, msg *Message, question string, spec submissionSpec, principal budget.Principal, sendPush *PushNotificationConfig) (any, *Error, *budget.Refusal) {
	if h.Requests == nil {
		return nil, &Error{
			Code:    CodeUnsupported,
			Message: "knowledge requests are not accepted by this deployment",
		}, nil
	}
	// A retry of an already-stored submission is answered with the request the
	// peer already owns, even when the inbox is full: refusing it would leave
	// the peer unable to learn the id of a row that exists.
	idempotentRetry := false
	if spec.IdempotencyKey != "" {
		existing, found, err := h.Requests.FindByIdempotencyKey(principal.String(), spec.IdempotencyKey)
		if err != nil {
			logRequestError(err, "knowledge request lookup failed")
			return nil, &Error{Code: CodeInternalError, Message: "request store unavailable"}, nil
		}
		if found {
			idempotentRetry = existing.Question == question
			if !idempotentRetry {
				return nil, invalidParam("metadata.knowledgeRequest.idempotencyKey was already used for a different question"), nil
			}
		}
	}
	// The inbox cap is the owner's protection: a public agent must not become
	// unbounded storage. It is checked before the budget so a full inbox does
	// not spend a submission credit.
	if h.MaxPending > 0 && !idempotentRetry {
		pending, err := h.Requests.Pending()
		if err != nil {
			logRequestError(err, "knowledge request count failed")
			return nil, &Error{Code: CodeInternalError, Message: "request store unavailable"}, nil
		}
		if pending >= h.MaxPending {
			// Soft bound: the count and the insert are separate statements, so
			// concurrent submissions can exceed it slightly. It protects disk
			// from a flood, not state from a race.
			return nil, nil, &budget.Refusal{Scope: budget.ScopeInbox, RetryAfter: inboxRetryAfter}
		}
	}
	// Validate the optional send-time webhook before anything is written: a
	// rejected registration must not leave a stored request behind.
	if sendPush != nil {
		if h.Push == nil {
			return nil, &Error{Code: CodePushNotSupported, Message: "push notifications are not supported"}, nil
		}
		if _, err := h.Push.ValidateURL(sendPush.URL); err != nil {
			return nil, invalidParam("configuration.pushNotificationConfig.url must be an https URL to a public host, without userinfo or a fragment"), nil
		}
	}
	// A retry writes no new row, so it must neither spend a submission credit
	// nor be refused when the budget is exhausted: otherwise a peer whose
	// first response was lost cannot learn the id of the row it already owns.
	if h.Submit != nil && !idempotentRetry {
		release, err := h.Submit.Acquire(budget.Caller{
			Principal: principal,
			Address:   budget.PeerAddress(r),
		})
		if err != nil {
			if refusal, ok := budget.AsRefusal(err); ok {
				return nil, nil, refusal
			}
			logRequestError(err, "submission budget unavailable")
			return nil, &Error{Code: CodeInternalError, Message: "submission failed"}, nil
		}
		defer release()
	}

	tokenID := ""
	if tok, ok := auth.TokenFrom(r.Context()); ok {
		tokenID = tok.ID
	}
	contextID := strings.TrimSpace(msg.ContextID)
	if contextID == "" {
		contextID = newID()
	}
	start := time.Now()
	req, created, err := h.Requests.Create(requests.CreateInput{
		Question:       question,
		Reason:         spec.Reason,
		Context:        spec.Context,
		Principal:      principal.String(),
		TokenID:        tokenID,
		IdempotencyKey: spec.IdempotencyKey,
		Origin:         spec.Origin,
		ContextID:      contextID,
		Now:            h.Now,
	})
	entry := audit.Entry{
		Principal: principal.String(),
		TokenID:   tokenID,
		Method:    "a2a/" + MethodSendMessage + "/" + submissionKey,
		Question:  question,
		Outcome:   audit.OutcomeSubmitted,
		Elapsed:   time.Since(start),
	}
	switch {
	case errors.Is(err, requests.ErrIdempotencyConflict):
		// Nothing was stored and the owner was not told, so the audit record
		// says so instead of claiming a submission.
		entry.Outcome = audit.OutcomeError
		audit.Answer(entry)
		return nil, invalidParam("metadata.knowledgeRequest.idempotencyKey was already used for a different question"), nil
	case errors.Is(err, requests.ErrInvalid):
		entry.Outcome = audit.OutcomeError
		audit.Answer(entry)
		return nil, invalidParam(err.Error()), nil
	case err != nil:
		entry.Outcome = audit.OutcomeError
		audit.Answer(entry)
		logRequestError(err, "knowledge request insert failed")
		return nil, &Error{Code: CodeInternalError, Message: "submission failed"}, nil
	}
	audit.Answer(entry)
	pushRegistered := false
	if sendPush != nil {
		if _, err := h.Requests.SetPushConfig(req.ID, sendPush.URL, sendPush.Token); err != nil {
			logRequestError(err, "push config store failed")
		} else {
			pushRegistered = true
		}
	}
	if created && h.OnSubmit != nil {
		// The hook is the owner notification. It must never block the peer's
		// receipt: the store write is the durable acknowledgement.
		h.OnSubmit(req)
	}
	log := logging.HTTP()
	log.Info().
		Str("request_id", req.ID).
		Bool("created", created).
		Str("principal", principal.String()).
		Msg("knowledge request submitted")
	task := TaskForRequest(req)
	if sendPush != nil {
		// The request is durable either way; report whether the webhook stuck,
		// so a peer whose registration failed knows to poll or re-register.
		if kr, ok := task.Metadata["knowledgeRequest"].(map[string]any); ok {
			kr["push_registered"] = pushRegistered
		}
	}
	return &task, nil, nil
}

// TaskForRequest renders a stored request as an A2A Task. The same shape is
// returned on submission, by `tasks/get`, and as the push-notification body,
// so a peer can consume whichever path it has.
func TaskForRequest(req requests.Request) Task {
	task := Task{
		Kind:      "task",
		ID:        req.ID,
		ContextID: req.ContextID,
		Status: TaskStatus{
			State:     TaskStateFor(req.Status),
			Timestamp: req.UpdatedAt,
		},
		Metadata: map[string]any{
			"knowledgeRequest": map[string]any{
				"id":           req.ID,
				"status":       req.Status,
				"origin":       req.Origin,
				"submitted_at": req.SubmittedAt,
			},
		},
	}
	if req.Status == requests.StatusResolved {
		answerText := req.Answer
		task.Artifacts = []Artifact{{
			ArtifactID:  "answer",
			Name:        "Grounded answer",
			Description: "The answer from Eli's published writing, with the evidence the loop attached.",
			Parts:       []Part{{Kind: "text", Text: answerText}},
			Metadata: map[string]any{
				"citations":  req.Citations,
				"sources":    req.Sources,
				"source_url": req.SourceURL,
				"refused":    false,
			},
		}}
		task.Status.Message = &Message{
			Kind:      "message",
			Role:      "agent",
			MessageID: "answer-" + req.ID,
			ContextID: req.ContextID,
			Parts:     []Part{{Kind: "text", Text: answerText}},
		}
	}
	if req.Status == requests.StatusDeclined {
		task.Status.Message = &Message{
			Kind:      "message",
			Role:      "agent",
			MessageID: "declined-" + req.ID,
			ContextID: req.ContextID,
			Parts:     []Part{{Kind: "text", Text: "This question was declined; there is no published answer."}},
		}
	}
	return task
}

// TaskStateFor maps the workflow status onto an A2A task state.
func TaskStateFor(status string) string {
	switch status {
	case requests.StatusNew:
		return TaskStateSubmitted
	case requests.StatusReviewed:
		return TaskStateWorking
	case requests.StatusResolved:
		return TaskStateCompleted
	case requests.StatusDeclined:
		return TaskStateRejected
	case requests.StatusCanceled:
		return TaskStateCanceled
	default:
		return TaskStateSubmitted
	}
}

// getTask answers `tasks/get` from the durable store: the one task type this
// service can still describe after the request that created it is gone.
func (h *Handler) getTask(r *http.Request, raw json.RawMessage) (any, *Error) {
	if h.Requests == nil {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	id, rpcErr := taskIDFrom(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	req, err := h.Requests.Get(id)
	if errors.Is(err, requests.ErrNotFound) {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	if err != nil {
		logRequestError(err, "knowledge request read failed")
		return nil, &Error{Code: CodeInternalError, Message: "task unavailable"}
	}
	if rpcErr := authorizeTask(r, req); rpcErr != nil {
		return nil, rpcErr
	}
	task := TaskForRequest(req)
	return &task, nil
}

// cancelTask lets the submitting peer withdraw an open request.
func (h *Handler) cancelTask(r *http.Request, raw json.RawMessage) (any, *Error) {
	if h.Requests == nil {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	id, rpcErr := taskIDFrom(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	existing, err := h.Requests.Get(id)
	if errors.Is(err, requests.ErrNotFound) {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	if err != nil {
		logRequestError(err, "knowledge request read failed")
		return nil, &Error{Code: CodeInternalError, Message: "task unavailable"}
	}
	if rpcErr := authorizeTask(r, existing); rpcErr != nil {
		return nil, rpcErr
	}
	req, err := h.Requests.Cancel(id, "", h.now)
	switch {
	case errors.Is(err, requests.ErrNotFound):
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	case errors.Is(err, requests.ErrInvalidTransition):
		return nil, &Error{Code: CodeTaskNotCancelable, Message: "Task is not cancelable"}
	case err != nil:
		logRequestError(err, "knowledge request cancel failed")
		return nil, &Error{Code: CodeInternalError, Message: "task unavailable"}
	}
	task := TaskForRequest(req)
	return &task, nil
}

// setPushConfig registers a peer webhook for one knowledge request. Push
// notifications are opt-in at the deployment level; a server that cannot
// deliver says so with -32003 instead of accepting a registration it will drop.
func (h *Handler) setPushConfig(r *http.Request, raw json.RawMessage) (any, *Error) {
	if h.Requests == nil || h.Push == nil {
		return nil, &Error{Code: CodePushNotSupported, Message: "push notifications are not supported"}
	}
	var params SetTaskPushNotificationConfigParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidParam("params must be a pushNotificationConfig object")
	}
	taskID := strings.TrimSpace(params.TaskID)
	if taskID == "" {
		return nil, invalidParam("params.taskId is required")
	}
	req, rpcErr := h.lookupTask(taskID)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := authorizeTask(r, req); rpcErr != nil {
		return nil, rpcErr
	}
	// A closed task is never delivered again, so accepting a registration for
	// it would leave the peer waiting on a webhook that cannot fire.
	if !req.Open() {
		return nil, invalidParam("push notifications cannot be registered for a closed task")
	}
	target := strings.TrimSpace(params.PushNotificationConfig.URL)
	if _, err := h.Push.ValidateURL(target); err != nil {
		// The URL is peer input and may carry a credential in its query
		// string, so the client gets the rule, not the value.
		return nil, invalidParam("params.pushNotificationConfig.url must be an https URL to a public host, without userinfo or a fragment")
	}
	token := strings.TrimSpace(params.PushNotificationConfig.Token)
	if len(token) > maxPushTokenBytes {
		return nil, invalidParam(fmt.Sprintf("params.pushNotificationConfig.token must be at most %d bytes", maxPushTokenBytes))
	}
	updated, err := h.Requests.SetPushConfig(req.ID, target, token)
	switch {
	case errors.Is(err, requests.ErrInvalidTransition):
		return nil, invalidParam("push notifications cannot be registered for a closed task")
	case errors.Is(err, requests.ErrNotFound):
		// The owner deleted the request between the lookup and this write.
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	case err != nil:
		logRequestError(err, "push config store failed")
		return nil, &Error{Code: CodeInternalError, Message: "push config unavailable"}
	}
	return &TaskPushNotificationConfig{
		TaskID: updated.ID,
		PushNotificationConfig: PushNotificationConfig{
			URL:            updated.PushURL,
			Token:          updated.PushToken,
			Authentication: params.PushNotificationConfig.Authentication,
		},
	}, nil
}

// getPushConfig returns the registration for one task.
func (h *Handler) getPushConfig(r *http.Request, raw json.RawMessage) (any, *Error) {
	if h.Requests == nil || h.Push == nil {
		return nil, &Error{Code: CodePushNotSupported, Message: "push notifications are not supported"}
	}
	id, rpcErr := taskIDFrom(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	req, rpcErr := h.lookupTask(id)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := authorizeTask(r, req); rpcErr != nil {
		return nil, rpcErr
	}
	target, token, err := h.Requests.PushTarget(id)
	if errors.Is(err, requests.ErrNotFound) {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	if errors.Is(err, requests.ErrNoPushConfig) {
		return nil, invalidParam("no push notification config is registered for this task")
	}
	if err != nil {
		logRequestError(err, "push config read failed")
		return nil, &Error{Code: CodeInternalError, Message: "push config unavailable"}
	}
	return &TaskPushNotificationConfig{
		TaskID:                 id,
		PushNotificationConfig: PushNotificationConfig{URL: target, Token: token},
	}, nil
}

// listPushConfigs returns the task's single registration as a one-element
// list, which is the spec's list shape.
func (h *Handler) listPushConfigs(r *http.Request, raw json.RawMessage) (any, *Error) {
	if h.Requests == nil || h.Push == nil {
		return nil, &Error{Code: CodePushNotSupported, Message: "push notifications are not supported"}
	}
	// Validate params first: malformed input must stay -32602 and must not be
	// flattened into a successful empty list.
	id, rpcErr := taskIDFrom(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	req, rpcErr := h.lookupTask(id)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := authorizeTask(r, req); rpcErr != nil {
		return nil, rpcErr
	}
	target, token, err := h.Requests.PushTarget(id)
	if errors.Is(err, requests.ErrNoPushConfig) {
		return []TaskPushNotificationConfig{}, nil
	}
	if errors.Is(err, requests.ErrNotFound) {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	if err != nil {
		logRequestError(err, "push config read failed")
		return nil, &Error{Code: CodeInternalError, Message: "push config unavailable"}
	}
	return []TaskPushNotificationConfig{{
		TaskID:                 id,
		PushNotificationConfig: PushNotificationConfig{URL: target, Token: token},
	}}, nil
}

// deletePushConfig removes the registration; the peer can still poll.
func (h *Handler) deletePushConfig(r *http.Request, raw json.RawMessage) (any, *Error) {
	if h.Requests == nil || h.Push == nil {
		return nil, &Error{Code: CodePushNotSupported, Message: "push notifications are not supported"}
	}
	id, rpcErr := taskIDFrom(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	req, rpcErr := h.lookupTask(id)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := authorizeTask(r, req); rpcErr != nil {
		return nil, rpcErr
	}
	err := h.Requests.ClearPushConfig(id)
	if errors.Is(err, requests.ErrNotFound) {
		return nil, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	if err != nil {
		logRequestError(err, "push config delete failed")
		return nil, &Error{Code: CodeInternalError, Message: "push config unavailable"}
	}
	return nil, nil
}

// authorizeTask binds a task to the identity a call asserts. Task ids are
// 128-bit handles, so possession is the only proof a token-less peer has; but
// when a call *does* assert an identity (a bearer token, or X-Agent-Id), that
// identity must own the task, or one peer could cancel another's request or
// read its push token. A mismatch is reported as TaskNotFound so the reply
// does not confirm that the id exists. A token is the operator's credential and
// is always allowed.
func authorizeTask(r *http.Request, req requests.Request) *Error {
	if tok, ok := auth.TokenFrom(r.Context()); ok && tok.ID != "" {
		return nil
	}
	id := strings.TrimSpace(r.Header.Get("X-Agent-Id"))
	if id == "" {
		// Nothing asserted: the unguessable id is the capability.
		return nil
	}
	if !budget.ValidID(id) || req.Principal != (budget.Principal{Kind: budget.KindAgent, ID: id}).String() {
		return &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	return nil
}

// lookupTask resolves a task id for the task methods.
func (h *Handler) lookupTask(id string) (requests.Request, *Error) {
	req, err := h.Requests.Get(id)
	if errors.Is(err, requests.ErrNotFound) {
		return requests.Request{}, &Error{Code: CodeTaskNotFound, Message: "Task not found"}
	}
	if err != nil {
		logRequestError(err, "knowledge request read failed")
		return requests.Request{}, &Error{Code: CodeInternalError, Message: "task unavailable"}
	}
	return req, nil
}

// taskIDFrom reads the task id from params, accepting the spec's `id` and the
// `taskId` spelling some clients send.
func taskIDFrom(raw json.RawMessage) (string, *Error) {
	var params TaskIDParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return "", invalidParam("params must be an object with an id")
		}
	}
	id := strings.TrimSpace(firstNonEmpty(params.ID, params.TaskID))
	if id == "" {
		// A missing id is a malformed request on every task method; only a
		// well-formed id that matches nothing is TaskNotFound.
		return "", invalidParam("params.id is required")
	}
	if len(id) > 128 {
		return "", invalidParam("params.id is invalid")
	}
	return id, nil
}

// logRequestError is the shared failure log for the request store. The store
// error never carries question text, and the HTTP surface never returns it.
func logRequestError(err error, message string) {
	log := logging.HTTP()
	log.Error().Err(err).Msg(message)
}

// invalidParam is the one -32602 constructor these paths use.
func invalidParam(message string) *Error {
	return &Error{Code: CodeInvalidParams, Message: message}
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// requestSubmissionHint is what a refusal advertises: the peer can turn its
// unanswered question into a deferred request if the deployment accepts one.
func requestSubmissionHint(push bool) map[string]any {
	hint := map[string]any{
		"available": true,
		"how":       "resend message/send with message.metadata.knowledgeRequest = {} (optionally {reason, context, idempotencyKey})",
		"returns":   "a Task in state \"submitted\"; poll tasks/get with its id",
		"note":      "submitting stores the question for the owner; it is not a promise of an answer",
	}
	if push {
		hint["returns"] = "a Task in state \"submitted\"; poll tasks/get with its id, or register a webhook with tasks/pushNotificationConfig/set"
	}
	return hint
}

// inboxRetryAfter is the Retry-After for a full inbox: the peer should come
// back after the owner has triaged something, which is not a fixed interval.
const inboxRetryAfter = time.Hour
