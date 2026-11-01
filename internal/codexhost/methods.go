package codexhost

// MinCodexVersion is the oldest Codex CLI Orqen supports (reference-project ADR 0009).
const MinCodexVersion = "0.159.2"

// ProtocolVersion is the Codex release whose schema is pinned in testdata.
const ProtocolVersion = "0.160.0"

// Methods Orqen sends.
const (
	MInitialize        = "initialize"
	MInitialized       = "initialized" // client notification
	MThreadStart       = "thread/start"
	MThreadResume      = "thread/resume"
	MTurnStart         = "turn/start"
	MTurnInterrupt     = "turn/interrupt"
	MAccountRead       = "account/read"
	MModelList         = "model/list"
	MMcpServerStatuses = "mcpServerStatus/list"
)

// Server notifications Orqen handles.
const (
	NTurnStarted      = "turn/started"
	NTurnCompleted    = "turn/completed"
	NItemStarted      = "item/started"
	NItemCompleted    = "item/completed"
	NThreadTokenUsage = "thread/tokenUsage/updated"
	NError            = "error"
)

// Server requests Orqen answers.
const (
	RCommandApproval = "item/commandExecution/requestApproval"
	RFileApproval    = "item/fileChange/requestApproval"
	RPermissions     = "item/permissions/requestApproval"
	RUserInput       = "item/tool/requestUserInput"
	RElicitation     = "mcpServer/elicitation/request"
	RDynamicTool     = "item/tool/call"
	RLegacyExec      = "execCommandApproval"
	RLegacyPatch     = "applyPatchApproval"
)

// ClientRequests, ClientNotifications, ServerNotifications and ServerRequests list every name above by direction;
// the schema test asserts each exists in the pinned schema.
var (
	ClientRequests       = []string{MInitialize, MThreadStart, MThreadResume, MTurnStart, MTurnInterrupt, MAccountRead, MModelList, MMcpServerStatuses}
	ClientNotifications  = []string{MInitialized}
	ServerNotifications  = []string{NTurnStarted, NTurnCompleted, NItemStarted, NItemCompleted, NThreadTokenUsage, NError}
	ServerRequestMethods = []string{RCommandApproval, RFileApproval, RPermissions, RUserInput, RElicitation, RDynamicTool, RLegacyExec, RLegacyPatch}
)
