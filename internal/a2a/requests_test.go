package a2a_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/budget"
	"github.com/eli-l/elilap.dev-agent/internal/notify"
	"github.com/eli-l/elilap.dev-agent/internal/requests"
)

// requestHandler builds a handler with a durable store and a fake loop.
func requestHandler(t *testing.T, loop a2a.Answerer) (*a2a.Handler, *requests.Store) {
	t.Helper()
	store, err := requests.Open(filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("open requests store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := a2a.NewHandler(loop)
	h.Requests = store
	return h, store
}

// submissionBody builds a message/send that explicitly submits a deferred
// request, optionally with extra metadata fields.
func submissionBody(question string, meta string) string {
	if meta != "" {
		meta = "," + meta
	}
	return `{
		"jsonrpc": "2.0",
		"id": 11,
		"method": "message/send",
		"params": {
			"message": {
				"kind": "message",
				"role": "user",
				"messageId": "m-sub",
				"contextId": "ctx-1",
				"parts": [{"kind": "text", "text": ` + strconvQuote(question) + `}],
				"metadata": {"agentId": "peer-1", "knowledgeRequest": {` + strings.TrimPrefix(meta, ",") + `}}
			}
		}
	}`
}

func rpcBody(method, params string) string {
	return `{"jsonrpc":"2.0","id":9,"method":"` + method + `","params":` + params + `}`
}

// TestSubmissionReturnsATask pins the wire shape of a deferred request: the
// peer gets a Task in `submitted`, not an answer, and the store holds the
// question, the reason, and the idempotency key.
func TestSubmissionReturnsATask(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		t.Fatal("a submission must not run the answer loop")
		return agentloop.Answer{}, nil
	}))
	rec := post(t, h, submissionBody("What about event sourcing?",
		`"reason":"architecture comparison","context":"comparing two designs","idempotencyKey":"sub-1"`))
	env := decode(t, rec)
	if env.Error != nil {
		t.Fatalf("error = %+v", env.Error)
	}
	var task a2a.Task
	if err := json.Unmarshal(env.Result, &task); err != nil {
		t.Fatalf("decode task: %v (%s)", err, env.Result)
	}
	if task.Kind != "task" || task.Status.State != a2a.TaskStateSubmitted {
		t.Errorf("task = %+v, want kind=task state=submitted", task)
	}
	if task.ContextID != "ctx-1" {
		t.Errorf("contextId = %q, want the peer's context", task.ContextID)
	}
	if !strings.HasPrefix(task.ID, "req_") {
		t.Errorf("task id = %q, want req_*", task.ID)
	}
	stored, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Question != "What about event sourcing?" || stored.Reason != "architecture comparison" ||
		stored.IdempotencyKey != "sub-1" || stored.Origin != requests.OriginPeerRequest {
		t.Errorf("stored = %+v", stored)
	}
	if stored.Principal != "agent:peer-1" {
		t.Errorf("principal = %q, want agent:peer-1", stored.Principal)
	}

	// A retry with the same key returns the same task and creates no second row.
	rec = post(t, h, submissionBody("What about event sourcing?",
		`"idempotencyKey":"sub-1"`))
	var retried a2a.Task
	if err := json.Unmarshal(decode(t, rec).Result, &retried); err != nil {
		t.Fatalf("decode retried task: %v", err)
	}
	if retried.ID != task.ID {
		t.Errorf("retried id = %q, want %q", retried.ID, task.ID)
	}
	if n, _ := store.Pending(); n != 1 {
		t.Errorf("pending = %d, want 1", n)
	}

	// The same key with a different question is a conflict, not a silent overwrite.
	rec = post(t, h, submissionBody("What about CQRS?", `"idempotencyKey":"sub-1"`))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("conflict error = %+v, want -32602", env.Error)
	}
}

func TestSubmissionRejectedWithoutAStore(t *testing.T) {
	h := a2a.NewHandler(answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{Answer: "x"}, nil
	}))
	rec := post(t, h, submissionBody("q", ""))
	env := decode(t, rec)
	if env.Error == nil || env.Error.Code != a2a.CodeUnsupported {
		t.Fatalf("error = %+v, want -32004", env.Error)
	}
}

// TestRefusalAdvertisesSubmission keeps discovery honest: the hint appears on
// a refusal only when this deployment can actually store a request.
func TestRefusalAdvertisesSubmission(t *testing.T) {
	refusing := answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{Answer: agentloop.Refusal, Refused: true}, nil
	})
	h, _ := requestHandler(t, refusing)
	var msg a2a.Message
	if err := json.Unmarshal(decode(t, post(t, h, sendBody("unanswerable"))).Result, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	hint, ok := msg.Metadata["requestSubmission"].(map[string]any)
	if !ok {
		t.Fatalf("refusal metadata has no requestSubmission hint: %v", msg.Metadata)
	}
	if returns, _ := hint["returns"].(string); strings.Contains(returns, "pushNotificationConfig") {
		t.Errorf("hint advertises push to a deployment that does not serve it: %q", returns)
	}

	plain := a2a.NewHandler(refusing)
	var plainMsg a2a.Message
	if err := json.Unmarshal(decode(t, post(t, plain, sendBody("unanswerable"))).Result, &plainMsg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if _, ok := plainMsg.Metadata["requestSubmission"]; ok {
		t.Errorf("a deployment without a store advertised submissions: %v", plainMsg.Metadata)
	}
}

// TestSubmissionIsBudgeted pins the separate submission gate: a refusal from
// the store's budget is a 429, and nothing is persisted.
func TestSubmissionIsBudgeted(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.Submit = refuseAll{}
	rec := post(t, h, submissionBody("q", ""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if n, _ := store.Pending(); n != 0 {
		t.Errorf("a refused submission was stored (pending=%d)", n)
	}
}

// TestSubmissionInboxCap pins the owner's bound: past MaxPending the service
// refuses new requests instead of growing without limit.
func TestSubmissionInboxCap(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.MaxPending = 1
	if _, _, err := store.Create(requests.CreateInput{Question: "first", Principal: "agent:a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := post(t, h, submissionBody("second", ""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if n, _ := store.Pending(); n != 1 {
		t.Errorf("pending = %d, want 1", n)
	}
}

func TestGetTaskReflectsResolution(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a", ContextID: "ctx-9"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Unknown ids are still TaskNotFound.
	rec := post(t, h, rpcBody(a2a.MethodGetTask, `{"id":"req_missing"}`))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeTaskNotFound {
		t.Fatalf("unknown task error = %+v, want -32001", env.Error)
	}

	if _, err := store.Review(req.ID, "writing it up", nil); err != nil {
		t.Fatalf("Review: %v", err)
	}
	var task a2a.Task
	if err := json.Unmarshal(decode(t, post(t, h, rpcBody(a2a.MethodGetTask, `{"id":"`+req.ID+`"}`))).Result, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.Status.State != a2a.TaskStateWorking {
		t.Errorf("state = %q, want working", task.Status.State)
	}

	if _, err := store.Resolve(req.ID, requests.Resolution{
		Answer:    "Eli wrote about it.",
		Citations: `[{"title":"x"}]`,
		Sources:   `[{"title":"x"}]`,
		SourceURL: "https://read.elilap.dev/x/",
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := json.Unmarshal(decode(t, post(t, h, rpcBody(a2a.MethodGetTask, `{"id":"`+req.ID+`"}`))).Result, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %q, want completed", task.Status.State)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].Parts[0].Text != "Eli wrote about it." {
		t.Errorf("artifacts = %+v", task.Artifacts)
	}
	if task.ContextID != "ctx-9" {
		t.Errorf("contextId = %q, want ctx-9", task.ContextID)
	}
}

func TestCancelTask(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var task a2a.Task
	if err := json.Unmarshal(decode(t, post(t, h, rpcBody(a2a.MethodCancelTask, `{"id":"`+req.ID+`"}`))).Result, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.Status.State != a2a.TaskStateCanceled {
		t.Errorf("state = %q, want canceled", task.Status.State)
	}
	rec := post(t, h, rpcBody(a2a.MethodCancelTask, `{"id":"`+req.ID+`"}`))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeTaskNotCancelable {
		t.Errorf("second cancel error = %+v, want -32002", env.Error)
	}
}

func TestPushConfigRegistration(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	setBody := func(url string) string {
		return rpcBody(a2a.MethodPushConfigSet, `{"taskId":"`+req.ID+`","pushNotificationConfig":{"url":`+strconvQuote(url)+`,"token":"peer-secret"}}`)
	}

	// Not enabled: the card would say false, and the method answers -32003.
	rec := post(t, h, setBody("https://peer.example/hook"))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodePushNotSupported {
		t.Fatalf("disabled push error = %+v, want -32003", env.Error)
	}

	h.Push = notify.New(notify.Policy{})
	// Plain http is refused by the default policy.
	rec = post(t, h, setBody("http://peer.example/hook"))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("http push url error = %+v, want -32602", env.Error)
	}
	rec = post(t, h, setBody("https://127.0.0.1/hook"))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("loopback push url error = %+v, want -32602", env.Error)
	}
	rec = post(t, h, setBody("https://peer.example/hook"))
	if env := decode(t, rec); env.Error != nil {
		t.Fatalf("valid push url error = %+v", env.Error)
	}
	target, token, err := store.PushTarget(req.ID)
	if err != nil {
		t.Fatalf("PushTarget: %v", err)
	}
	if target != "https://peer.example/hook" || token != "peer-secret" {
		t.Errorf("target/token = %q/%q", target, token)
	}

	rec = post(t, h, rpcBody(a2a.MethodPushConfigGet, `{"id":"`+req.ID+`"}`))
	var cfg a2a.TaskPushNotificationConfig
	if err := json.Unmarshal(decode(t, rec).Result, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.PushNotificationConfig.URL != "https://peer.example/hook" {
		t.Errorf("config = %+v", cfg)
	}

	rec = post(t, h, rpcBody(a2a.MethodPushConfigList, `{"id":"`+req.ID+`"}`))
	var list []a2a.TaskPushNotificationConfig
	if err := json.Unmarshal(decode(t, rec).Result, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("list = %+v, want one entry", list)
	}

	rec = post(t, h, rpcBody(a2a.MethodPushConfigDelete, `{"id":"`+req.ID+`"}`))
	if env := decode(t, rec); env.Error != nil {
		t.Errorf("delete returned an error: %+v", env.Error)
	}
	// A successful no-payload method must still carry "result": null; an
	// omitted member is not valid JSON-RPC.
	if !strings.Contains(rec.Body.String(), `"result":null`) {
		t.Errorf("delete response = %s, want an explicit null result", rec.Body.String())
	}
	// A task with no registration is not a missing task.
	rec = post(t, h, rpcBody(a2a.MethodPushConfigGet, `{"id":"`+req.ID+`"}`))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("get without a registration = %+v, want -32602", env.Error)
	}
	if _, _, err := store.PushTarget(req.ID); !errors.Is(err, requests.ErrNoPushConfig) {
		t.Errorf("config survived delete: %v", err)
	}
}

// TestPushConfigOnAClosedTaskIsAClientError keeps a state mistake from looking
// like a server failure.
func TestPushConfigOnAClosedTaskIsAClientError(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.Push = notify.New(notify.Policy{})
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Cancel(req.ID, "", nil); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	rec := post(t, h, rpcBody(a2a.MethodPushConfigSet,
		`{"taskId":"`+req.ID+`","pushNotificationConfig":{"url":"https://peer.example/hook"}}`))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("closed-task registration = %+v, want -32602", env.Error)
	}
}

// TestIdempotentRetryWorksWhenTheInboxIsFull pins the retry path: a peer
// resending a submission it already owns must get its task, not a 429 the
// inbox cap would produce.
func TestIdempotentRetryWorksWhenTheInboxIsFull(t *testing.T) {
	h, _ := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.MaxPending = 1
	first := submissionBody("q", `"idempotencyKey":"retry-1"`)
	var task a2a.Task
	if err := json.Unmarshal(decode(t, post(t, h, first)).Result, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	// The inbox is now at the cap; the same submission is still answered.
	rec := post(t, h, first)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", rec.Code)
	}
	var retried a2a.Task
	if err := json.Unmarshal(decode(t, rec).Result, &retried); err != nil {
		t.Fatalf("decode retried task: %v", err)
	}
	if retried.ID != task.ID {
		t.Errorf("retried id = %q, want %q", retried.ID, task.ID)
	}
}

// TestSendTimePushConfigRegistersForTheTask covers the one-call form: a
// submission may register its webhook in configuration.pushNotificationConfig.
func TestSendTimePushConfigRegistersForTheTask(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.Push = notify.New(notify.Policy{AllowInsecure: true})
	body := `{
		"jsonrpc": "2.0", "id": 3, "method": "message/send",
		"params": {
			"message": {
				"kind": "message", "role": "user", "messageId": "m-p",
				"parts": [{"kind": "text", "text": "q"}],
				"metadata": {"knowledgeRequest": {}}
			},
			"configuration": {"pushNotificationConfig": {"url": "http://127.0.0.1:9/hook", "token": "s"}}
		}
	}`
	var task a2a.Task
	if err := json.Unmarshal(decode(t, post(t, h, body)).Result, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	target, _, err := store.PushTarget(task.ID)
	if err != nil {
		t.Fatalf("PushTarget: %v", err)
	}
	if target != "http://127.0.0.1:9/hook" {
		t.Errorf("target = %q", target)
	}
}

// TestCardAdvertisesOnlyEnabledCapabilities keeps the card honest.
func TestCardAdvertisesOnlyEnabledCapabilities(t *testing.T) {
	plain := a2a.NewCard("https://agent.example", false)
	if plain.Capabilities.PushNotifications || len(plain.Capabilities.Extensions) != 0 {
		t.Errorf("default card advertises capabilities it lacks: %+v", plain.Capabilities)
	}
	full := a2a.NewCardWithOptions("https://agent.example", false, a2a.CardOptions{
		PushNotifications: true,
		KnowledgeRequests: true,
	})
	if !full.Capabilities.PushNotifications {
		t.Error("push capability not advertised")
	}
	if len(full.Capabilities.Extensions) != 1 || full.Capabilities.Extensions[0].URI != a2a.KnowledgeRequestExtension {
		t.Fatalf("extensions = %+v", full.Capabilities.Extensions)
	}
	if _, ok := full.Capabilities.Extensions[0].Params["push"]; !ok {
		t.Error("extension with push enabled does not document the push method")
	}

	// Without push delivery the extension must not mention registering a
	// webhook: the method would answer -32003.
	pollOnly := a2a.NewCardWithOptions("https://agent.example", false, a2a.CardOptions{KnowledgeRequests: true})
	ext := pollOnly.Capabilities.Extensions[0]
	if _, ok := ext.Params["push"]; ok {
		t.Errorf("extension advertises push while pushNotifications=false: %+v", ext.Params)
	}
	if strings.Contains(ext.Description, "push") {
		t.Errorf("extension description promises push: %q", ext.Description)
	}
}

// countingAcquirer records charges, so a retry that writes nothing can be
// proven not to spend a submission credit.
type countingAcquirer struct{ charges int }

func (c *countingAcquirer) Acquire(budget.Caller) (func(), error) {
	c.charges++
	return func() {}, nil
}

// TestIdempotentRetryDoesNotSpendSubmissionBudget pins the second half of the
// retry contract: no new row is written, so no budget gate is charged.
func TestIdempotentRetryDoesNotSpendSubmissionBudget(t *testing.T) {
	h, _ := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	counter := &countingAcquirer{}
	h.Submit = counter
	body := submissionBody("q", `"idempotencyKey":"k1"`)
	post(t, h, body)
	post(t, h, body)
	if counter.charges != 1 {
		t.Errorf("submission budget charged %d time(s) for one request, want 1", counter.charges)
	}
}

// TestPushConfigOnAResolvedTaskIsRefused keeps the peer from waiting on a
// webhook that can no longer fire.
func TestPushConfigOnAResolvedTaskIsRefused(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.Push = notify.New(notify.Policy{})
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Resolve(req.ID, requests.Resolution{Answer: "answered"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	rec := post(t, h, rpcBody(a2a.MethodPushConfigSet,
		`{"taskId":"`+req.ID+`","pushNotificationConfig":{"url":"https://peer.example/hook"}}`))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("resolved-task registration = %+v, want -32602", env.Error)
	}
}

// TestMissingTaskIDIsAParamError keeps a malformed request distinct from an
// unknown task, on every task method.
func TestMissingTaskIDIsAParamError(t *testing.T) {
	h, _ := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.Push = notify.New(notify.Policy{})
	for _, method := range []string{
		a2a.MethodGetTask,
		a2a.MethodCancelTask,
		a2a.MethodPushConfigGet,
		a2a.MethodPushConfigList,
		a2a.MethodPushConfigDelete,
	} {
		rec := post(t, h, rpcBody(method, `{}`))
		env := decode(t, rec)
		if env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
			t.Errorf("%s with no id = %+v, want -32602", method, env.Error)
		}
	}
	// pushNotificationConfig/set names the task with taskId.
	rec := post(t, h, rpcBody(a2a.MethodPushConfigSet, `{"pushNotificationConfig":{"url":"https://peer.example/hook"}}`))
	if env := decode(t, rec); env.Error == nil || env.Error.Code != a2a.CodeInvalidParams {
		t.Errorf("push set with no taskId = %+v, want -32602", env.Error)
	}
}

func TestPushConfigListWithoutARegistrationIsEmpty(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	h.Push = notify.New(notify.Policy{})
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := post(t, h, rpcBody(a2a.MethodPushConfigList, `{"id":"`+req.ID+`"}`))
	if env := decode(t, rec); env.Error != nil {
		t.Fatalf("list without registration = %+v, want an empty array", env.Error)
	}
	var list []a2a.TaskPushNotificationConfig
	if err := json.Unmarshal(decode(t, rec).Result, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list = %+v, want empty", list)
	}
}

// TestTaskIdentityMustOwnTheTask closes the IDOR path for a caller that does
// assert an identity: an X-Agent-Id that does not own the task gets the same
// answer as a nonexistent id.
func TestTaskIdentityMustOwnTheTask(t *testing.T) {
	h, store := requestHandler(t, answererFunc(func(context.Context, string) (agentloop.Answer, error) {
		return agentloop.Answer{}, nil
	}))
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:peer-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	postAs := func(agentID string) envelope {
		t.Helper()
		httpReq := httptest.NewRequest(http.MethodPost, "/a2a",
			strings.NewReader(rpcBody(a2a.MethodGetTask, `{"id":"`+req.ID+`"}`)))
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("X-Agent-Id", agentID)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httpReq)
		return decode(t, rec)
	}
	if env := postAs("peer-2"); env.Error == nil || env.Error.Code != a2a.CodeTaskNotFound {
		t.Errorf("foreign identity = %+v, want -32001", env.Error)
	}
	if env := postAs("peer-1"); env.Error != nil {
		t.Errorf("owning identity = %+v, want the task", env.Error)
	}
}

// refuseAll is an Acquirer that always refuses on the identity gate.
type refuseAll struct{}

func (refuseAll) Acquire(budget.Caller) (func(), error) {
	return nil, &budget.Refusal{Scope: budget.ScopeIdentityDaily, RetryAfter: time.Hour}
}
