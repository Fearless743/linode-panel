package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// ---------- 手动迁移（上游已不存在的实例 -> 另一个账号） ----------
//
// 实例在上游被删除/替换后，缓存行会以 alive=0 保留并显示为「不可用」。
// 这里允许用户选择另一个账号（PAT），用缓存中的 region/type/image/label
// 以及已保存的开机脚本、root 密码在该账号上创建一个新实例，并把稳定缓存行
// （连同脚本、root 密码、自动换 IP、替换配置）迁移到新实例/新账号。

// migrateInstanceRequest 是 POST /api/instances/{id}/migrate 的请求体。
type migrateInstanceRequest struct {
	// TokenID 是实例当前所属账号；0/缺省时表示用户的第一个账号。
	TokenID int `json:"token_id"`
	// TargetTokenID 是目标账号，必须属于当前用户且不同于来源账号。
	TargetTokenID int `json:"target_token_id"`
	// RootPassword 可覆盖缓存中已保存的 root 密码；未保存密码时必须填写。
	RootPassword string `json:"root_password"`
	// Label 可选覆盖缓存中的实例名称。
	Label string `json:"label"`
}

func (s *Server) handleMigrateInstance(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req migrateInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	user, _ := UserFrom(r.Context())

	sourceID := req.TokenID
	if sourceID <= 0 {
		sourceID = s.tokenIDFor(user, "")
	} else if s.tokenIDFor(user, strconv.Itoa(sourceID)) != sourceID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "实例所属账号不存在"})
		return
	}
	if sourceID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择实例所属账号"})
		return
	}
	if req.TargetTokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择目标账号"})
		return
	}
	if req.TargetTokenID == sourceID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "目标账号不能与当前账号相同"})
		return
	}
	targetPAT, err := s.resolveToken(user, strconv.Itoa(req.TargetTokenID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "目标账号不存在"})
		return
	}

	row, found := s.Store.InstanceCacheRow(sourceID, id)
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "实例缓存不存在"})
		return
	}
	if row.Alive {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "实例在 Linode 仍存在，无需迁移"})
		return
	}
	spec := row.Instance
	if spec.Region == "" || spec.Type == "" || spec.Image == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "缓存中缺少地区/机型/镜像，无法迁移"})
		return
	}

	// root 密码：请求显式提供优先；否则沿用该实例已保存的密码。
	rootPass, _ := s.Store.InstanceRootPasswordForCacheID(row.CacheID)
	if strings.TrimSpace(req.RootPassword) != "" {
		rootPass = req.RootPassword
	}
	if rootPass == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该实例未保存 root 密码，请填写 root 密码"})
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = spec.Label
	}

	// 已保存的开机脚本随实例迁移：创建后在实例启动后通过 root 密码执行。
	script := ""
	if v, ok := s.Store.InstanceScriptForCacheID(row.CacheID); ok {
		script = strings.TrimSpace(v)
	}
	opts := linode.CreateOptions{
		Region:   spec.Region,
		Type:     spec.Type,
		Image:    spec.Image,
		Label:    label,
		RootPass: rootPass,
		Tags:     spec.Tags,
	}

	inst, err := s.clientFor(targetPAT).CreateInstance(r.Context(), opts)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "在目标账号创建实例失败: " + err.Error()})
		return
	}

	region := inst.Region
	if region == "" {
		region = spec.Region
	}
	if err := s.clientFor(targetPAT).AttachAllowAllFirewall(r.Context(), region, inst.ID); err != nil {
		slog.Warn("迁移实例: 开放所有端口防火墙绑定失败（继续）", "instance_id", inst.ID, "error", err)
	}
	if err := s.Store.MigrateInstanceCache(row.CacheID, req.TargetTokenID, inst); err != nil {
		slog.Warn("迁移实例缓存失败", "from_token", sourceID, "to_token", req.TargetTokenID,
			"old_instance_id", id, "new_instance_id", inst.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "实例已创建，但迁移面板记录失败: " + err.Error()})
		return
	}
	// 显式提供的密码写回配置；未提供时已保存的密码随稳定缓存 id 自动保留。
	if strings.TrimSpace(req.RootPassword) != "" {
		if err := s.Store.SetInstanceRootPassword(inst.ID, req.RootPassword); err != nil {
			slog.Warn("迁移实例: 保存 root 密码失败（继续）", "instance_id", inst.ID, "error", err)
		}
	}
	if script != "" {
		s.startBootScript(inst.ID, targetPAT, rootPass, script)
	}

	targetTok := store.Token{ID: req.TargetTokenID, Label: s.tokenLabel(user, strconv.Itoa(req.TargetTokenID))}
	writeJSON(w, http.StatusOK, map[string]any{
		"instance":        s.instanceJSONFor(inst, targetTok, "", false),
		"old_instance_id": id,
		"token_id":        req.TargetTokenID,
		"token_label":     targetTok.Label,
	})
}
