package a2a

import (
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/requests"
)

// SkillID is the stable identifier of the agent's one skill.
const SkillID = "grounded-writing-qa"

// SkillDescription is the card's skill copy: it must describe what the loop
// actually does, including the refusal string and the citation contract.
const SkillDescription = "Answers questions about Eli Lap's published technical knowledge, engineering experience, experiments, and opinions using only the indexed corpus: Ghost posts, Postiz cross-posts, and the pages of Eli's website. Every claim carries a verifiable citation (chunk_id and source_url); when the corpus does not contain the answer the reply is exactly \"" + agentloop.Refusal + "\"."

// CardURLPath is the endpoint the card's url points at.
const CardURLPath = "/a2a"

// KnowledgeRequestExtension is the URI of the extension that advertises the
// deferred-question workflow: submit a refused question, get a task id, and
// receive the answer when the writing exists. It is a v0.3 capability
// extension, so a peer that does not understand it can ignore it.
const KnowledgeRequestExtension = "https://elilap.dev/a2a/extensions/knowledge-request/v1"

// CardOptions toggle the capabilities this deployment can actually honour.
// The card never advertises what the server will not do: push notifications
// stay false until the operator enables them, and the knowledge-request
// extension appears only when a durable store is open.
type CardOptions struct {
	// PushNotifications is true when tasks/pushNotificationConfig/set is
	// served and the resolve path can deliver.
	PushNotifications bool
	// KnowledgeRequests is true when a submission can be persisted.
	KnowledgeRequests bool
}

// CardPaths are the well-known locations the card is served from. The A2A
// v0.3 spec recommends /.well-known/agent-card.json; agent.json is the v0.2
// path and is served as an alias for older clients.
var CardPaths = []string{
	"/.well-known/agent-card.json",
	"/.well-known/agent.json",
}

// NewCard builds the agent card with no optional capabilities.
func NewCard(baseURL string, secured bool) AgentCard {
	return NewCardWithOptions(baseURL, secured, CardOptions{})
}

// NewCardWithOptions builds the card for one deployment. baseURL is the service
// root the card should advertise (no trailing slash, e.g.
// https://agent.example), and secured says whether bearer tokens are required.
func NewCardWithOptions(baseURL string, secured bool, opts CardOptions) AgentCard {
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
			// The loop answers synchronously; only an explicitly submitted
			// knowledge request is persisted and reaches a task state.
			Streaming:              false,
			PushNotifications:      opts.PushNotifications,
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
	if opts.KnowledgeRequests {
		// The text may only promise what this deployment serves: push delivery
		// is a separate toggle, and advertising it while the method answers
		// -32003 would make the card contradict its own capabilities block.
		description := "Submit a question the corpus did not answer as a deferred request. " +
			"The task is `submitted`, becomes `working` when the operator triages it, and " +
			"ends `completed` with the grounded answer, `rejected` (declined), or " +
			"`canceled` (withdrawn); `tasks/get` reports the current state."
		params := map[string]any{
			"submit":            "message/send with message.metadata.knowledgeRequest = {reason?, context?, idempotencyKey?}",
			"poll":              "tasks/get with params.id = the returned task id",
			"max_reason_bytes":  requests.MaxReasonBytes,
			"max_context_bytes": requests.MaxContextBytes,
		}
		if opts.PushNotifications {
			description += " A registered push notification is delivered when the answer lands."
			params["push"] = "tasks/pushNotificationConfig/set with params.taskId and params.pushNotificationConfig.url"
		}
		card.Capabilities.Extensions = []AgentExtension{{
			URI:         KnowledgeRequestExtension,
			Description: description,
			Params:      params,
		}}
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
