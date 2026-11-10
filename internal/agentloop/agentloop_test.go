package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

// chunk ids must be 64 lowercase hex characters to look like real citations.
var (
	idA = strings.Repeat("a", 64)
	idB = strings.Repeat("b", 64)
	idC = strings.Repeat("c", 64)
)

func hit(id, body string) retrieval.Hit {
	return retrieval.Hit{
		ChunkID: id, SourceKind: "ghost_post", SourceURL: "https://read.elilap.dev/" + id[:8],
		Title: "A title", Body: body, PublishedAt: "2026-11-02T13:21:35Z",
	}
}

func toolCallResponse(calls ...llm.ToolCall) llm.ChatResponse {
	return llm.ChatResponse{Choices: []struct {
		Message      llm.ChatMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	}{{Message: llm.ChatMessage{Role: "assistant", ToolCalls: calls}}}}
}

func answerResponse(content string) llm.ChatResponse {
	return llm.ChatResponse{Choices: []struct {
		Message      llm.ChatMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	}{{Message: llm.ChatMessage{Role: "assistant", Content: content}, FinishReason: "stop"}}}
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.ToolFunction{Name: name, Arguments: args}}
}

type scriptedChatter struct {
	responses []llm.ChatResponse
	err       error
	requests  []llm.ChatRequest
	next      int
}

func (c *scriptedChatter) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return llm.ChatResponse{}, c.err
	}
	if c.next >= len(c.responses) {
		return llm.ChatResponse{}, errors.New("unexpected chat call")
	}
	resp := c.responses[c.next]
	c.next++
	return resp, nil
}

type fakeSearcher struct {
	hits    []retrieval.Hit
	err     error
	queries []retrieval.Query
}

func (s *fakeSearcher) Search(_ context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	s.queries = append(s.queries, q)
	return s.hits, s.err
}

func newRunner(chatter *scriptedChatter, searcher *fakeSearcher) *agentloop.Runner {
	return &agentloop.Runner{
		Searcher: searcher, Chat: chatter, Model: "chat-model",
		Now: func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) },
	}
}

func TestRunCallsToolThenAnswersWithCitations(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness lock-in","top_n":2}`)),
		answerResponse("Harness lock-in is the theme (" + idA + ", https://read.elilap.dev/aaaaaaaa)."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "harnesses are vendor lock-in"), hit(idB, "another excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "What is the harness take?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused {
		t.Fatalf("refused: %+v", ans)
	}
	if ans.ToolIterations != 1 {
		t.Errorf("tool iterations = %d, want 1", ans.ToolIterations)
	}
	if len(ans.Citations) != 1 || ans.Citations[0].ChunkID != idA {
		t.Errorf("citations = %+v, want idA", ans.Citations)
	}
	if ans.Citations[0].SourceURL == "" {
		t.Error("citation URL was not resolved from the retrieved hit")
	}
	if !strings.Contains(ans.Answer, "Harness lock-in") {
		t.Errorf("answer = %q", ans.Answer)
	}
	if len(searcher.queries) != 1 || searcher.queries[0].Text != "harness lock-in" || searcher.queries[0].TopN != 2 {
		t.Errorf("searcher queries = %+v", searcher.queries)
	}
	// The second request must echo the assistant tool call and the tool result.
	second := chatter.requests[1]
	if len(second.Messages) != 4 {
		t.Fatalf("second request has %d messages, want system/user/assistant/tool", len(second.Messages))
	}
	toolMsg := second.Messages[3]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" {
		t.Errorf("tool message = %+v", toolMsg)
	}
	if !strings.Contains(toolMsg.Content, idA) || !strings.Contains(toolMsg.Content, "vendor lock-in") {
		t.Errorf("tool content = %q, want the retrieved hits", toolMsg.Content)
	}
	if len(chatter.requests[0].Tools) != 1 || chatter.requests[0].Tools[0].Function.Name != agentloop.ToolName {
		t.Errorf("first request tools = %+v", chatter.requests[0].Tools)
	}
	// The identity mapping is repeated in the user turn, not only the system
	// prompt; without it the model treats Ilja-authored cross-posts as
	// someone else's writing.
	firstUser := chatter.requests[0].Messages[1].Content
	if !strings.Contains(firstUser, "[Identity fact]") || !strings.Contains(firstUser, "Ilja Lapkovsky") {
		t.Errorf("first user message = %q, want the identity fact", firstUser)
	}
}

func TestRunPinsReasoningEffortPerCallKind(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("c1", agentloop.ToolName, `{"query":"one"}`)),
		toolCallResponse(call("c2", agentloop.ToolName, `{"query":"two"}`)),
		answerResponse("Grounded (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	if _, err := newRunner(chatter, searcher).Run(context.Background(), "q"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Nothing retrieved yet: the call is still turning the question into search
	// keys, so reasoning is off. Once excerpts are in hand the same tool-offering
	// call is composing the answer, so it deliberates.
	if got := chatter.requests[0].ReasoningEffort; got != agentloop.ReasoningTransform {
		t.Errorf("transform effort = %q, want %q", got, agentloop.ReasoningTransform)
	}
	if got := chatter.requests[1].ReasoningEffort; got != agentloop.ReasoningAnswer {
		t.Errorf("composing effort = %q, want %q", got, agentloop.ReasoningAnswer)
	}
	for i, req := range chatter.requests {
		if req.ReasoningEffort == "" {
			t.Errorf("request %d left reasoning_effort empty; the backend default is unbounded", i+1)
		}
	}
}

func TestRunForcedFinalAnswerUsesAnswerEffort(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("c1", agentloop.ToolName, `{"query":"one"}`)),
		toolCallResponse(call("c2", agentloop.ToolName, `{"query":"two"}`)),
		answerResponse("Grounded (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	runner := newRunner(chatter, searcher)
	runner.MaxIterations = 2
	if _, err := runner.Run(context.Background(), "q"); err != nil {
		t.Fatalf("run: %v", err)
	}
	final := chatter.requests[len(chatter.requests)-1]
	if len(final.Tools) != 0 {
		t.Fatalf("final request still offers tools: %+v", final.Tools)
	}
	if final.ReasoningEffort != agentloop.ReasoningAnswer {
		t.Errorf("final effort = %q, want %q", final.ReasoningEffort, agentloop.ReasoningAnswer)
	}
}

func TestRunRefusesWithoutCitation(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Harnesses are a form of vendor lock-in, but I forgot to cite."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ans.Refused || ans.Answer != agentloop.Refusal {
		t.Errorf("answer = %+v, want the refusal", ans)
	}
	if len(ans.Citations) != 0 {
		t.Errorf("citations = %+v, want none", ans.Citations)
	}
}

func TestRunRefusesHallucinatedCitation(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Made up (" + idC + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ans.Refused || ans.Answer != agentloop.Refusal {
		t.Errorf("answer = %+v, want the refusal for an unresolvable citation", ans)
	}
}

func TestRunDeduplicatesCitations(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("First " + idA + " then again " + idA + "."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(ans.Citations) != 1 {
		t.Errorf("citations = %+v, want one after deduplication", ans.Citations)
	}
}

func TestRunStopsAtIterationBudget(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("c1", agentloop.ToolName, `{"query":"one"}`)),
		toolCallResponse(call("c2", agentloop.ToolName, `{"query":"two"}`)),
		toolCallResponse(call("c3", agentloop.ToolName, `{"query":"three"}`)),
		answerResponse("Grounded (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	runner := newRunner(chatter, searcher)
	runner.MaxIterations = 3
	ans, err := runner.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.ToolIterations != 3 {
		t.Errorf("tool iterations = %d, want 3", ans.ToolIterations)
	}
	if ans.Refused {
		t.Errorf("answer was refused despite a grounded final call: %+v", ans)
	}
	if len(chatter.requests) != 4 {
		t.Fatalf("chat calls = %d, want 3 tool rounds + 1 final answer", len(chatter.requests))
	}
	final := chatter.requests[3]
	if len(final.Tools) != 0 {
		t.Errorf("final request still offers tools: %+v", final.Tools)
	}
	if !strings.Contains(final.Messages[len(final.Messages)-1].Content, "Answer the question now") {
		t.Error("final request lacks the forcing instruction")
	}
}

func TestRunTrimsExcerptsToBudget(t *testing.T) {
	// Ten words, five-token budget: the tool result keeps five words and an
	// ellipsis, and the citation still resolves.
	body := "one two three four five six seven eight nine ten"
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Grounded (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, body)}}
	runner := newRunner(chatter, searcher)
	runner.ExcerptTokens = 5
	ans, err := runner.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused {
		t.Fatalf("refused: %+v", ans)
	}
	toolContent := chatter.requests[1].Messages[3].Content
	if !strings.Contains(toolContent, "…") {
		t.Errorf("tool content was not truncated: %q", toolContent)
	}
	if strings.Contains(toolContent, "six") {
		t.Errorf("tool content kept words past the budget: %q", toolContent)
	}
	if !strings.Contains(toolContent, idA) {
		t.Error("truncation lost the chunk id the answer cites")
	}
}

func TestRunReportsToolArgumentErrorsToModel(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{bad json`)),
		answerResponse(agentloop.Refusal),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ans.Refused {
		t.Errorf("answer = %+v, want refusal", ans)
	}
	toolContent := chatter.requests[1].Messages[3].Content
	if !strings.Contains(toolContent, `"error"`) || !strings.Contains(toolContent, "JSON object") {
		t.Errorf("tool content = %q, want a JSON error the model can read", toolContent)
	}
	if len(searcher.queries) != 0 {
		t.Error("the searcher ran despite invalid arguments")
	}
}

func TestRunUnknownToolIsReported(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", "web_search", `{"query":"q"}`)),
		answerResponse(agentloop.Refusal),
	}}
	searcher := &fakeSearcher{}
	if _, err := newRunner(chatter, searcher).Run(context.Background(), "q"); err != nil {
		t.Fatalf("run: %v", err)
	}
	toolContent := chatter.requests[1].Messages[3].Content
	if !strings.Contains(toolContent, "unknown tool") {
		t.Errorf("tool content = %q, want an unknown-tool error", toolContent)
	}
}

func TestRunRefusalStringIsHonoredAfterSearch(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"off corpus"}`)),
		answerResponse(agentloop.Refusal),
	}}
	ans, err := newRunner(chatter, &fakeSearcher{}).Run(context.Background(), "off-corpus question")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ans.Refused || ans.Answer != agentloop.Refusal || ans.ToolIterations != 1 {
		t.Errorf("answer = %+v", ans)
	}
}

// TestRunNudgesImmediateRefusal proves the loop does not accept a refusal
// before the model has searched even once.
func TestRunNudgesImmediateRefusal(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		answerResponse(agentloop.Refusal),
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"identity"}`)),
		answerResponse("Grounded (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "who is this?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused || ans.ToolIterations != 1 {
		t.Errorf("answer = %+v, want a grounded answer after the nudge", ans)
	}
	if len(chatter.requests) != 3 {
		t.Fatalf("chat calls = %d, want refusal, nudged tool call, answer", len(chatter.requests))
	}
	nudge := chatter.requests[1].Messages[len(chatter.requests[1].Messages)-1]
	if nudge.Role != "user" || !strings.Contains(nudge.Content, "Before refusing") {
		t.Errorf("nudge message = %+v", nudge)
	}
	if len(searcher.queries) != 1 {
		t.Errorf("searcher calls = %d, want 1", len(searcher.queries))
	}
}

func TestRunPropagatesBackendFailure(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"q"}`)),
	}}
	searcher := &fakeSearcher{err: errors.New("database is locked")}
	_, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("err = %v, want the backend failure", err)
	}
}

func TestSearchToolSchema(t *testing.T) {
	tool := agentloop.SearchTool()
	if tool.Type != "function" || tool.Function.Name != agentloop.ToolName {
		t.Fatalf("tool = %+v", tool)
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil {
		t.Fatalf("parameters: %v", err)
	}
	if schema.Type != "object" || len(schema.Properties) != 7 || len(schema.Required) != 1 || schema.Required[0] != "query" {
		t.Errorf("schema = %+v", schema)
	}
	// Every retrieval source the CLI accepts must be reachable through the
	// tool, or the loop silently cannot filter by that corpus.
	var source struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(schema.Properties["source"], &source); err != nil {
		t.Fatalf("source property: %v", err)
	}
	for _, want := range []string{"all", "ghost", "postiz", "web"} {
		found := false
		for _, got := range source.Enum {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("source enum = %v, want it to contain %q", source.Enum, want)
		}
	}
}

// TestRunAcceptsHashQuotedFromExcerpt proves a 64-hex digest that appears
// inside retrieved source text is not mistaken for a fabricated citation.
func TestRunAcceptsHashQuotedFromExcerpt(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"digest"}`)),
		answerResponse("The build digest is " + idC + "; see the source (" + idA + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{
		hit(idA, "release notes mention digest "+idC+" in passing"),
	}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "what is the digest?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused {
		t.Fatalf("refused on a hash quoted from the excerpt: %+v", ans)
	}
	if len(ans.Citations) != 1 || ans.Citations[0].ChunkID != idA {
		t.Errorf("citations = %+v, want only idA", ans.Citations)
	}
}

// TestRunRejectsOversizedQuestion pins the prompt guard both HTTP surfaces
// rely on: the question is handed to the model before any excerpt budget, so
// an unbounded peer message must never reach the loop.
func TestRunRejectsOversizedQuestion(t *testing.T) {
	_, err := newRunner(&scriptedChatter{}, &fakeSearcher{}).
		Run(context.Background(), strings.Repeat("x", agentloop.MaxQuestionBytes+1))
	if !errors.Is(err, agentloop.ErrQuestionTooLong) {
		t.Fatalf("error = %v, want ErrQuestionTooLong", err)
	}
}
