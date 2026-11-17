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

// ---------- 统一迁移：单个实例 / 整个账号 ----------
//
// 用 scope=instance 迁移一台上游已不存在的实例；用 scope=account 迁移一个
// 账号的全部实例与面板配置。两者共用请求入口 POST /api/migrate。

type migrateRequest struct {
	Scope         string `json:"scope"` // instance | account
	InstanceID    int    `json:"instance_id,omitempty"`
	SourceTokenID int    `json:"source_token_id,omitempty"`
	TargetTokenID int    `json:"target_token_id"`
	DeleteSource  bool   `json:"delete_source,omitempty"`
	RootPassword  string `json:"root_password,omitempty"`
	Label         string `json:"label,omitempty"`
}

type migrateResult struct {
	SourceInstanceID int    `json:"source_instance_id"`
	TargetInstanceID int    `json:"target_instance_id,omitempty"`
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
}

type migrateResponse struct {
	Migrated int             `json:"migrated"`
	Failed   int             `json:"failed"`
	Results  []migrateResult `json:"results"`
}

func (s *Server) handleMigrate(w http.ResponseWriter, r *http.Request) {
	var req migrateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Scope == "" {
		req.Scope = "instance"
	}
	if req.TargetTokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择目标账号"})
		return
	}
	switch req.Scope {
	case "instance":
		s.migrateOneInstance(w, r, req)
	case "account":
		s.migrateAccount(w, r, req)
	default:
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "迁移范围无效"})
	}
}

func (s *Server) migrateOneInstance(w http.ResponseWriter, r *http.Request, req migrateRequest) {
	user, _ := UserFrom(r.Context())
	id := req.InstanceID
	if id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择要迁移的实例"})
		return
	}
	sourceID := req.SourceTokenID
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
	script, _ := s.Store.InstanceScriptForCacheID(row.CacheID)
	opts := linode.CreateOptions{
		Region: spec.Region, Type: spec.Type, Image: spec.Image,
		Label: label, RootPass: rootPass, Tags: spec.Tags,
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
		slog.Warn("迁移实例: 防火墙绑定失败（继续）", "instance_id", inst.ID, "error", err)
	}
	if err := s.Store.MigrateInstanceCache(row.CacheID, req.TargetTokenID, inst); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "实例已创建，但迁移面板记录失败: " + err.Error()})
		return
	}
	if err := s.Store.ReassignDualInstance(sourceID, req.TargetTokenID, id, inst.ID); err != nil {
		slog.Warn("迁移实例双机指针失败", "error", err)
	}
	if strings.TrimSpace(req.RootPassword) != "" {
		_ = s.Store.SetInstanceRootPassword(inst.ID, req.RootPassword)
	}
	if strings.TrimSpace(script) != "" {
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

func (s *Server) migrateAccount(w http.ResponseWriter, r *http.Request, req migrateRequest) {
	user, _ := UserFrom(r.Context())
	if req.SourceTokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择源账号与目标账号"})
		return
	}
	if req.SourceTokenID == req.TargetTokenID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "源账号与目标账号不能相同"})
		return
	}
	owned := map[int]bool{}
	for _, t := range s.Store.TokensOf(user.ID) {
		owned[t.ID] = true
	}
	if !owned[req.SourceTokenID] || !owned[req.TargetTokenID] {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "账号不存在"})
		return
	}
	srcPAT, err := s.Store.TokenPAT(user.ID, req.SourceTokenID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "源账号凭证无效"})
		return
	}
	tgtPAT, err := s.Store.TokenPAT(user.ID, req.TargetTokenID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "目标账号凭证无效"})
		return
	}

	rows := s.Store.CachedInstanceRows(req.SourceTokenID)
	resp := migrateResponse{Results: []migrateResult{}}
	ctx := r.Context()
	idMap := map[int]int{}
	for _, row := range rows {
		spec := row.Instance
		res := migrateResult{SourceInstanceID: spec.ID}
		if spec.Region == "" || spec.Type == "" || spec.Image == "" {
			res.Error = "缓存缺少地区/机型/镜像"
			resp.Results = append(resp.Results, res)
			resp.Failed++
			continue
		}
		rootPass, _ := s.Store.InstanceRootPasswordForCacheID(row.CacheID)
		script, _ := s.Store.InstanceScriptForCacheID(row.CacheID)
		label := strings.TrimSpace(spec.Label)
		if label == "" {
			label = "migrated"
		}
		booted := true
		newInst, err := s.clientFor(tgtPAT).CreateInstance(ctx, linode.CreateOptions{
			Region: spec.Region, Type: spec.Type, Image: spec.Image,
			Label: label, Tags: spec.Tags, RootPass: rootPass, Booted: &booted,
		})
		if err != nil {
			res.Error = "创建实例失败: " + err.Error()
			resp.Results = append(resp.Results, res)
			resp.Failed++
			continue
		}
		region := newInst.Region
		if region == "" {
			region = spec.Region
		}
		_ = s.clientFor(tgtPAT).AttachAllowAllFirewall(ctx, region, newInst.ID)
		if err := s.Store.MigrateInstanceCache(row.CacheID, req.TargetTokenID, newInst); err != nil {
			res.Error = "缓存迁移失败: " + err.Error()
			resp.Results = append(resp.Results, res)
			resp.Failed++
			continue
		}
		if strings.TrimSpace(script) != "" && rootPass != "" {
			s.startBootScript(newInst.ID, tgtPAT, rootPass, script)
		}
		if req.DeleteSource && row.Alive {
			_ = s.clientFor(srcPAT).DeleteInstance(ctx, spec.ID)
			_ = s.Store.DeleteInstanceCache(req.SourceTokenID, spec.ID)
		}
		res.TargetInstanceID = newInst.ID
		res.OK = true
		resp.Results = append(resp.Results, res)
		resp.Migrated++
		if spec.ID > 0 && newInst.ID > 0 {
			idMap[spec.ID] = newInst.ID
		}
	}

	// 统一自愈配置随账号迁移：源有、目标无时复制并按 idMap 重绑指针。
	srcHeal := s.Store.SelfHealOf(req.SourceTokenID)
	if srcHeal.Mode != store.SelfHealOff {
		dstHeal := s.Store.SelfHealOf(req.TargetTokenID)
		if dstHeal.Mode == store.SelfHealOff {
			nh := srcHeal
			nh.TokenID, nh.UserID = req.TargetTokenID, user.ID
			nh.ActiveInstanceID = idMap[srcHeal.ActiveInstanceID]
			nh.StandbyInstanceID = idMap[srcHeal.StandbyInstanceID]
			nh.State = "idle"
			nh.FailCount = 0
			nh.LastCheckAt, nh.LastDetail = "", ""
			pass, _ := s.Store.AccountRootPassword(req.SourceTokenID)
			_ = s.Store.SetSelfHeal(nh, pass)
		} else if dstHeal.Mode == store.SelfHealDual {
			a, b := dstHeal.ActiveInstanceID, dstHeal.StandbyInstanceID
			if a == 0 && srcHeal.ActiveInstanceID > 0 {
				a = idMap[srcHeal.ActiveInstanceID]
			}
			if b == 0 && srcHeal.StandbyInstanceID > 0 {
				b = idMap[srcHeal.StandbyInstanceID]
			}
			if a != dstHeal.ActiveInstanceID || b != dstHeal.StandbyInstanceID {
				_ = s.Store.UpdateSelfHealInstances(req.TargetTokenID, a, b)
			}
		}
	}
	// Agent 绑定：目标未绑定时继承源。
	var srcAgent int
	for _, t := range s.Store.TokensOf(user.ID) {
		if t.ID == req.SourceTokenID {
			srcAgent = t.AgentID
		}
	}
	if srcAgent > 0 {
		_ = s.Store.SetTokenAgent(user.ID, req.TargetTokenID, srcAgent)
	}
	_ = s.Store.InvalidateInstanceCache(req.TargetTokenID)
	if req.DeleteSource {
		_ = s.Store.InvalidateInstanceCache(req.SourceTokenID)
	}
	writeJSON(w, http.StatusOK, resp)
}
