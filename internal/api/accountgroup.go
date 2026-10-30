package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"linode-panel/internal/store"
)

// ---------- 当前登录用户的账号组管理 ----------
//
// 账号组把同一 user 下的多个账号(Token)绑定成组，组内某账号的实例被封号
// 或无余额时，可用相同配置在组内另一账号重建替换。这里只提供组的增删改查
// 与成员管理，替换引擎不在此文件。

// handleListAccountGroups returns the calling user's account groups, each with
// its member token ids.
func (s *Server) handleListAccountGroups(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	groups := s.Store.AccountGroups(user.ID)
	if groups == nil {
		groups = []store.AccountGroup{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// handleCreateAccountGroup creates an empty account group for the calling user.
func (s *Server) handleCreateAccountGroup(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	g, err := s.Store.CreateAccountGroup(user.ID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// handleUpdateAccountGroup renames one of the calling user's account groups.
func (s *Server) handleUpdateAccountGroup(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	gid, ok := parseAccountGroupID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if err := s.Store.UpdateAccountGroup(user.ID, gid, req.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleDeleteAccountGroup deletes one of the calling user's account groups
// together with its member rows.
func (s *Server) handleDeleteAccountGroup(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	gid, ok := parseAccountGroupID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteAccountGroup(user.ID, gid); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleAddAccountGroupMember adds one of the calling user's tokens to one of
// the calling user's account groups.
func (s *Server) handleAddAccountGroupMember(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	gid, ok := parseAccountGroupID(w, r)
	if !ok {
		return
	}
	var req struct {
		TokenID int `json:"token_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.TokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "令牌 ID 无效"})
		return
	}
	if err := s.Store.AddAccountGroupMember(user.ID, gid, req.TokenID); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleRemoveAccountGroupMember removes a token from one of the calling user's
// account groups.
func (s *Server) handleRemoveAccountGroupMember(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	gid, ok := parseAccountGroupID(w, r)
	if !ok {
		return
	}
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "令牌 ID 无效"})
		return
	}
	if err := s.Store.RemoveAccountGroupMember(user.ID, gid, tid); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// parseAccountGroupID parses the {gid} path parameter, writing a 400 and returning
// false when it is missing or not a positive integer.
func parseAccountGroupID(w http.ResponseWriter, r *http.Request) (int, bool) {
	gid, err := strconv.Atoi(r.PathValue("gid"))
	if err != nil || gid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "组 ID 无效"})
		return 0, false
	}
	return gid, true
}
