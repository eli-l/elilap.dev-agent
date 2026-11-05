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

// mapSearcher answers per query text, so a test can give each fan-out query its
// own ranked list.
type mapSearcher struct {
	byQuery map[string][]retrieval.Hit
	err     error
	queries []string
}

func (s *mapSearcher) Search(_ context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	s.queries = append(s.queries, q.Text)
	if s.err != nil {
		return nil, s.err
	}
	return s.byQuery[q.Text], nil
}

// labelledResponse is the rewrite reply in the shape the prompt asks for: three
// named keys, each stating the job it has to do.
func labelledResponse(exact, vocabulary, narrower string) llm.ChatResponse {
	payload, _ := json.Marshal(struct {
		ExactTerms       string `json:"exact_terms"`
		CorpusVocabulary string `json:"corpus_vocabulary"`
		Narrower         string `json:"narrower"`
	}{exact, vocabulary, narrower})
	return answerResponse(string(payload))
}

// legacyResponse is the older array shape; the parser still accepts it.
func legacyResponse(variants ...string) llm.ChatResponse {
	payload, _ := json.Marshal(struct {
		Variants []string `json:"variants"`
	}{Variants: variants})
	return answerResponse(string(payload))
}

func newFanoutRunner(chatter *scriptedChatter, searcher agentloop.Searcher) *agentloop.Runner {
	return &agentloop.Runner{
		Searcher: searcher, Chat: chatter, Model: "chat-model",
		Mode: agentloop.ModeFanout,
		Now:  func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) },
	}
}

func TestParseSearchMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want agentloop.SearchMode
		bad  bool
	}{
		{in: "", want: agentloop.ModeLoop},
		{in: "loop", want: agentloop.ModeLoop},
		{in: " FANOUT ", want: agentloop.ModeFanout},
		{in: "bogus", bad: true},
	} {
		got, err := agentloop.ParseSearchMode(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseSearchMode(%q) accepted an unknown mode", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseSearchMode(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestFanoutSearchesQuestionAndVariants(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		labelledResponse("sqlite fts5 build tag", "full text search sqlite tag", "fts5 cgo sqlite"),
		answerResponse("FTS5 needs the tag (" + idA + ")."),
	}}
	searcher := &mapSearcher{byQuery: map[string][]retrieval.Hit{
		"what about fts5?":            {hit(idB, "fts5 behind a build tag")},
		"sqlite fts5 build tag":       {hit(idA, "sqlite_fts5 build tag")},
		"full text search sqlite tag": {hit(idA, "sqlite_fts5 build tag")},
		"fts5 cgo sqlite":             {hit(idA, "sqlite_fts5 build tag")},
	}}

	ans, err := newFanoutRunner(chatter, searcher).Run(context.Background(), "what about fts5?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused || len(ans.Citations) != 1 || ans.Citations[0].ChunkID != idA {
		t.Fatalf("answer = %+v, want a grounded citation to idA", ans)
	}
	if ans.ToolIterations != 0 {
		t.Errorf("tool iterations = %d, want 0 on the fan-out path", ans.ToolIterations)
	}
	if len(searcher.queries) != 4 {
		t.Fatalf("searched %v, want the question plus all three labelled variants", searcher.queries)
	}
	if searcher.queries[0] != "what about fts5?" {
		t.Errorf("first query = %q, want the original question", searcher.queries[0])
	}

	// Two chat calls only: one rewrite at the cheap effort, one answer at the
	// deliberating effort. Neither offers tools.
	if len(chatter.requests) != 2 {
		t.Fatalf("chat calls = %d, want 2", len(chatter.requests))
	}
	if got := chatter.requests[0].ReasoningEffort; got != agentloop.ReasoningTransform {
		t.Errorf("rewrite effort = %q, want %q", got, agentloop.ReasoningTransform)
	}
	if got := chatter.requests[1].ReasoningEffort; got != agentloop.ReasoningAnswer {
		t.Errorf("answer effort = %q, want %q", got, agentloop.ReasoningAnswer)
	}
	for i, req := range chatter.requests {
		if len(req.Tools) != 0 {
			t.Errorf("request %d offers tools on the fan-out path", i+1)
		}
	}
	// The answer turn carries the merged excerpts inline, best ranked first.
	answerTurn := chatter.requests[1].Messages[len(chatter.requests[1].Messages)-1].Content
	if !strings.Contains(answerTurn, "[Excerpts]") || !strings.Contains(answerTurn, idA) {
		t.Errorf("answer turn lacks the excerpt array: %q", answerTurn)
	}
	if strings.Index(answerTurn, idA) > strings.Index(answerTurn, idB) {
		t.Error("excerpts are not ranked: idA is the consensus hit and must come first")
	}
}

func TestFanoutFallsBackToTheQuestionAlone(t *testing.T) {
	// A reply that is not JSON at all: the run must degrade to one query, not
	// fail, and must not spend a second rewrite call.
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		answerResponse("I could not produce variants, sorry."),
		answerResponse("Grounded (" + idA + ")."),
	}}
	searcher := &mapSearcher{byQuery: map[string][]retrieval.Hit{
		"q": {hit(idA, "excerpt")},
	}}
	ans, err := newFanoutRunner(chatter, searcher).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans.Refused {
		t.Errorf("refused despite a retrieved hit: %+v", ans)
	}
	if len(searcher.queries) != 1 || searcher.queries[0] != "q" {
		t.Errorf("searched %v, want just the question", searcher.queries)
	}
}

func TestFanoutRefusesWithoutRetrieval(t *testing.T) {
	chatter := &scriptedChatter{responses: []llm.ChatResponse{
		labelledResponse("one", "two", "three"),
	}}
	ans, err := newFanoutRunner(chatter, &mapSearcher{}).Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ans.Refused || ans.Answer != agentloop.Refusal {
		t.Errorf("answer = %+v, want the refusal", ans)
	}
	// Only the rewrite ran: there is nothing to ground an answer on, so paying
	// for an answer call would be waste.
	if len(chatter.requests) != 1 {
		t.Errorf("chat calls = %d, want 1 (rewrite only)", len(chatter.requests))
	}
}

func TestFanoutPropagatesBackendFailures(t *testing.T) {
	t.Run("rewrite", func(t *testing.T) {
		chatter := &scriptedChatter{err: errors.New("model down")}
		if _, err := newFanoutRunner(chatter, &mapSearcher{}).Run(context.Background(), "q"); err == nil ||
			!strings.Contains(err.Error(), "rewrite") {
			t.Fatalf("err = %v, want a rewrite failure", err)
		}
	})

	t.Run("search", func(t *testing.T) {
		chatter := &scriptedChatter{responses: []llm.ChatResponse{labelledResponse("a", "b", "c")}}
		searcher := &mapSearcher{err: errors.New("index gone")}
		if _, err := newFanoutRunner(chatter, searcher).Run(context.Background(), "q"); err == nil ||
			!strings.Contains(err.Error(), "index gone") {
			t.Fatalf("err = %v, want the search failure", err)
		}
	})
}
