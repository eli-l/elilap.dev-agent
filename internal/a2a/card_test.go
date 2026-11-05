package a2a_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
)

// TestCardIsA2A030 pins the required AgentCard members from the v0.3 schema
// and the transport declaration rules: the main url and preferredTransport
// must be present, and additionalInterfaces must repeat that pair.
func TestCardIsA2A030(t *testing.T) {
	card := a2a.NewCard("https://agent.example", false)
	if card.ProtocolVersion != "0.3.0" {
		t.Errorf("protocolVersion = %q, want 0.3.0", card.ProtocolVersion)
	}
	if card.Name == "" || card.Version == "" || card.Description == "" {
		t.Errorf("identity fields incomplete: %+v", card)
	}
	if card.URL != "https://agent.example/a2a" {
		t.Errorf("url = %q", card.URL)
	}
	if card.PreferredTransport != "JSONRPC" {
		t.Errorf("preferredTransport = %q", card.PreferredTransport)
	}
	found := false
	for _, iface := range card.AdditionalInterfaces {
		if iface.URL == card.URL && iface.Transport == "JSONRPC" {
			found = true
		}
	}
	if !found {
		t.Errorf("additionalInterfaces %+v does not repeat the main URL/transport", card.AdditionalInterfaces)
	}
	if card.Description != agentloop.Description {
		t.Errorf("description = %q, want the canonical agentloop.Description", card.Description)
	}
	if card.Capabilities.Streaming || card.Capabilities.PushNotifications {
		t.Errorf("capabilities = %+v, want both false", card.Capabilities)
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != a2a.SkillID {
		t.Fatalf("skills = %+v, want the one grounded-qa skill", card.Skills)
	}
	if !strings.Contains(card.Skills[0].Description, agentloop.Refusal) {
		t.Errorf("skill description does not name the refusal string: %q", card.Skills[0].Description)
	}
	if len(card.DefaultInputModes) == 0 || len(card.DefaultOutputModes) == 0 {
		t.Errorf("input/output modes missing: %+v", card)
	}
	if card.SecuritySchemes != nil || card.Security != nil {
		t.Errorf("token-less card advertises security: %+v", card.Security)
	}
}

func TestCardAdvertisesBearerWhenSecured(t *testing.T) {
	card := a2a.NewCard("http://127.0.0.1:8080", true)
	if card.SecuritySchemes == nil {
		t.Fatal("secured card has no securitySchemes")
	}
	b, err := json.Marshal(card.SecuritySchemes["bearer"])
	if err != nil {
		t.Fatalf("marshal scheme: %v", err)
	}
	for _, want := range []string{`"type":"http"`, `"scheme":"bearer"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("scheme JSON %s does not contain %s", b, want)
		}
	}
	if len(card.Security) != 1 {
		t.Fatalf("security = %+v, want one requirement", card.Security)
	}
	if _, ok := card.Security[0]["bearer"]; !ok {
		t.Errorf("security requirement = %+v, want bearer", card.Security[0])
	}
}

func TestCardPathsCoverBothWellKnownNames(t *testing.T) {
	want := []string{"/.well-known/agent-card.json", "/.well-known/agent.json"}
	if len(a2a.CardPaths) != len(want) {
		t.Fatalf("CardPaths = %v", a2a.CardPaths)
	}
	for i, path := range want {
		if a2a.CardPaths[i] != path {
			t.Errorf("CardPaths[%d] = %q, want %q", i, a2a.CardPaths[i], path)
		}
	}
}
