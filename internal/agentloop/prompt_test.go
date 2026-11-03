package agentloop

import (
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
}
