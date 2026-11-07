package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"linode-panel/internal/store"
)

// 账号级配置：script / root 密码 / auto-rebuild 全在账号里，
// 同账号所有实例共用；单实例入口已仅作账号别名。
type accountConfigRequest struct {
	Script       *string `json:"script"`
	RootPassword *string `json:"root_password"`
	ClearScript  bool    `json:"clear_script"`
	ClearPass    bool    `json:"clear_root_password"`
	Enabled      *bool   `json:"enabled"`
	Port         int     `json:"port"`
	Threshold    int     `json:"threshold"`
	IntervalSec  int     `json:"interval_seconds"`
}

func (s *Server) accountOwned(tid int, user store.User) bool {
	for _, t := range s.Store.TokensOf(user.ID) {
		if t.ID == tid {
			return true
		}
	}
	return false
}

func (s *Server) handleGetAccountConfig(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return
	}
	user, _ := UserFrom(r.Context())
	if !s.accountOwned(tid, user) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号不存在"})
		return
	}
	c, ok := s.Store.AccountConfig(tid)
	if !ok {
		c = store.AccountConfig{TokenID: tid, UserID: user.ID, Port: 22, Threshold: 3, IntervalSec: 300}
	}
	if c.Port == 0 {
		c.Port = 22
	}
	if c.Threshold == 0 {
		c.Threshold = 3
	}
	if c.IntervalSec == 0 {
		c.IntervalSec = 300
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handlePutAccountConfig(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return
	}
	user, _ := UserFrom(r.Context())
	if !s.accountOwned(tid, user) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号不存在"})
		return
	}
	var req accountConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "端口必须在 1-65535 之间"})
		return
	}
	if req.Threshold != 0 && (req.Threshold < 1 || req.Threshold > 20) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "连续失败次数必须在 1-20 之间"})
		return
	}
	if req.IntervalSec != 0 && (req.IntervalSec < 30 || req.IntervalSec > 24*3600) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "检测间隔必须在 30 秒到 24 小时之间"})
		return
	}
	// 脚本与密码
	if req.ClearScript {
		if err := s.Store.DeleteAccountScript(tid); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "清除脚本失败"})
			return
		}
	} else if req.Script != nil {
		if err := s.Store.SetAccountScript(tid, user.ID, *req.Script); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存脚本失败"})
			return
		}
	}
	if req.ClearPass {
		if err := s.Store.DeleteAccountRootPassword(tid); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "清除 root 密码失败"})
			return
		}
	} else if req.RootPassword != nil && *req.RootPassword != "" {
		if err := s.Store.SetAccountRootPassword(tid, user.ID, *req.RootPassword); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存 root 密码失败"})
			return
		}
	}
	// 自动换 IP
	if req.Enabled != nil {
		port := req.Port
		if port == 0 {
			if c, ok := s.Store.AccountConfig(tid); ok && c.Port != 0 {
				port = c.Port
			} else {
				port = 22
			}
		}
		th := req.Threshold
		if th == 0 {
			if c, ok := s.Store.AccountConfig(tid); ok && c.Threshold != 0 {
				th = c.Threshold
			} else {
				th = 3
			}
		}
		iv := req.IntervalSec
		if iv == 0 {
			if c, ok := s.Store.AccountConfig(tid); ok && c.IntervalSec != 0 {
				iv = c.IntervalSec
			} else {
				iv = 300
			}
		}
		if err := s.Store.SetAccountAutoRebuild(tid, user.ID, *req.Enabled, port, th, iv); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存自动换IP配置失败"})
			return
		}
	}
	c, _ := s.Store.AccountConfig(tid)
	writeJSON(w, http.StatusOK, c)
}
