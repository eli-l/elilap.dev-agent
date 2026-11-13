package agentloop_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

// urlOf is the source URL the hit helper attaches to a chunk id.
var urlOf = func(id string) string { return "https://read.elilap.dev/" + id[:8] }

// hexID matches a sha256 chunk id, which must never appear in a response.
var hexID = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

// TestEvidenceDescribesExcerptsAndDocuments pins the response's two reference
// lists: one Citation per excerpt the model was shown (in ranking order), one
// Source per distinct document. Both are built from the loop's own retrieval
// record, so neither can be fabricated by the model.
func TestEvidenceDescribesExcerptsAndDocuments(t *testing.T) {
	postiz := retrieval.Hit{
		ChunkID: idC, SourceKind: "postiz_post", SourceID: "postiz-1",
		SourceURL: "https://www.linkedin.com/posts/x", Platform: "linkedin",
		Author: "Ilja Lapkovsky", Title: "Cross-post", Section: "Reply",
		PublishedAt: "2026-11-01T10:00:00Z", Body: "a cross-posted excerpt",
	}
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("A plain summary with no citations in it."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "the ghost excerpt"), postiz}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused || ans.Answer != "A plain summary with no citations in it." {
		t.Fatalf("answer = %+v, want the model's text verbatim", ans)
	}

	if len(ans.Citations) != 2 {
		t.Fatalf("citations = %+v, want one per excerpt", ans.Citations)
	}
	first := ans.Citations[0]
	if first.SourceKind != "ghost_post" || first.SourceURL != urlOf(idA) ||
		first.Title != "A title" || first.PublishedAt != "2026-11-02T13:21:35Z" ||
		first.Excerpt != "the ghost excerpt" {
		t.Errorf("first citation = %+v, want the first retrieved excerpt", first)
	}
	if second := ans.Citations[1]; second.SourceKind != "postiz_post" || second.Section != "Reply" {
		t.Errorf("second citation = %+v, want the postiz excerpt", second)
	}

	if len(ans.Sources) != 2 {
		t.Fatalf("sources = %+v, want one per document", ans.Sources)
	}
	if src := ans.Sources[0]; src.Kind != "ghost_post" || src.URL != urlOf(idA) ||
		src.Author != "Eli" || src.Title != "A title" {
		t.Errorf("first source = %+v", src)
	}
	if src := ans.Sources[1]; src.Platform != "linkedin" || src.Author != "Ilja Lapkovsky" ||
		src.PublishedAt != "2026-11-01T10:00:00Z" {
		t.Errorf("second source = %+v, want the cross-post metadata", src)
	}
}

// TestResponseNeverExposesChunkIDs is the hard requirement on the wire shape:
// the internal identifier is logged for tracing, but no answer surface may
// carry it. Marshalling the whole Answer covers every field a surface emits.
func TestResponseNeverExposesChunkIDs(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"harness"}`)),
		answerResponse("Summary."),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	payload, err := json.Marshal(ans)
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	encoded := string(payload)
	if strings.Contains(encoded, "chunk_id") {
		t.Errorf("response carries a chunk_id field: %s", encoded)
	}
	if id := hexID.FindString(encoded); id != "" {
		t.Errorf("response carries a chunk id %s: %s", id, encoded)
	}
	// The reference lists are still there, just without the identifier.
	for _, want := range []string{`"citations":[`, `"sources":[`} {
		if !strings.Contains(encoded, want) {
			t.Errorf("response is missing %s: %s", want, encoded)
		}
	}
}

// TestRefusalExposesNoEvidence keeps the refusal shape stable for clients: a
// refused answer is the refusal string with two empty arrays, not nulls.
func TestRefusalExposesNoEvidence(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		toolCallResponse(call("call_1", agentloop.ToolName, `{"query":"off corpus"}`)),
		answerResponse(agentloop.Refusal),
	}}
	searcher := &fakeSearcher{hits: []retrieval.Hit{hit(idA, "excerpt")}}
	ans, err := newRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	payload, err := json.Marshal(ans)
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	encoded := string(payload)
	for _, want := range []string{`"citations":[]`, `"sources":[]`, `"refused":true`} {
		if !strings.Contains(encoded, want) {
			t.Errorf("refusal response is missing %s: %s", want, encoded)
		}
	}
}
