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

// ---------- 双机轮询（账号维度） ----------

type dualRequest struct {
	Enabled            bool   `json:"enabled"`
	TemplateInstanceID int    `json:"template_instance_id"`
	Port               int    `json:"port"`
	Threshold          int    `json:"threshold"`
	IntervalSec        int    `json:"interval_seconds"`
	Script             string `json:"script"`
	RootPassword       string `json:"root_password"`
}

// handleListDual 返回当前用户全部双机配置。
func (s *Server) handleListDual(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	all := s.Store.DualConfigs()
	out := make([]store.DualConfig, 0, len(all))
	for _, d := range all {
		if d.UserID == user.ID {
			out = append(out, d)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dual": out})
}

// handlePutDual 创建或更新某账号的双机配置。启用时若尚未指定 active，则需要
// template_instance_id（该账号下已有实例作模板，模板机直接作为 A）。
func (s *Server) handlePutDual(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return
	}
	var req dualRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	user, _ := UserFrom(r.Context())
	// 校验账号归属。
	toks := s.Store.TokensOf(user.ID)
	owned := false
	for _, t := range toks {
		if t.ID == tid {
			owned = true
			break
		}
	}
	if !owned {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号不存在"})
		return
	}
	pat, err := s.Store.TokenPAT(user.ID, tid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号凭证无效"})
		return
	}

	port := req.Port
	if port == 0 {
		port = DefaultDualPort
	}
	if port < 1 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "端口必须在 1-65535 之间"})
		return
	}
	threshold := req.Threshold
	if threshold <= 0 {
		threshold = DefaultDualThreshold
	}
	if threshold < 1 || threshold > 20 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "连续失败次数必须在 1-20 之间"})
		return
	}
	interval := req.IntervalSec
	if interval <= 0 {
		interval = int(DefaultDualInterval / time.Second)
	}
	if interval < 30 || interval > 24*3600 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "检测间隔必须在 30 秒到 24 小时之间"})
		return
	}

	existing, hasExisting := s.Store.DualConfigOf(tid)
	activeID := existing.ActiveInstanceID
	standbyID := existing.StandbyInstanceID
	script := req.Script
	rootPass := req.RootPassword

	if req.Enabled {
		// 确定模板/active：显式传模板时切换 active。
		// 模板等于当前 standby 则交换 A/B；模板是无关机器则旧 active 降为 standby。
		templateID := req.TemplateInstanceID
		if templateID > 0 {
			spec, ok := s.resolveTemplateSpec(r.Context(), pat, tid, templateID)
			if !ok {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "模板实例不存在或不属于该账号"})
				return
			}
			_ = spec
			if templateID != activeID {
				if templateID == standbyID {
					activeID, standbyID = standbyID, activeID
				} else {
					// 旧 standby 成为游离机，不再属于双机对。
					standbyID = activeID
					activeID = templateID
				}
			}
		} else if activeID <= 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请先选择该账号下的模板实例"})
			return
		}
		// 脚本与密码：显式提供优先，否则继承模板/已有配置。
		if strings.TrimSpace(script) == "" {
			if s, ok := s.Store.InstanceScript(activeID); ok && strings.TrimSpace(s) != "" {
				script = s
			} else if hasExisting && strings.TrimSpace(existing.Script) != "" {
				script = existing.Script
			}
		}
		if rootPass == "" {
			if p, ok := s.Store.InstanceRootPassword(activeID); ok && p != "" {
				rootPass = p
			} else if p, ok := s.Store.DualRootPassword(tid); ok && p != "" {
				rootPass = p
			}
		}
		if rootPass == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请提供 root 密码（模板未保存密码）"})
			return
		}
		cfg := store.DualConfig{
			TokenID: tid, UserID: user.ID, Enabled: true,
			Port: port, Threshold: threshold, IntervalSec: interval,
			ActiveInstanceID: activeID, StandbyInstanceID: standbyID,
			Script: strings.TrimSpace(script),
		}
		if err := s.Store.SetDualConfig(cfg, rootPass); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存双机配置失败"})
			return
		}
		// 确保 B 存在（关机待命）。失败不回滚配置，前端可重试。
		if sid, cerr := s.ensureStandbyForAPI(r.Context(), pat, tid, user.ID, activeID); cerr != nil {
			out, _ := s.Store.DualConfigOf(tid)
			writeJSON(w, http.StatusOK, map[string]any{"dual": out, "warning": cerr.Error()})
			return
		} else {
			_ = sid
		}
		// 立即探测一次，UI 直接看到状态。
		if s.dualMonitor != nil {
			if saved, ok := s.Store.DualConfigOf(tid); ok {
				go s.dualMonitor.check(context.Background(), saved)
			}
		}
		out, _ := s.Store.DualConfigOf(tid)
		writeJSON(w, http.StatusOK, map[string]any{"dual": out})
		return
	}

	// 关闭：保留 A/B 与脚本，只关开关。
	cfg := store.DualConfig{
		TokenID: tid, UserID: user.ID, Enabled: false,
		Port: port, Threshold: threshold, IntervalSec: interval,
		ActiveInstanceID: activeID, StandbyInstanceID: standbyID,
		Script: strings.TrimSpace(script),
	}
	if strings.TrimSpace(script) == "" && hasExisting {
		cfg.Script = existing.Script
	}
	if err := s.Store.SetDualConfig(cfg, rootPass); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存双机配置失败"})
		return
	}
	_ = s.Store.UpdateDualState(tid, 0, "idle", "idle", "已关闭双机轮询", time.Now().UTC().Format(time.RFC3339))
	out, _ := s.Store.DualConfigOf(tid)
	writeJSON(w, http.StatusOK, map[string]any{"dual": out})
}

// handleDeleteDual 删除账号的双机配置（两台实例本身保留）。
func (s *Server) handleDeleteDual(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return
	}
	user, _ := UserFrom(r.Context())
	if d, ok := s.Store.DualConfigOf(tid); ok && d.UserID != user.ID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "双机配置不存在"})
		return
	}
	if err := s.Store.DeleteDualConfig(tid); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "删除双机配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleRotateDual 立即执行一次轮换（不等被墙阈值）。
func (s *Server) handleRotateDual(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return
	}
	user, _ := UserFrom(r.Context())
	cfg, ok := s.Store.DualConfigOf(tid)
	if !ok || cfg.UserID != user.ID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该账号未配置双机轮询"})
		return
	}
	if !cfg.Enabled {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "双机轮询未开启"})
		return
	}
	if cfg.ActiveInstanceID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "未配置使用中实例"})
		return
	}
	if cfg.State == "rotating" || cfg.State == "rebuilding_standby" {
		// 状态疑似卡死且无运行中任务时允许重试，避免永久锁死。
		busy := s.Tasks != nil && (s.Tasks.Busy(cfg.ActiveInstanceID) || (cfg.StandbyInstanceID > 0 && s.Tasks.Busy(cfg.StandbyInstanceID)))
		// rotating 是同步上锁阶段（ForceRotate 应速进入 rotate），无 Busy 即疑似卡死
		if busy {
			writeJSON(w, http.StatusConflict, errorResponse{Error: "轮换/重建进行中，请稍后再试"})
			return
		}
		if cfg.State == "rotating" {
			// rotating 无 Busy 即上次 ForceRotate 被误拦截/进程重启残留，直接自愈
		} else {
			// rebuilding_standby 且无 Busy 时 rebuildStandbyLoop 已结束/未启动，同样自愈
			// rebuilding 任务若真在跑，Busy 必为 true，这里可安全重置
		}
	}
	if s.dualMonitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "双机轮询监控未启用"})
		return
	}
	if s.Tasks != nil && (s.Tasks.Busy(cfg.ActiveInstanceID) || (cfg.StandbyInstanceID > 0 && s.Tasks.Busy(cfg.StandbyInstanceID))) {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "轮换/重建任务进行中，请稍后再试"})
		return
	}
	_ = s.Store.UpdateDualState(tid, cfg.FailCount, "rotating", cfg.LastStatus, "手动触发立即替换", time.Now().UTC().Format(time.RFC3339))
	go s.dualMonitor.ForceRotate(context.Background(), cfg)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "token_id": tid})
}

// handleCheckDual 立即探测一次并返回最新状态。
func (s *Server) handleCheckDual(w http.ResponseWriter, r *http.Request) {
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号 ID 无效"})
		return
	}
	user, _ := UserFrom(r.Context())
	cfg, ok := s.Store.DualConfigOf(tid)
	if !ok || cfg.UserID != user.ID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该账号未配置双机轮询"})
		return
	}
	if s.dualMonitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "双机轮询监控未启用"})
		return
	}
	s.dualMonitor.check(r.Context(), cfg)
	out, _ := s.Store.DualConfigOf(tid)
	writeJSON(w, http.StatusOK, map[string]any{"dual": out})
}

// resolveTemplateSpec 解析模板实例的配置（缓存优先，API 兜底），并校验归属。
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
	cfg, ok := s.Store.DualConfigOf(tokenID)
	if !ok {
		return 0, errDualNoConfig
	}
	if cfg.StandbyInstanceID > 0 {
		if _, err := s.clientFor(pat).GetInstance(ctx, cfg.StandbyInstanceID); err == nil {
			return cfg.StandbyInstanceID, nil
		} else {
			var apiErr *linode.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				if derr := s.Store.DeleteInstanceCache(tokenID, cfg.StandbyInstanceID); derr != nil {
					_ = s.Store.MarkInstanceMissing(tokenID, cfg.StandbyInstanceID)
				}
			}
		}
	}
	spec, ok := s.resolveTemplateSpec(ctx, pat, tokenID, activeID)
	if !ok || spec.Region == "" {
		return 0, errDualNoSpec
	}
	rootPass, _ := s.Store.DualRootPassword(tokenID)
	if rootPass == "" {
		return 0, errDualNoPass
	}
	label := dualStandbyLabel(strings.TrimSpace(spec.Label))
	booted := false
	newInst, err := s.clientFor(pat).CreateInstance(ctx, linode.CreateOptions{
		Region: spec.Region, Type: spec.Type, Image: spec.Image,
		Label: label, Tags: spec.Tags, RootPass: rootPass, Booted: &booted,
	})
	if err != nil {
		return 0, err
	}
	if err := s.clientFor(pat).AttachAllowAllFirewall(ctx, spec.Region, newInst.ID); err != nil {
		// 尽力而为。
	}
	if err := s.Store.UpsertInstanceCache(tokenID, newInst); err != nil {
		// 缓存失败不阻塞。
	}
	preservedActive := activeID
	if fresh, ok := s.Store.DualConfigOf(tokenID); ok && fresh.ActiveInstanceID > 0 {
		preservedActive = fresh.ActiveInstanceID
	}
	_ = s.Store.UpdateDualInstances(tokenID, preservedActive, newInst.ID)
	_ = s.Store.UpdateDualState(tokenID, 0, "idle", "idle",
		"备用机 #"+strconv.Itoa(newInst.ID)+" 已创建，关机待命", time.Now().UTC().Format(time.RFC3339))
	return newInst.ID, nil
}

var (
	errDualNoConfig = errMsg("双机配置不存在")
	errDualNoSpec   = errMsg("模板配置缺失，无法创建备用机")
	errDualNoPass   = errMsg("未保存 root 密码，无法创建备用机")
)

// dualStandbyLabel 由模板名派生备用机名：Linode label 仅允许字母/数字/
// 下划线/横线/点号，因此后缀用 ASCII "-standby"，并把非法字符替换为 "-"。
// 主→备 后主会去掉 -standby 前缀（见 renameActiveLabel），因此这里始终追加，避免新备与新主同名成“都是主”。
func dualStandbyLabel(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "dual"
	}
	// 已有备用后缀：先剥掉再加，保持根名稳定，避免每次轮换名字无限变长。
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
