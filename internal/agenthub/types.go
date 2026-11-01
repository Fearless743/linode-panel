// Package agenthub tracks registered agents and their pending commands.
//
// Frozen contract (internal/agenthub):
//
//	func NewHub() *Hub
//	func (h *Hub) Register(name string) (agent Agent, token string) // token = hex 32B
//	func (h *Hub) AgentByToken(token string) (Agent, bool)
//	func (h *Hub) Agent(id int) (Agent, bool)
//	func (h *Hub) Rename(id int, name string) bool
//	func (h *Hub) Agents() []Agent
//	func (h *Hub) Enqueue(agentID int, cmd Command) (Command, error) // ErrNoAgent
//	func (h *Hub) NextCommands(ctx context.Context, agentID int, wait time.Duration) []Command // long-poll
//	func (h *Hub) CompleteCommand(id int64, res Result) bool
//	func (h *Hub) Results() []Result            // all completed results, newest first
//	func (h *Hub) PendingCommands() []Command   // queued + in-flight, oldest first
package agenthub

// Command types (frozen).
const (
	CmdList   = "list"
	CmdPower  = "power"
	CmdReboot = "rebuild"
	CmdDelete = "delete"
)

// Power actions.
const (
	ActionBoot     = "boot"
	ActionShutdown = "shutdown"
	ActionReboot   = "reboot"
)

// Command statuses.
const (
	StatusQueued    = "queued"
	StatusInFlight  = "in_flight"
	StatusCompleted = "completed"
)

// Command is one work item for an agent.
type Command struct {
	ID        int64          `json:"id"`
	AgentID   int            `json:"agent_id"`
	Type      string         `json:"type"` // CmdList | CmdPower | CmdRebuild | CmdDelete
	Params    map[string]any `json:"params"`
	Status    string         `json:"status"`
	CreatedAt string         `json:"created_at"`
}

// Result is an agent's report for one command.
type Result struct {
	CommandID int64          `json:"command_id"`
	AgentID   int            `json:"agent_id"`
	OK        bool           `json:"ok"`
	Error     string         `json:"error,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	At        string         `json:"at"`
}

// Agent is a registered runner.
type Agent struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"-"` // sha256 hex; token itself is shown once at register
	Hostname  string `json:"hostname,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}
