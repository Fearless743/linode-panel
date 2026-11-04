package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"linode-panel/internal/store"
)

// ---------- TG 通知全局配置（管理员） ----------

type notifyRequest struct {
	Enabled  bool   `json:"enabled"`
	ChatID   string `json:"chat_id"`
	BotToken string `json:"bot_token"`
}

func (s *Server) handleGetNotify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.GetNotifyConfig())
}

func (s *Server) handlePutNotify(w http.ResponseWriter, r *http.Request) {
	var req notifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Enabled && req.ChatID == "" {
		// 允许只开开关后补 chat，但无 chat 时发送会被跳过；此处仅提示。
	}
	if err := s.Store.SetNotifyConfig(req.Enabled, req.ChatID, req.BotToken); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存通知配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, s.Store.GetNotifyConfig())
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.GetNotifyConfig()
	if !cfg.Enabled {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "通知未开启"})
		return
	}
	if cfg.ChatID == "" || !cfg.HasToken {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请先填写 Bot Token 与 Chat ID"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.sendTelegram(ctx, "✅ Linode 面板 TG 通知测试成功（"+time.Now().Format("15:04:05")+"）"); err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "发送失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

var _ = store.PermAdmin
