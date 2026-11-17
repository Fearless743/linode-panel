package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// ---------- 账号级「被墙自愈」统一配置 ----------
//
// 原「自动换 IP」与「双机轮询」合并为一个功能：账号选择一种模式
// （rebuild=删机重建换 IP，dual=双机轮换），共用检测参数、脚本与 root 密码。

type selfHealRequest struct {
	Mode               string `json:"mode"`
	Enabled            bool   `json:"enabled"`
	Port               int    `json:"port"`
	Threshold          int    `json:"threshold"`
	IntervalSec        int    `json:"interval_seconds"`
	Script             string `json:"script"`
	RootPassword       string `json:"root_password"`
	TemplateInstanceID int    `json:"template_instance_id"`
}

// handleListSelfHeal 返回当前用户全部账号的自愈配置。
func (s *Server) handleListSelfHeal(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	all := s.Store.SelfHeals()
	out := make([]store.SelfHeal, 0, len(all))
	for _, c := range all {
		if c.UserID == user.ID {
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"self_heal": out})
}

// handleGetSelfHeal 返回某账号的自愈配置（从未配置过时 mode=off）。
func (s *Server) handleGetSelfHeal(w http.ResponseWriter, r *http.Request) {
	tid, user, ok := selfHealToken(w, r)
	if !ok {
		return
	}
	if !s.ownsToken(user, tid) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号不存在"})
		return
	}
	writeJSON(w, http.StatusOK, s.Store.SelfHealOf(tid))
}

// handlePutSelfHeal 创建或更新账号的自愈配置。
func (s *Server) handlePutSelfHeal(w http.ResponseWriter, r *http.Request) {
	tid, user, ok := selfHealToken(w, r)
	if !ok {
		return
	}
	var req selfHealRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if !s.ownsToken(user, tid) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号不存在"})
		return
	}
	mode := normalizeSelfHealMode(req.Mode)
	if mode == store.SelfHealOff {
		req.Enabled = false
	}

	existing := s.Store.SelfHealOf(tid)
	port := req.Port
	if port == 0 {
		// 未显式传端口时沿用账号已保存的检测端口，避免回退到全局 SSH 端口
		// 导致被墙探测端口与账号设置不一致。
		port = existing.Port
	}
	if port == 0 {
		port = s.sshPort()
	}
	if port < 1 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "端口必须在 1-65535 之间"})
		return
	}
	threshold := req.Threshold
	if threshold <= 0 {
		threshold = DefaultSelfHealThreshold
	}
	if threshold < 1 || threshold > 20 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "连续失败次数必须在 1-20 之间"})
		return
	}
	interval := req.IntervalSec
	if interval <= 0 {
		interval = int(DefaultSelfHealInterval / time.Second)
	}
	if interval < 30 || interval > 24*3600 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "检测间隔必须在 30 秒到 24 小时之间"})
		return
	}

	activeID, standbyID := existing.ActiveInstanceID, existing.StandbyInstanceID
	script := strings.TrimSpace(req.Script)
	rootPass := req.RootPassword

	pat, err := s.Store.TokenPAT(user.ID, tid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号凭证无效"})
		return
	}

	if mode == store.SelfHealDual && req.Enabled {
		// 显式模板时切换 A/B：等于当前 standby 则交换，无关机器则旧 A 降为 B。
		if templateID := req.TemplateInstanceID; templateID > 0 {
			spec, found := s.resolveTemplateSpec(r.Context(), pat, tid, templateID)
			if !found || spec.Region == "" {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "模板实例不存在或不属于该账号"})
				return
			}
			if templateID != activeID {
				if templateID == standbyID {
					activeID, standbyID = standbyID, activeID
				} else {
					standbyID, activeID = activeID, templateID
				}
			}
		} else if activeID <= 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请先选择该账号下的模板实例"})
			return
		}
		if script == "" {
			if v, ok := s.Store.AccountScript(tid); ok && strings.TrimSpace(v) != "" {
				script = v
			} else if strings.TrimSpace(existing.Script) != "" {
				script = existing.Script
			}
		}
		if rootPass == "" {
			rootPass, _ = s.Store.AccountRootPassword(tid)
		}
		if rootPass == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请提供 root 密码（账号未保存密码）"})
			return
		}
	} else {
		// rebuild / off：模板与 A/B 指针无意义。
		activeID, standbyID = 0, 0
		if script == "" && strings.TrimSpace(existing.Script) != "" {
			script = existing.Script
		}
	}

	cfg := store.SelfHeal{
		TokenID: tid, UserID: user.ID, Mode: mode, Enabled: req.Enabled,
		Port: port, Threshold: threshold, IntervalSec: interval, Script: script,
		ActiveInstanceID: activeID, StandbyInstanceID: standbyID,
	}
	if err := s.Store.SetSelfHeal(cfg, rootPass); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存自愈配置失败"})
		return
	}

	var warning string
	if mode == store.SelfHealDual && req.Enabled {
		if _, err := s.ensureStandbyForAPI(r.Context(), pat, tid, user.ID, activeID); err != nil {
			warning = err.Error()
		}
	}
	if req.Enabled && s.selfHealMonitor != nil {
		go s.selfHealMonitor.checkNow(context.Background(), s.Store.SelfHealOf(tid))
	}
	out := s.Store.SelfHealOf(tid)
	if warning != "" {
		writeJSON(w, http.StatusOK, map[string]any{"self_heal": out, "warning": warning})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteSelfHeal 关闭账号的自愈配置（实例本身保留）。
func (s *Server) handleDeleteSelfHeal(w http.ResponseWriter, r *http.Request) {
	tid, user, ok := selfHealToken(w, r)
	if !ok {
		return
	}
	if !s.ownsToken(user, tid) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "自愈配置不存在"})
		return
	}
	if err := s.Store.DeleteSelfHeal(tid); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "删除自愈配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleCheckSelfHeal 立即探测一次并返回最新状态。
func (s *Server) handleCheckSelfHeal(w http.ResponseWriter, r *http.Request) {
	tid, user, ok := selfHealToken(w, r)
	if !ok {
		return
	}
	cfg := s.Store.SelfHealOf(tid)
	if cfg.TokenID != tid || cfg.UserID != user.ID || !cfg.Enabled {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该账号未开启自愈"})
		return
	}
	if s.selfHealMonitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "自愈监测未启用"})
		return
	}
	if cfg.Mode == store.SelfHealDual && s.selfHealMonitor.isBusy(tid) {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "检测/轮换/重建进行中，请稍后再试"})
		return
	}
	s.selfHealMonitor.clearCooldown(tid)
	if cfg.Mode == store.SelfHealDual && cfg.State == dualStateCooldown {
		_ = s.Store.UpdateSelfHealState(tid, cfg.FailCount, "idle", cfg.LastStatus, "手动检测，退出重建冷却", time.Now().UTC().Format(time.RFC3339))
		cfg.State = "idle"
	}
	s.selfHealMonitor.checkNow(context.Background(), cfg)
	writeJSON(w, http.StatusOK, s.Store.SelfHealOf(tid))
}

// handleRotateSelfHeal 立即执行一次双机轮换（不等被墙阈值）。
func (s *Server) handleRotateSelfHeal(w http.ResponseWriter, r *http.Request) {
	tid, user, ok := selfHealToken(w, r)
	if !ok {
		return
	}
	cfg, found := s.Store.SelfHealConfigOf(tid)
	if !found || cfg.UserID != user.ID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该账号未配置双机自愈"})
		return
	}
	if !cfg.Enabled || cfg.ActiveInstanceID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "双机自愈未开启或未配置使用中实例"})
		return
	}
	if s.selfHealMonitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "自愈监测未启用"})
		return
	}
	if s.Tasks != nil && (s.Tasks.Busy(cfg.ActiveInstanceID) ||
		(cfg.StandbyInstanceID > 0 && s.Tasks.Busy(cfg.StandbyInstanceID))) {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "轮换/重建任务进行中，请稍后再试"})
		return
	}
	_ = s.Store.UpdateSelfHealState(tid, cfg.FailCount, "rotating", cfg.LastStatus, "手动触发立即替换", time.Now().UTC().Format(time.RFC3339))
	go s.selfHealMonitor.ForceRotate(context.Background(), cfg)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "token_id": tid})
}

// selfHealToken 解析路径里的账号 ID，并取回当前用户。
func selfHealToken(w http.ResponseWriter, r *http.Request) (int, store.User, bool) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return 0, store.User{}, false
	}
	user, _ := UserFrom(r.Context())
	return tid, user, true
}

func (s *Server) ownsToken(user store.User, tid int) bool {
	for _, t := range s.Store.TokensOf(user.ID) {
		if t.ID == tid {
			return true
		}
	}
	return false
}

func normalizeSelfHealMode(mode string) string {
	switch mode {
	case store.SelfHealRebuild, store.SelfHealDual:
		return mode
	default:
		return store.SelfHealOff
	}
}

// resolveTemplateSpec 解析模板实例的规格（缓存优先，API 兜底），并校验归属。
func (s *Server) resolveTemplateSpec(ctx context.Context, pat string, tokenID, instanceID int) (linode.Instance, bool) {
	if row, ok := s.Store.InstanceCacheRow(tokenID, instanceID); ok {
		return row.Instance, true
	}
	if inst, err := s.clientFor(pat).GetInstance(ctx, instanceID); err == nil {
		return inst, true
	}
	if cached, ok := s.Store.CachedInstance(instanceID); ok && cached.Region != "" {
		return cached, true
	}
	return linode.Instance{}, false
}

// ensureStandbyForAPI 确保 B 存在且关机待命，返回 B 的实例 id。
func (s *Server) ensureStandbyForAPI(ctx context.Context, pat string, tokenID, userID, activeID int) (int, error) {
	cfg, ok := s.Store.SelfHealConfigOf(tokenID)
	if !ok {
		return 0, errSelfHealNoConfig
	}
	if cfg.StandbyInstanceID > 0 {
		if _, err := s.clientFor(pat).GetInstance(ctx, cfg.StandbyInstanceID); err == nil {
			return cfg.StandbyInstanceID, nil
		} else {
			var apiErr *linode.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				_ = s.Store.MarkInstanceMissing(tokenID, cfg.StandbyInstanceID)
			}
		}
	}
	spec, ok := s.resolveTemplateSpec(ctx, pat, tokenID, activeID)
	if !ok || spec.Region == "" {
		return 0, errSelfHealNoSpec
	}
	rootPass, _ := s.Store.AccountRootPassword(tokenID)
	if rootPass == "" {
		return 0, errSelfHealNoPass
	}
	label := healStandbyLabel(strings.TrimSpace(spec.Label))
	booted := false
	newInst, err := s.clientFor(pat).CreateInstance(ctx, linode.CreateOptions{
		Region: spec.Region, Type: spec.Type, Image: spec.Image,
		Label: label, Tags: spec.Tags, RootPass: rootPass, Booted: &booted,
	})
	if err != nil {
		return 0, err
	}
	_ = s.clientFor(pat).AttachAllowAllFirewall(ctx, spec.Region, newInst.ID)
	_ = s.Store.UpsertInstanceCache(tokenID, newInst)
	preservedActive := activeID
	if fresh, ok := s.Store.SelfHealConfigOf(tokenID); ok && fresh.ActiveInstanceID > 0 {
		preservedActive = fresh.ActiveInstanceID
	}
	_ = s.Store.UpdateSelfHealInstances(tokenID, preservedActive, newInst.ID)
	_ = s.Store.UpdateSelfHealState(tokenID, 0, "idle", "idle",
		"备用机 #"+strconv.Itoa(newInst.ID)+" 已创建，关机待命", time.Now().UTC().Format(time.RFC3339))
	return newInst.ID, nil
}

var (
	errSelfHealNoConfig = errMsg("自愈配置不存在")
	errSelfHealNoSpec   = errMsg("模板配置缺失，无法创建备用机")
	errSelfHealNoPass   = errMsg("未保存 root 密码，无法创建备用机")
)

// healStandbyLabel 由模板名派生备用机名。
func healStandbyLabel(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "dual"
	}
	if strings.HasSuffix(base, "-standby") {
		base = strings.TrimSuffix(base, "-standby")
	}
	return sanitizeLinodeLabel(base + "-standby")
}

// sanitizeLinodeLabel 把非法字符替换为 "-"，并截断到 64 字符。
func sanitizeLinodeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	out = strings.Trim(out, "-._")
	if out == "" {
		return "dual-standby"
	}
	return out
}

type errMsg string

func (e errMsg) Error() string { return string(e) }
