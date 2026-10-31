package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"linode-panel/internal/store"
)

// ---------- 当前登录用户的账号组管理 ----------
//
// 账号组把同一 user 下的多个账号(Token)绑定成组，组内某账号的实例被封号
// 或无余额时，可用相同配置在组内另一账号重建替换。这里只提供组的增删改查
// 与成员管理，替换引擎不在此文件。

// accountGroupOrEmpty normalizes g.TokenIDs to a non-nil slice so the JSON
// contract always renders token_ids as [] instead of null (store may return
// nil for a group with no members).
func accountGroupOrEmpty(g store.AccountGroup) store.AccountGroup {
	if g.TokenIDs == nil {
		g.TokenIDs = []int{}
	}
	return g
}

// handleListAccountGroups returns the calling user's account groups, each with
// its member token ids.
func (s *Server) handleListAccountGroups(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	groups := s.Store.AccountGroups(user.ID)
	out := make([]store.AccountGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, accountGroupOrEmpty(g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
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
	writeJSON(w, http.StatusCreated, accountGroupOrEmpty(g))
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

// handleGetAccountGroupInstanceConfig 返回账号组的目标开机配置。
// GET /api/account/groups/{gid}/instance-config
func (s *Server) handleGetAccountGroupInstanceConfig(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	gid, ok := parseAccountGroupID(w, r)
	if !ok {
		return
	}
	cfg, found := s.Store.InstanceConfig(user.ID, gid)
	// 若组不存在或不属于当前 user，InstanceConfig 返回 false；
	// 区分"组不存在"和"组存在但未配置"两种 false。
	if !found {
		_, groupExists := s.Store.AccountGroup(user.ID, gid)
		if !groupExists {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号组不存在"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg})
}

// handlePutAccountGroupInstanceConfig 设置或清除账号组的目标开机配置。
// PUT /api/account/groups/{gid}/instance-config
// 请求体：{"config":{"region":"","type":"","image":"","label":"","script":"","booted":true}|null}
func (s *Server) handlePutAccountGroupInstanceConfig(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	gid, ok := parseAccountGroupID(w, r)
	if !ok {
		return
	}
	// 先校验组属于当前 user
	if _, groupExists := s.Store.AccountGroup(user.ID, gid); !groupExists {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号组不存在"})
		return
	}
	var req struct {
		Config *store.InstanceConfigJSON `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	// config == nil 表示清除配置。
	if req.Config != nil {
		cfg := req.Config
		// 校验必填字段
		if strings.TrimSpace(cfg.Region) == "" ||
			strings.TrimSpace(cfg.Type) == "" ||
			strings.TrimSpace(cfg.Image) == "" ||
			strings.TrimSpace(cfg.Label) == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "region/type/image/label 均不能为空"})
			return
		}
		// booted 缺省默认 true
		if cfg.Booted == nil {
			v := true
			cfg.Booted = &v
		}
	}
	if err := s.Store.UpdateAccountGroupConfig(user.ID, gid, req.Config); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}
