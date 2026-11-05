package a2a

import (
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
)

// SkillID is the stable identifier of the agent's one skill.
const SkillID = "grounded-writing-qa"

// SkillDescription is the card's skill copy: it must describe what the loop
// actually does, including the refusal string and the citation contract.
const SkillDescription = "Answers questions about Eli Lap's published technical knowledge, engineering experience, experiments, and opinions using only indexed Ghost posts and Postiz cross-posts. Every claim carries a verifiable citation (chunk_id and source_url); when the corpus does not contain the answer the reply is exactly \"" + agentloop.Refusal + "\"."

// CardURLPath is the endpoint the card's url points at.
const CardURLPath = "/a2a"

// CardPaths are the well-known locations the card is served from. The A2A
// v0.3 spec recommends /.well-known/agent-card.json; agent.json is the v0.2
// path and is served as an alias for older clients.
var CardPaths = []string{
	"/.well-known/agent-card.json",
	"/.well-known/agent.json",
}

// NewCard builds the agent card for one request. baseURL is the service root
// the card should advertise (no trailing slash, e.g. https://agent.example),
// and secured says whether bearer tokens are required.
func NewCard(baseURL string, secured bool) AgentCard {
	url := strings.TrimRight(baseURL, "/") + CardURLPath
	card := AgentCard{
		ProtocolVersion:    ProtocolVersion,
		Name:               AgentName,
		Description:        agentloop.Description,
		URL:                url,
		PreferredTransport: "JSONRPC",
		AdditionalInterfaces: []AgentInterface{
			{URL: url, Transport: "JSONRPC"},
		},
		Provider: &AgentProvider{
			Organization: "elilap.dev",
			URL:          "https://elilap.dev",
		},
		Version: Version,
		Capabilities: AgentCapabilities{
			// The loop answers synchronously and this service keeps no task
			// store, so there is nothing to stream or push.
			Streaming:              false,
			PushNotifications:      false,
			StateTransitionHistory: false,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []AgentSkill{{
			ID:          SkillID,
			Name:        "Grounded Q&A over Eli Lap's published writing",
			Description: SkillDescription,
			Tags:        []string{"writing", "retrieval", "grounded-qa", "citations"},
			Examples: []string{
				"What is the harness take?",
				"What has Eli written about vendor lock-in?",
				"Which posts mention context windows and agent memory?",
			},
			InputModes:  []string{"text/plain"},
			OutputModes: []string{"text/plain"},
		}},
	}
	if secured {
		card.SecuritySchemes = map[string]any{
			"bearer": HTTPAuthScheme{
				Type:         "http",
				Scheme:       "bearer",
				BearerFormat: "opaque",
				Description:  "Read-only API token issued by the agent operator. Send it as an Authorization: Bearer <token> header.",
			},
		}
		card.Security = []map[string][]string{{"bearer": {}}}
	}
	return card
}
