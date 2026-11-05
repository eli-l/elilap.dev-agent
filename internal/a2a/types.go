// Package a2a implements the A2A v0.3 JSON-RPC binding over the in-service
// grounded loop: the agent card, `message/send`, and the task methods this
// stateless service can honestly answer.
//
// The wire types mirror the v0.3 specification
// (https://github.com/a2aproject/A2A/blob/v0.3.0/specification/json/a2a.json).
// Only the subset this service produces or accepts is modelled: unknown
// fields are ignored on input, and `metadata` carries the extension data.
package a2a

import "encoding/json"

// Protocol and service identity versions.
const (
	// ProtocolVersion is the A2A protocol revision this server speaks. It is
	// advertised in the agent card and never inferred from a request.
	ProtocolVersion = "0.3.0"
	// Version is this agent implementation's version, advertised in the card.
	Version = "0.1.0"
	// AgentName is the human-readable agent name in the card.
	AgentName = "Eli Lap's published writing"
)

// JSON-RPC method names (A2A v0.3).
const (
	MethodSendMessage      = "message/send"
	MethodStreamMessage    = "message/stream"
	MethodGetTask          = "tasks/get"
	MethodCancelTask       = "tasks/cancel"
	MethodResubscribe      = "tasks/resubscribe"
	MethodPushConfigSet    = "tasks/pushNotificationConfig/set"
	MethodPushConfigGet    = "tasks/pushNotificationConfig/get"
	MethodPushConfigList   = "tasks/pushNotificationConfig/list"
	MethodPushConfigDelete = "tasks/pushNotificationConfig/delete"
	MethodExtendedCard     = "agent/getAuthenticatedExtendedCard"
)

// JSON-RPC error codes: the standard set plus the A2A-specific codes and the
// one implementation-defined server error this service returns for transport
// budget problems (documented in docs/a2a.md).
const (
	CodeParseError                = -32700
	CodeInvalidRequest            = -32600
	CodeMethodNotFound            = -32601
	CodeInvalidParams             = -32602
	CodeInternalError             = -32603
	CodeServerError               = -32000 // implementation-defined: timeout / rate limit
	CodeTaskNotFound              = -32001
	CodeTaskNotCancelable         = -32002
	CodePushNotSupported          = -32003
	CodeUnsupported               = -32004
	CodeContentType               = -32005
	CodeInvalidAgentResponse      = -32006
	CodeExtendedCardNotConfigured = -32007
)

// Request is a JSON-RPC 2.0 request. ID is kept raw so any JSON scalar the
// peer chose is echoed back byte-for-byte.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Response is a JSON-RPC 2.0 response: exactly one of Result or Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface so handler code can return protocol
// errors as ordinary values.
func (e *Error) Error() string { return e.Message }

// AgentCard is the A2A v0.3 self-describing manifest served at
// /.well-known/agent-card.json.
type AgentCard struct {
	ProtocolVersion      string                `json:"protocolVersion"`
	Name                 string                `json:"name"`
	Description          string                `json:"description"`
	URL                  string                `json:"url"`
	PreferredTransport   string                `json:"preferredTransport"`
	AdditionalInterfaces []AgentInterface      `json:"additionalInterfaces,omitempty"`
	Provider             *AgentProvider        `json:"provider,omitempty"`
	Version              string                `json:"version"`
	Capabilities         AgentCapabilities     `json:"capabilities"`
	DefaultInputModes    []string              `json:"defaultInputModes"`
	DefaultOutputModes   []string              `json:"defaultOutputModes"`
	Skills               []AgentSkill          `json:"skills"`
	SecuritySchemes      map[string]any        `json:"securitySchemes,omitempty"`
	Security             []map[string][]string `json:"security,omitempty"`
}

// AgentInterface declares one URL/transport pair.
type AgentInterface struct {
	URL       string `json:"url"`
	Transport string `json:"transport"`
}

// AgentProvider identifies the agent's operator.
type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
}

// AgentCapabilities declares the optional A2A features this agent supports.
type AgentCapabilities struct {
	Streaming              bool `json:"streaming"`
	PushNotifications      bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

// AgentSkill describes the one skill this agent exposes.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// HTTPAuthScheme is the OpenAPI-style bearer scheme advertised in the card
// when token auth is configured.
type HTTPAuthScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme"`
	BearerFormat string `json:"bearerFormat,omitempty"`
	Description  string `json:"description,omitempty"`
}

// Message is one A2A message.
type Message struct {
	Kind             string         `json:"kind"`
	Role             string         `json:"role"`
	MessageID        string         `json:"messageId"`
	Parts            []Part         `json:"parts"`
	ContextID        string         `json:"contextId,omitempty"`
	TaskID           string         `json:"taskId,omitempty"`
	ReferenceTaskIDs []string       `json:"referenceTaskIds,omitempty"`
	Extensions       []string       `json:"extensions,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

// Part is one message part. Only text parts are accepted and produced; Data
// and File exist so an unsupported input can be named in the error.
type Part struct {
	Kind     string         `json:"kind"`
	Text     string         `json:"text,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	File     *FileContent   `json:"file,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// FileContent is the FilePart payload.
type FileContent struct {
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Bytes    string `json:"bytes,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// Task is the A2A task object. This service answers with a Message rather
// than a Task, so it is produced only by tests that pin the wire types.
type Task struct {
	Kind      string         `json:"kind"`
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    TaskStatus     `json:"status"`
	History   []Message      `json:"history,omitempty"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// TaskStatus is a task's state plus optional detail.
type TaskStatus struct {
	State     string   `json:"state"`
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
}

// Artifact is one task output.
type Artifact struct {
	ArtifactID  string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Extensions  []string       `json:"extensions,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// MessageSendParams is the `params` object of message/send.
type MessageSendParams struct {
	Message       *Message           `json:"message"`
	Configuration *MessageSendConfig `json:"configuration,omitempty"`
	Metadata      map[string]any     `json:"metadata,omitempty"`
}

// MessageSendConfig is the optional per-request configuration.
type MessageSendConfig struct {
	AcceptedOutputModes    []string       `json:"acceptedOutputModes,omitempty"`
	HistoryLength          *int           `json:"historyLength,omitempty"`
	PushNotificationConfig map[string]any `json:"pushNotificationConfig,omitempty"`
	Blocking               *bool          `json:"blocking,omitempty"`
}
