package codexhost

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ClientInfo identifies Orqen to the app-server.
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type capabilities struct {
	OptOutNotificationMethods []string `json:"optOutNotificationMethods,omitempty"`
}

type initializeParams struct {
	ClientInfo   ClientInfo    `json:"clientInfo"`
	Capabilities *capabilities `json:"capabilities,omitempty"`
}

// InitializeResult is the server's answer to initialize.
type InitializeResult struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

// ThreadParams are the settings of a new thread.
type ThreadParams struct {
	Cwd              string
	BaseInstructions string
	Model            string
	Effort           string // used for every turn of the thread
	Config           map[string]any
}

// threadStartParams is the wire form of thread/start. Orqen pins approval and sandbox for the orchestrator thread.
type threadStartParams struct {
	ApprovalPolicy   string         `json:"approvalPolicy,omitempty"`
	BaseInstructions string         `json:"baseInstructions,omitempty"`
	Config           map[string]any `json:"config,omitempty"`
	Cwd              string         `json:"cwd,omitempty"`
	Ephemeral        bool           `json:"ephemeral,omitempty"`
	Model            string         `json:"model,omitempty"`
	Sandbox          string         `json:"sandbox,omitempty"`
	ServiceName      string         `json:"serviceName,omitempty"`
}

type threadResumeParams struct {
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	ExcludeTurns   bool   `json:"excludeTurns,omitempty"`
	Model          string `json:"model,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	ThreadID       string `json:"threadId"`
}

type userInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type turnStartParams struct {
	Effort   string      `json:"effort,omitempty"`
	Input    []userInput `json:"input"`
	Model    string      `json:"model,omitempty"`
	ThreadID string      `json:"threadId"`
}

type turnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type accountReadParams struct {
	RefreshToken bool `json:"refreshToken"`
}

type modelListParams struct {
	IncludeHidden bool `json:"includeHidden,omitempty"`
}

type mcpStatusParams struct {
	Detail   string `json:"detail,omitempty"`
	ThreadID string `json:"threadId,omitempty"`
}

// McpResult is the result of an MCP tool call item.
type McpResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent"`
}

// Item is one thread item (the fields Orqen reads).
type Item struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Status    string          `json:"status"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *McpResult      `json:"result"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error"`
	Text    string `json:"text"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// TurnError is why a turn failed.
type TurnError struct {
	Message string `json:"message"`
}

// Turn is one turn of a thread.
type Turn struct {
	ID     string     `json:"id"`
	Status string     `json:"status"`
	Items  []Item     `json:"items"`
	Error  *TurnError `json:"error"`
}

// Thread is a started or resumed thread.
type Thread struct {
	ID         string
	Model      string
	Effort     string
	CLIVersion string
	Turns      []Turn
}

type threadResponse struct {
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	Thread          struct {
		ID         string `json:"id"`
		CLIVersion string `json:"cliVersion"`
		Turns      []Turn `json:"turns"`
	} `json:"thread"`
}

// TokenUsage is a thread's cumulative usage as reported by thread/tokenUsage/updated.
type TokenUsage struct {
	Input, CachedInput, Output, Reasoning, Total int64
}

// ToolCall is the validated catherd call of a directive.
type ToolCall struct {
	ThreadID, TurnID, ItemID, Server, Tool string
	Arguments                              json.RawMessage
	// Result is the structured content, or the JSON parsed from the first text content. Text is set when the
	// text was not JSON.
	Result json.RawMessage
	Text   string
}

// Errors of RunDirective.
var (
	ErrDeviation      = errors.New("codexhost: the host deviated from the directive")
	ErrNoToolCall     = errors.New("codexhost: the turn ended without the directed tool call")
	ErrArgsMismatch   = errors.New("codexhost: the tool call arguments differ from the directive")
	ErrOutcomeUnknown = errors.New("codexhost: the outcome of the directive is unknown")
)

// ToolError is a catherd error returned by a tool call: {code, message, fix}.
type ToolError struct{ Code, Message, Fix string }

func (e *ToolError) Error() string { return fmt.Sprintf("catherd %s: %s", e.Code, e.Message) }

// DeviationError carries what the host did instead.
type DeviationError struct{ Detail string }

func (e *DeviationError) Error() string { return "codexhost: deviation: " + e.Detail }
func (e *DeviationError) Unwrap() error { return ErrDeviation }

// ProbeResult is what Probe reads from the app-server. No thread or turn is created.
type ProbeResult struct {
	UserAgent    string
	CodexHome    string
	LoggedIn     bool
	AccountType  string
	RequiresAuth bool
	Models       []string
	MCPServers   []MCPServer
}

// MCPServer is one configured MCP server and its tools.
type MCPServer struct {
	Name       string
	Tools      []string
	ToolsError string
}
