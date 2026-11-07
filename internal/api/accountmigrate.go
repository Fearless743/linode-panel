package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"linode-panel/internal/linode"
)

type accountMigrateRequest struct {
	SourceTokenID int  `json:"source_token_id"`
	TargetTokenID int  `json:"target_token_id"`
	DeleteSource  bool `json:"delete_source"`
}

type accountMigrateResult struct {
	SourceInstanceID int    `json:"source_instance_id"`
	TargetInstanceID int    `json:"target_instance_id,omitempty"`
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
}

type accountMigrateResponse struct {
	Migrated int                     `json:"migrated"`
	Failed   int                     `json:"failed"`
	Results  []accountMigrateResult  `json:"results"`
}

// handleMigrateAccount 把一个账号（PAT）的全部实例与面板配置迁移到另一个账号：
// 对源账号缓存的每个实例，在目标账号用相同 region/type/image/label + 已保存的
// root 密码与开机脚本创建新实例，把稳定缓存行（脚本、root 密码、自动换 IP、
// 替换配置）迁移到新实例；可选删除源实例。账号级双机配置与 Agent 绑定
// 也会同步。
func (s *Server) handleMigrateAccount(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	var req accountMigrateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.SourceTokenID <= 0 || req.TargetTokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择源账号与目标账号"})
		return
	}
	if req.SourceTokenID == req.TargetTokenID {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "源账号与目标账号不能相同"})
		return
	}
	// 校验归属。
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
	resp := accountMigrateResponse{Results: []accountMigrateResult{}}
	ctx := r.Context()

	for _, row := range rows {
		spec := row.Instance
		res := accountMigrateResult{SourceInstanceID: spec.ID}
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
		opts := linode.CreateOptions{
			Region:   spec.Region,
			Type:     spec.Type,
			Image:    spec.Image,
			Label:    label,
			Tags:     spec.Tags,
			RootPass: rootPass,
			Booted:   &booted,
		}
		newInst, err := s.clientFor(tgtPAT).CreateInstance(ctx, opts)
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
		if err := s.clientFor(tgtPAT).AttachAllowAllFirewall(ctx, region, newInst.ID); err != nil {
			// 尽力而为。
		}
		// root 密码与开机脚本已挂在源稳定缓存行上，MigrateInstanceCache 会把
		// 同一行（含配置）迁移到目标账号/新实例，这里不必再写一次，避免
		// 产生多余的 anchor 行再合并。
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
			if err := s.clientFor(srcPAT).DeleteInstance(ctx, spec.ID); err != nil {
				// 上游删除失败不阻塞迁移结果，仅记录。
			}
			_ = s.Store.DeleteInstanceCache(req.SourceTokenID, spec.ID)
		}
		res.TargetInstanceID = newInst.ID
		res.OK = true
		resp.Results = append(resp.Results, res)
		resp.Migrated++
	}

	// 账号级双机配置：源有、目标无时复制（实例指向清空，用户重新启用）。
	if d, ok := s.Store.DualConfigOf(req.SourceTokenID); ok {
		if _, ok2 := s.Store.DualConfigOf(req.TargetTokenID); !ok2 {
			nd := d
			nd.TokenID = req.TargetTokenID
			nd.UserID = user.ID
			nd.ActiveInstanceID = 0
			nd.StandbyInstanceID = 0
			nd.State = "idle"
			nd.FailCount = 0
			rp, _ := s.Store.DualRootPassword(req.SourceTokenID)
			_ = s.Store.SetDualConfig(nd, rp)
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
		for _, t := range s.Store.TokensOf(user.ID) {
			if t.ID == req.TargetTokenID && t.AgentID == 0 {
				_ = s.Store.SetTokenAgent(user.ID, req.TargetTokenID, srcAgent)
			}
		}
	}
	_ = s.Store.InvalidateInstanceCache(req.TargetTokenID)
	if req.DeleteSource {
		_ = s.Store.InvalidateInstanceCache(req.SourceTokenID)
	}
	writeJSON(w, http.StatusOK, resp)
}
