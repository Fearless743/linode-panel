// Package agenthub tracks registered agents and their pending commands.
//
// In-memory, mutex-protected implementation of the frozen contract in
// types.go.
package agenthub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNoAgent is returned by Enqueue when the agent ID is unknown.
var ErrNoAgent = errors.New("agent 不存在")

// Hub stores agents, their queued/in-flight/completed commands, and results.
// All fields are guarded by mu.
type Hub struct {
	mu       sync.Mutex
	agents   map[int]*Agent
	byToken  map[string]int // token sha256 hex -> agent ID
	nextID   int
	commands map[int64]*Command
	queued   map[int][]int64 // agent ID -> FIFO of queued command IDs
	nextCmd  int64
	results  []Result
	wake     chan struct{} // closed+replaced on each enqueue to wake long-pollers
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{
		agents:   make(map[int]*Agent),
		byToken:  make(map[string]int),
		commands: make(map[int64]*Command),
		queued:   make(map[int][]int64),
		wake:     make(chan struct{}),
	}
}

// Register creates a new agent, generates a 32-byte random token, and stores
// only its SHA-256 hex hash. The plaintext token is returned exactly once.
func (h *Hub) Register(name string) (Agent, string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failure is unrecoverable in practice; panic rather
		// than silently mint a weak token.
		panic("agenthub: crypto/rand failed: " + err.Error())
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	agent := &Agent{
		ID:        h.nextID,
		Name:      name,
		TokenHash: hex.EncodeToString(sum[:]),
	}
	h.agents[agent.ID] = agent
	h.byToken[agent.TokenHash] = agent.ID
	return *agent, token
}

// AgentByToken returns the agent whose token hash matches token, using a
// constant-time comparison over every stored hash.
func (h *Hub) AgentByToken(token string) (Agent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	for _, a := range h.agents {
		if subtle.ConstantTimeCompare([]byte(a.TokenHash), []byte(hash)) == 1 {
			return *a, true
		}
	}
	return Agent{}, false
}

// Agent returns the agent with the given ID.
func (h *Hub) Agent(id int) (Agent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.agents[id]
	if !ok {
		return Agent{}, false
	}
	return *a, true
}

// SetHostname records the agent's reported hostname. Unknown IDs are a no-op.
func (h *Hub) SetHostname(id int, hostname string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.agents[id]; ok {
		a.Hostname = hostname
	}
}

// Rename updates an agent's name. Unknown IDs return false.
func (h *Hub) Rename(id int, name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.agents[id]
	if !ok {
		return false
	}
	a.Name = name
	return true
}

// MarkManual flags an agent as created from the panel UI (rather than
// self-registered by an agent process), so the panel may rename it. Unknown
// IDs return false.
func (h *Hub) MarkManual(id int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.agents[id]
	if !ok {
		return false
	}
	a.Manual = true
	return true
}

// Agents returns all agents ordered by ID ascending.
func (h *Hub) Agents() []Agent {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]int, 0, len(h.agents))
	for id := range h.agents {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]Agent, 0, len(ids))
	for _, id := range ids {
		out = append(out, *h.agents[id])
	}
	return out
}

// Delete removes an agent and all of its queued/in-flight commands. It
// returns false when the agent ID is unknown. Completed results are kept.
func (h *Hub) Delete(id int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	a, ok := h.agents[id]
	if !ok {
		return false
	}
	delete(h.agents, id)
	delete(h.byToken, a.TokenHash)
	for _, cmdID := range h.queued[id] {
		delete(h.commands, cmdID)
	}
	delete(h.queued, id)
	return true
}

// Enqueue queues a command for the given agent. It fills in ID, AgentID,
// Status and CreatedAt, and wakes any blocked NextCommands callers.
func (h *Hub) Enqueue(agentID int, cmd Command) (Command, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.agents[agentID]; !ok {
		return Command{}, ErrNoAgent
	}
	h.nextCmd++
	cmd.ID = h.nextCmd
	cmd.AgentID = agentID
	cmd.Status = StatusQueued
	cmd.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	h.commands[cmd.ID] = &cmd
	h.queued[agentID] = append(h.queued[agentID], cmd.ID)
	h.broadcastLocked()
	return cmd, nil
}

// NextCommands returns all queued commands for the agent (FIFO by ID),
// marking them in_flight, and updates LastSeen. If none are queued it blocks
// up to wait for an Enqueue to wake it; on timeout or wait <= 0 it returns an
// empty (non-nil) slice.
func (h *Hub) NextCommands(agentID int, wait time.Duration) []Command {
	h.mu.Lock()
	defer h.mu.Unlock()

	agent, ok := h.agents[agentID]
	if !ok {
		return []Command{}
	}
	agent.LastSeen = time.Now().UTC().Format(time.RFC3339)

	deadline := time.Now().Add(wait)
	for {
		if cmds := h.dequeueLocked(agentID); len(cmds) > 0 {
			return cmds
		}
		if wait <= 0 {
			return []Command{}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return []Command{}
		}
		wake := h.wake
		h.mu.Unlock()
		timer := time.NewTimer(remaining)
		select {
		case <-wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
		h.mu.Lock()
	}
}

// NextCommandsContext is NextCommands with an additional context: when ctx is
// done (client disconnect/cancel) it stops waiting and returns immediately
// with whatever commands are queued (possibly empty).
func (h *Hub) NextCommandsContext(ctx context.Context, agentID int, wait time.Duration) []Command {
	if ctx == nil {
		return h.NextCommands(agentID, wait)
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	agent, ok := h.agents[agentID]
	if !ok {
		return []Command{}
	}
	agent.LastSeen = time.Now().UTC().Format(time.RFC3339)

	deadline := time.Now().Add(wait)
	for {
		if cmds := h.dequeueLocked(agentID); len(cmds) > 0 {
			return cmds
		}
		if wait <= 0 {
			return []Command{}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return []Command{}
		}
		wake := h.wake
		h.mu.Unlock()
		timer := time.NewTimer(remaining)
		select {
		case <-wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			h.mu.Lock()
			return []Command{}
		}
		h.mu.Lock()
	}
}

// dequeueLocked takes all queued command IDs for the agent in FIFO order,
// marking each in_flight. Caller must hold mu.
func (h *Hub) dequeueLocked(agentID int) []Command {
	ids := h.queued[agentID]
	if len(ids) == 0 {
		return []Command{}
	}
	h.queued[agentID] = nil
	cmds := make([]Command, 0, len(ids))
	for _, id := range ids {
		c := h.commands[id]
		c.Status = StatusInFlight
		cmds = append(cmds, *c)
	}
	return cmds
}

// CompleteCommand marks the command completed and records its result. It
// returns false for an unknown command ID.
func (h *Hub) CompleteCommand(id int64, res Result) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, ok := h.commands[id]
	if !ok {
		return false
	}
	c.Status = StatusCompleted
	res.CommandID = id
	res.AgentID = c.AgentID
	res.At = time.Now().UTC().Format(time.RFC3339)
	h.results = append(h.results, res)
	return true
}

// Results returns all completed results, newest first.
func (h *Hub) Results() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]Result, len(h.results))
	for i, r := range h.results {
		out[len(h.results)-1-i] = r
	}
	return out
}

// PendingCommands returns queued and in-flight commands, oldest first.
func (h *Hub) PendingCommands() []Command {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]Command, 0)
	for _, c := range h.commands {
		if c.Status == StatusQueued || c.Status == StatusInFlight {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Command returns a copy of the command with the given ID.
func (h *Hub) Command(id int64) (Command, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.commands[id]
	if !ok {
		return Command{}, false
	}
	return *c, true
}

// PendingFor returns the number of queued and in-flight commands for agentID.
func (h *Hub) PendingFor(agentID int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.commands {
		if c.AgentID == agentID && (c.Status == StatusQueued || c.Status == StatusInFlight) {
			n++
		}
	}
	return n
}

// broadcastLocked wakes every blocked NextCommands caller. Caller must hold
// mu. A closed channel plus a fresh replacement guarantees no lost wakeups.
func (h *Hub) broadcastLocked() {
	close(h.wake)
	h.wake = make(chan struct{})
}
