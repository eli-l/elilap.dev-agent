package agentloop

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGroundedPromptNamesAllBylines guards the alias mapping the corpus needs:
// Ghost chunks are authored by "Eli Lap", Postiz chunks by "Ilja Lapkovsky".
// Without this instruction the model treats the cross-posts as someone else's
// writing and refuses to use them.
func TestGroundedPromptNamesAllBylines(t *testing.T) {
	for _, want := range []string{"Eli Lap", "Ilja Lapkovsky", "Ilja Lapkovskis", "internationalized pen name"} {
		if !strings.Contains(groundedPrompt, want) {
			t.Errorf("groundedPrompt does not mention %q", want)
		}
	}
	if !strings.Contains(groundedPrompt, "never treat a name difference as evidence") {
		t.Error("groundedPrompt does not rule out the byline mismatch")
	}
}

// TestAgentDescription pins the canonical description used in the README and
// the planned A2A agent card.
func TestAgentDescription(t *testing.T) {
	for _, want := range []string{
		"Eli Lap's published technical knowledge", "engineering experience",
		"experiments", "opinions", "published sources", "verifiable citations",
	} {
		if !strings.Contains(Description, want) {
			t.Errorf("Description does not mention %q", want)
		}
	}
}

func TestGroundedPromptPersona(t *testing.T) {
	for _, want := range []string{
		"You are an agent representing",
		"published technical knowledge",
		"verifiable citations",
	} {
		if !strings.Contains(groundedPrompt, want) {
			t.Errorf("groundedPrompt does not mention %q", want)
		}
	}
}

func TestSearchToolDescriptionNamesAliases(t *testing.T) {
	tool := SearchTool()
	for _, want := range []string{"Ilja Lapkovsky", "Ilja Lapkovskis"} {
		if !strings.Contains(tool.Function.Description, want) {
			t.Errorf("tool description does not mention %q", want)
		}
	}
	if !strings.Contains(tool.Function.Description, "keywords") {
		t.Error("tool description does not ask for keywords")
	}
}

// TestGroundedPromptAsksForKeys guards the retrieval half of answer quality: the
// tool query must be extracted keys, not a paraphrase of the question. A small
// chat model defaults to restating the question, which drags the embedding and
// the FTS5 OR-list toward the question's own vocabulary instead of the corpus's.
func TestGroundedPromptAsksForKeys(t *testing.T) {
	for _, want := range []string{
		"KEYS, NOT A SENTENCE",
		"Do not restate or paraphrase the question",
		"Drop the question's scaffolding",
		"exactly as they are written",
		"Never retry with the whole question",
		"only when the question itself names that restriction",
	} {
		if !strings.Contains(groundedPrompt, want) {
			t.Errorf("groundedPrompt does not mention %q", want)
		}
	}
}

// TestSearchToolQueryDescriptionAsksForKeywords keeps the schema description in
// step with the prompt: the parameter description is what the model reads most
// often while filling the call in.
func TestSearchToolQueryDescriptionAsksForKeywords(t *testing.T) {
	tool := SearchTool()
	var schema struct {
		Properties struct {
			Query struct {
				Description string `json:"description"`
			} `json:"query"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil {
		t.Fatalf("parameters: %v", err)
	}
	desc := schema.Properties.Query.Description
	for _, want := range []string{"keywords", "verbatim", "Not the question restated"} {
		if !strings.Contains(desc, want) {
			t.Errorf("query description does not mention %q: %q", want, desc)
		}
	}
}

// TestSearchFirstInstructionAsksForKeys covers the refusal nudge, the one other
// place the loop tells the model how to search.
func TestSearchFirstInstructionAsksForKeys(t *testing.T) {
	if !strings.Contains(searchFirstInstruction, "key terms") {
		t.Errorf("searchFirstInstruction = %q, want key terms", searchFirstInstruction)
	}
}
