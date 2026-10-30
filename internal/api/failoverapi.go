package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"linode-panel/internal/store"
)

// ---------- 账号组自动替换配置的 HTTP 接口 ----------
//
// 在这里管理「某实例的承载账号被封号/无余额时，自动换到同组另一账号」的
// 按实例配置。替换引擎在 failover.go；本文件只做校验、落库与读取。
// 所有接口均限定为当前登录用户自己的配置。

// failoverRequest 是 PUT /api/account/failover/{iid} 的请求体。
type failoverRequest struct {
	TokenID     int  `json:"token_id"`
	GroupID     int  `json:"group_id"`
	Enabled     bool `json:"enabled"`
	Threshold   int  `json:"threshold"`
	IntervalSec int  `json:"interval_seconds"`
}

// parseFailoverInstanceID 解析 {iid} 路径参数；缺失或非正整数时写 400 并返回 false。
func parseFailoverInstanceID(w http.ResponseWriter, r *http.Request) (int, bool) {
	iid, err := strconv.Atoi(r.PathValue("iid"))
	if err != nil || iid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "实例 ID 无效"})
		return 0, false
	}
	return iid, true
}

// handleListFailover 返回当前登录用户的全部替换配置（无配置时 failovers 为 []）。
func (s *Server) handleListFailover(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	all := s.Store.Failovers()
	out := make([]store.Failover, 0, len(all))
	for _, f := range all {
		if f.UserID == user.ID {
			out = append(out, f)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"failovers": out})
}

// handlePutFailover 创建或更新一个实例的替换配置。
func (s *Server) handlePutFailover(w http.ResponseWriter, r *http.Request) {
	iid, ok := parseFailoverInstanceID(w, r)
	if !ok {
		return
	}
	var req failoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.TokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return
	}
	if req.GroupID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择账号组"})
		return
	}
	user, _ := UserFrom(r.Context())

	g, ok := s.Store.AccountGroup(user.ID, req.GroupID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号组不存在"})
		return
	}
	// 承载账号必须是该组成员，否则替换没有意义。
	member := false
	for _, tid := range g.TokenIDs {
		if tid == req.TokenID {
			member = true
			break
		}
	}
	if !member {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该账号不在所选账号组内"})
		return
	}

	threshold := req.Threshold
	if threshold <= 0 {
		threshold = DefaultFailoverThreshold
	}
	interval := req.IntervalSec
	if interval <= 0 {
		interval = int(DefaultFailoverInterval / time.Second)
	}

	cfg := store.Failover{
		InstanceID:  iid,
		UserID:      user.ID,
		GroupID:     req.GroupID,
		TokenID:     req.TokenID,
		Enabled:     req.Enabled,
		Threshold:   threshold,
		IntervalSec: interval,
	}
	if err := s.Store.SetFailover(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "保存替换配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleDeleteFailover 删除一个实例的替换配置（幂等：删除不存在的不报错）。
func (s *Server) handleDeleteFailover(w http.ResponseWriter, r *http.Request) {
	iid, ok := parseFailoverInstanceID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteFailover(iid); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "删除替换配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}
