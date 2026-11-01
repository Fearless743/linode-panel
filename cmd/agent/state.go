package main

// state persists the agent's identity (agent_id, agent_token). The Linode PAT
// is never stored: it is delivered by the panel with each operation command
// and only lives in memory for the duration of that command. The file is
// written atomically with 0600.
type state struct {
	AgentID    int    `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}
