package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"linode-panel/internal/agentcrypto"
	"linode-panel/internal/agenthub"
	"linode-panel/internal/store"
)

// agentAuth extracts the Bearer token from the request and resolves it to an
// agent. On failure it writes a 401 and returns ok=false.
func (s *Server) agentAuth(w http.ResponseWriter, r *http.Request) (agenthub.Agent, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "agent 未注册"})
		return agenthub.Agent{}, false
	}
	a, ok := s.Hub.AgentByToken(token)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "agent 未注册"})
		return agenthub.Agent{}, false
	}
	return a, true
}

// handleAgentRegister exchanges the registration code for an agent identity.
// Public: no panel session is required; the registration code is the secret.
func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistrationCode string `json:"registration_code"`
		Name             string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	code := s.Store.RegCode()
	if code == "" || subtle.ConstantTimeCompare([]byte(code), []byte(req.RegistrationCode)) != 1 {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "注册码无效"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "agent"
	}
	agent, token := s.Hub.Register(name)
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":    agent.ID,
		"agent_token": token,
	})
}

// handleAgentPoll long-polls for the agent's queued commands (25s).
func (s *Server) handleAgentPoll(w http.ResponseWriter, r *http.Request) {
	agent, ok := s.agentAuth(w, r)
	if !ok {
		return
	}
	var req struct {
		Hostname string `json:"hostname"`
	}
	// Body is optional; a read failure simply leaves hostname empty.
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Hostname != "" {
		s.Hub.SetHostname(agent.ID, req.Hostname)
	}
	// Watch the request context so a dropped client disconnect does not hold
	// the handler (and an httptest.Server) open for the full 25s poll.
	cmds := s.Hub.NextCommandsContext(r.Context(), agent.ID, 25*time.Second)
	writeJSON(w, http.StatusOK, map[string]any{"commands": cmds})
}

// handleAgentResult records a command result from an agent.
func (s *Server) handleAgentResult(w http.ResponseWriter, r *http.Request) {
	agent, ok := s.agentAuth(w, r)
	if !ok {
		return
	}
	var res struct {
		CommandID int64          `json:"command_id"`
		OK        bool           `json:"ok"`
		Error     string         `json:"error"`
		Payload   map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil || res.CommandID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	_ = agent // agent identity already authenticated; CompleteCommand re-derives AgentID
	cmd, exists := s.Hub.Command(res.CommandID)
	if !exists || cmd.AgentID != agent.ID {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "命令不存在"})
		return
	}
	s.Hub.CompleteCommand(res.CommandID, agenthub.Result{
		OK:      res.OK,
		Error:   res.Error,
		Payload: res.Payload,
	})
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleListAgents returns all registered agents with pending command counts.
// The panel's own self-agent is flagged as local=true; agents created from the
// UI are flagged as manual=true (both are renameable by the panel).
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.Hub.Agents()
	resp := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		local := a.ID == s.localAgentID
		resp = append(resp, map[string]any{
			"id":        a.ID,
			"name":      a.Name,
			"hostname":  a.Hostname,
			"last_seen": a.LastSeen,
			"pending":   s.Hub.PendingFor(a.ID),
			"local":     local,
			"manual":    a.Manual && !local,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": resp})
}

// handleCreateAgent registers a new agent and returns its token plus the
// current registration code (so an operator can hand both to the agent). The
// agent is flagged manual so the panel can rename it later.
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "agent"
	}
	agent, token := s.Hub.Register(name)
	s.Hub.MarkManual(agent.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":          agent.ID,
		"agent_token":       token,
		"registration_code": s.Store.RegCode(),
	})
}

// handleDeleteAgent removes an agent by id. The panel's own self-agent cannot
// be deleted.
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的 agent ID"})
		return
	}
	if id == s.localAgentID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "本地面板 agent 不可删除"})
		return
	}
	if !s.Hub.Delete(id) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "agent 不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleRenameAgent renames an agent the panel owns: its own self-agent (new
// name persisted, surviving restarts) or one created from the UI (manual).
// Agents that self-registered with the registration code cannot be renamed —
// their name is chosen by the agent process at registration and reclaimed on
// every poll.
func (s *Server) handleRenameAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的 agent ID"})
		return
	}
	isLocal := id == s.localAgentID
	if !isLocal {
		a, ok := s.Hub.Agent(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "agent 不存在"})
			return
		}
		if !a.Manual {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "只能重命名本地面板 agent 或手动创建的 agent"})
			return
		}
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "名称不能为空"})
		return
	}
	if !s.Hub.Rename(id, name) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "agent 不存在"})
		return
	}
	if isLocal {
		if err := s.Store.SetSelfAgentName(name); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存名称失败"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleAgentCommands returns the latest N commands and their results.
func (s *Server) handleAgentCommands(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	cmds := s.Hub.PendingCommands()
	results := s.Hub.Results()
	if len(cmds) > limit {
		cmds = cmds[len(cmds)-limit:]
	}
	if len(results) > limit {
		results = results[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commands": cmds,
		"results":  results,
	})
}

// handleSetRegCode sets or generates the agent registration code. An empty
// body generates a random 16-byte hex code and returns it.
func (s *Server) handleSetRegCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	// Empty body is allowed: decode only if there is a body.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
			return
		}
	}
	req.Code = strings.TrimSpace(req.Code)
	generated := false
	if req.Code == "" {
		generated = true
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "生成注册码失败"})
			return
		}
		req.Code = hex.EncodeToString(buf)
	}
	if err := s.Store.SetRegCode(req.Code); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存注册码失败"})
		return
	}
	if generated {
		writeJSON(w, http.StatusOK, map[string]any{"registration_code": req.Code})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// sealPATForAgent seals the user's PAT with the key that agentID expects. The
// panel's own self-agent uses its own key, so it works without a registration
// code; remote agents require the registration code.
func (s *Server) sealPATForAgent(user store.User, tokenID string, agentID int) (string, bool) {
	var key []byte
	if s.localAgentID > 0 && agentID == s.localAgentID {
		key = s.localAgentKey
	} else {
		code := s.Store.RegCode()
		if code == "" {
			return "", false
		}
		key = agentcrypto.DeriveKey(code)
	}
	pat, err := s.resolveToken(user, tokenID)
	if err != nil {
		return "", false
	}
	sealed, err := agentcrypto.Seal(key, []byte(pat))
	if err != nil {
		return "", false
	}
	return sealed, true
}

// enqueueAgentCommand queues an agent command for the agent that should
// handle the operation and returns the command ID, or writes a 400 when agent
// mode is unavailable. The agent is chosen in this order:
//
//  1. the explicit ?agent_id=N override (when present and valid),
//  2. the agent bound to the account (token) the operation targets,
//  3. the panel's own agent (本地面板) when enabled, otherwise the first
//     registered agent (backward-compatible default).
//
// The account's PAT is sealed under the target agent's key and attached as
// params["sealed_pat"]. The panel's own agent needs no registration code;
// remote agents do.
func (s *Server) enqueueAgentCommand(w http.ResponseWriter, r *http.Request, typ string, params map[string]any) (int64, bool) {
	user, _ := UserFrom(r.Context())
	agentID := s.agentIDForRequest(user, r)
	if agentID == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "没有已注册的 agent 或未设置注册码"})
		return 0, false
	}
	sealed, ok := s.sealPATForAgent(user, r.URL.Query().Get("token_id"), agentID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "没有已注册的 agent 或未设置注册码"})
		return 0, false
	}
	params["sealed_pat"] = sealed

	cmd, err := s.Hub.Enqueue(agentID, agenthub.Command{Type: typ, Params: params})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "没有已注册的 agent 或未设置注册码"})
		return 0, false
	}
	return cmd.ID, true
}

// agentIDForRequest resolves which agent should run the operation described by
// r, following the precedence documented on enqueueAgentCommand. It returns 0
// when no agent can be found.
func (s *Server) agentIDForRequest(user store.User, r *http.Request) int {
	// 1. Explicit per-request override (?agent_id=N), as selected in the UI.
	if v := r.URL.Query().Get("agent_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if _, ok := s.Hub.Agent(n); ok {
				return n
			}
		}
	}
	// 2. The agent bound to the account the operation targets.
	if id := s.boundAgentID(user, r.URL.Query().Get("token_id")); id > 0 {
		return id
	}
	// 3. The panel's own agent (本地面板) when enabled, otherwise the first
	// registered agent (backward-compatible default).
	return s.defaultAgentID()
}

// defaultAgentID returns the panel's own self-agent when it is enabled,
// otherwise the first registered agent.
func (s *Server) defaultAgentID() int {
	if s.localAgentID > 0 {
		return s.localAgentID
	}
	agents := s.Hub.Agents()
	if len(agents) == 0 {
		return 0
	}
	return agents[0].ID
}

// boundAgentID returns the agent bound to the token identified by tokenID
// (empty = the user's first PAT), or 0 when the token is unbound or the bound
// agent no longer exists.
func (s *Server) boundAgentID(user store.User, tokenID string) int {
	toks := s.Store.TokensOf(user.ID)
	if len(toks) == 0 {
		return 0
	}
	var tok store.Token
	if tokenID == "" {
		tok = toks[0]
	} else {
		id, err := parseTokenID(tokenID)
		if err != nil {
			return 0
		}
		found := false
		for _, t := range toks {
			if t.ID == id {
				tok = t
				found = true
				break
			}
		}
		if !found {
			return 0
		}
	}
	if tok.AgentID <= 0 {
		return 0
	}
	if _, ok := s.Hub.Agent(tok.AgentID); !ok {
		return 0
	}
	return tok.AgentID
}
