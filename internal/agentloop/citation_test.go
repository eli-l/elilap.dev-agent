package agentloop_test

import (
	"context"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

// urlOf is the source URL the hit helper attaches to a chunk id.
var urlOf = func(id string) string { return "https://read.elilap.dev/" + id[:8] }

func TestRunAcceptsSourceURLCitation(t *testing.T) {
	// The small chat model often copies the source_url but drops the 64-hex
	// chunk id; the URL is the same published source, so it must ground.
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Harnesses are vendor lock-in (" + urlOf(idA) + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "harnesses are vendor lock-in")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused {
		t.Fatalf("refused a URL-only citation: %+v", ans)
	}
	if len(ans.Citations) != 1 || ans.Citations[0].ChunkID != idA ||
		ans.Citations[0].SourceURL != urlOf(idA) {
		t.Errorf("citations = %+v, want idA resolved from its URL", ans.Citations)
	}
}

func TestRunResolvesSourceURLToBestRankedChunk(t *testing.T) {
	// Two chunks of the same post share a URL; the citation must resolve to the
	// better-ranked chunk, deterministically, and only once.
	second := hit(idB, "another excerpt")
	second.SourceURL = urlOf(idA)
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Grounded (" + urlOf(idA) + ")."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt"), second}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(ans.Citations) != 1 || ans.Citations[0].ChunkID != idA {
		t.Errorf("citations = %+v, want the best-ranked chunk idA only", ans.Citations)
	}
}

func TestRunRefusesUnretrievedSourceURL(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Made up (https://read.elilap.dev/never-retrieved/)."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ans.Refused || ans.Answer != agentloop.Refusal {
		t.Errorf("answer = %+v, want refusal for a URL the loop never retrieved", ans)
	}
}

func TestRunDoesNotTreatExcerptBodyLinkAsCitation(t *testing.T) {
	// An excerpt can link elsewhere; quoting that link is source material, not a
	// citation of a chunk the loop retrieved.
	body := "see https://example.com/elsewhere for the linked post"
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Grounded (https://example.com/elsewhere)."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, body)}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ans.Refused {
		t.Errorf("answer = %+v, want refusal: a body link is not a citation", ans)
	}
}

func TestRunAcceptsMarkdownLinkedSourceURL(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Grounded ([the post](" + urlOf(idA) + "))."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused || len(ans.Citations) != 1 || ans.Citations[0].ChunkID != idA {
		t.Errorf("answer = %+v, want a citation parsed out of the markdown link", ans)
	}
}
