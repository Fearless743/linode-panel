package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// userJSON is one user in GET /api/users. The field names are frozen by the
// shared contract; only the PAT count (not the tokens themselves) is exposed.
type userJSON struct {
	ID         int    `json:"id"`
	Username   string `json:"username"`
	GroupID    int    `json:"group_id"`
	GroupName  string `json:"group_name"`
	TokenCount int    `json:"token_count"`
}

// groupJSON is one group in GET /api/groups.
type groupJSON struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Perms     []string `json:"perms"`
	UserCount int      `json:"user_count"`
}

// tokenJSON is one PAT in GET /api/users/{id}/tokens (and the current user's
// accounts in GET /api/account/tokens). Only the masked value is returned;
// the plaintext token never leaves the server. agent_id/agent_name report the
// account's bound agent (0/empty when unbound).
// tokenJSON 用于 GET /api/account/tokens（及管理员的 /api/users/{id}/tokens）。
// health_* 字段由服务端对每个账号发起一次 GET /account 探测填充：
//   - health_status：ok | no_balance | banned | error，探测失败时留空；
//   - health_detail：可读中文说明（含余额数字）；
//   - health_checked_at：本次探测时间（RFC3339）。
// balance / balance_uninvoiced 仅探测成功时返回（float64，正数=欠款）。
type tokenJSON struct {
	ID               int     `json:"id"`
	Label            string  `json:"label"`
	Masked           string  `json:"masked"`
	AgentID          int     `json:"agent_id"`
	AgentName        string  `json:"agent_name,omitempty"`
	HealthStatus     string  `json:"health_status,omitempty"`
	HealthDetail     string  `json:"health_detail,omitempty"`
	HealthCheckedAt  string  `json:"health_checked_at,omitempty"`
	Balance          float64 `json:"balance,omitempty"`
	BalanceUninvoiced float64 `json:"balance_uninvoiced,omitempty"`
}

// handleListUsers returns all users with their group name and PAT count.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users := s.Store.Users()
	groups := s.Store.Groups()
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		gn := ""
		for _, g := range groups {
			if g.ID == u.GroupID {
				gn = g.Name
				break
			}
		}
		out = append(out, userJSON{
			ID:         u.ID,
			Username:   u.Username,
			GroupID:    u.GroupID,
			GroupName:  gn,
			TokenCount: len(u.Tokens),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// handleCreateUser creates a user. Password must be at least 6 chars.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		GroupID  int    `json:"group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "用户名不能为空"})
		return
	}
	if len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "密码长度至少 6 位"})
		return
	}
	if _, err := s.Store.CreateUser(req.Username, req.Password, req.GroupID); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleUpdateUser changes a user's group and/or password. Empty fields are
// left unchanged.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		GroupID  *int    `json:"group_id"`
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	groupChanged := req.GroupID != nil
	groupID := -1
	if groupChanged {
		groupID = *req.GroupID
		if groupID <= 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的权限组"})
			return
		}
	}
	pass := ""
	if req.Password != nil {
		pass = *req.Password
		if pass != "" && len(pass) < 6 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "密码长度至少 6 位"})
			return
		}
	}
	if !groupChanged && pass == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "没有可更新的字段"})
		return
	}
	// 只改密码时保留用户当前组，避免把“不更新组”表示成不存在的 group -1。
	if !groupChanged {
		u, ok := s.Store.User(id)
		if !ok {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("用户 %d 不存在", id)})
			return
		}
		groupID = u.GroupID
	}
	if err := s.Store.UpdateUser(id, groupID, pass); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleDeleteUser removes a user.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteUser(id); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleListTokens returns a user's PATs (masked only).
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	user, ok := s.Store.User(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "用户不存在"})
		return
	}
	out := make([]tokenJSON, 0, len(user.Tokens))
	for _, t := range user.Tokens {
		out = append(out, s.tokenJSONFor(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// handleAddToken adds a PAT to a user. An empty label defaults to "PAT".
func (s *Server) handleAddToken(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		Label string `json:"label"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "Token 不能为空"})
		return
	}
	label := req.Label
	if label == "" {
		label = "PAT"
	}
	if _, err := s.Store.AddToken(id, label, req.Token); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleDeleteToken removes a PAT from a user.
func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的 Token ID"})
		return
	}
	if err := s.Store.DeleteToken(id, tid); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleListGroups returns all groups with their user counts.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups := s.Store.Groups()
	users := s.Store.Users()
	out := make([]groupJSON, 0, len(groups))
	for _, g := range groups {
		count := 0
		for _, u := range users {
			if u.GroupID == g.ID {
				count++
			}
		}
		perms := g.Perms
		if perms == nil {
			perms = []string{}
		}
		out = append(out, groupJSON{ID: g.ID, Name: g.Name, Perms: perms, UserCount: count})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// handleCreateGroup creates a permission group.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string   `json:"name"`
		Perms []string `json:"perms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "组名不能为空"})
		return
	}
	if req.Perms == nil {
		req.Perms = []string{}
	}
	if _, err := s.Store.CreateGroup(req.Name, req.Perms); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleUpdateGroup renames a group and/or replaces its permissions.
// Empty fields are left unchanged.
func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseGroupID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name  *string   `json:"name"`
		Perms *[]string `json:"perms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	if req.Perms == nil && name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "没有可更新的字段"})
		return
	}
	perms := []string{}
	if req.Perms != nil {
		perms = *req.Perms
		if perms == nil {
			perms = []string{}
		}
	}
	if err := s.Store.UpdateGroup(id, name, perms); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleDeleteGroup removes a group; fails while any user references it.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseGroupID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteGroup(id); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func parseUserID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的用户 ID"})
		return 0, false
	}
	return id, true
}

func parseGroupID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的组 ID"})
		return 0, false
	}
	return id, true
}
