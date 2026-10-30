package main

// state persists the agent's identity (agent_id, agent_token) and its
// locally-saved Linode PAT (encrypted at rest with the AES-GCM key derived
// from the registration code). The file is written atomically with 0600.
type state struct {
	AgentID          int    `json:"agent_id"`
	AgentToken       string `json:"agent_token"`
	RegistrationCode string `json:"registration_code,omitempty"` // kept to re-derive the PAT key
	SealedPAT        string `json:"sealed_pat,omitempty"`        // agentcrypto.Seal(key, pat)
}
