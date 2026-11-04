package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// ---------- 当前登录用户的 PAT（账号）管理 ----------

// tokenJSONFor renders a token for the UI, attaching the name of the agent
// bound to the account (if any) so the account-management page can show it.
func (s *Server) tokenJSONFor(t store.Token) tokenJSON {
	js := tokenJSON{ID: t.ID, Label: t.Label, Masked: store.Masked(t.Token), AgentID: t.AgentID}
	if t.AgentID > 0 {
		if a, ok := s.Hub.Agent(t.AgentID); ok {
			js.AgentName = a.Name
		}
	}
	return js
}

// handleListMyTokens returns the calling user's PATs (masked), each with the
// agent bound to the account and a freshly-probed account health snapshot
// (status / detail / balance / balance_uninvoiced / checked_at).
func (s *Server) handleListMyTokens(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	toks := s.Store.TokensOf(user.ID)
	out := make([]tokenJSON, len(toks))
	for i, t := range toks {
		out[i] = s.tokenJSONFor(t)
	}
	// 并发探测所有账号（纯只读，PAT 缓存客户端内部线程安全）。
	var wg sync.WaitGroup
	wg.Add(len(toks))
	for i, t := range toks {
		go func(idx int, tok store.Token) {
			defer wg.Done()
			h := s.probeAccountHealthFull(r.Context(), tok.Token)
			js := &out[idx]
			js.HealthStatus = h.Status
			js.HealthDetail = h.Detail
			js.HealthCheckedAt = h.CheckedAt
			js.Balance = h.Balance
			js.BalanceUninvoiced = h.Unbilled
		}(i, t)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// handleAddMyToken binds a new PAT (a Linode account) to the calling user.
func (s *Server) handleAddMyToken(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	var req struct {
		Label string `json:"label"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Label == "" || req.Token == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "名称和 Token 不能为空"})
		return
	}
	t, err := s.Store.AddToken(user.ID, req.Label, req.Token)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.tokenJSONFor(t))
}

// handleUpdateMyToken updates the label (and optionally the agent binding) of
// one of the calling user's PATs.
func (s *Server) handleUpdateMyToken(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "令牌 ID 无效"})
		return
	}
	var req struct {
		Label   *string `json:"label"`
		AgentID *int    `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	// Update label.
	if req.Label != nil {
		label := strings.TrimSpace(*req.Label)
		if label == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "名称不能为空"})
			return
		}
		if err := s.Store.UpdateTokenLabel(user.ID, tid, label); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}
	// Update agent binding (0 = unbind).
	if req.AgentID != nil {
		aid := *req.AgentID
		if aid < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的 agent ID"})
			return
		}
		if aid > 0 {
			if _, ok := s.Hub.Agent(aid); !ok {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "agent 不存在"})
				return
			}
		}
		if err := s.Store.SetTokenAgent(user.ID, tid, aid); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}
	// Return the updated token.
	toks := s.Store.TokensOf(user.ID)
	for _, t := range toks {
		if t.ID == tid {
			writeJSON(w, http.StatusOK, s.tokenJSONFor(t))
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errorResponse{Error: "令牌不存在"})
}

// handleDeleteMyToken removes one of the calling user's PATs.
func (s *Server) handleDeleteMyToken(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "令牌 ID 无效"})
		return
	}
	if err := s.Store.DeleteToken(user.ID, tid); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleAccountAgents lists registered agents (id + name only) so any logged-in
// user can pick which agent should run operations for their own accounts.
func (s *Server) handleAccountAgents(w http.ResponseWriter, r *http.Request) {
	agents := s.Hub.Agents()
	out := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		out = append(out, map[string]any{
			"id":    a.ID,
			"name":  a.Name,
			"local": a.ID == s.localAgentID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

// handleSetTokenAgent binds (agent_id > 0) or unbinds (agent_id == 0) the
// agent that executes this account's operations (power, rebuild, delete) by
// default when the request does not override via_agent/agent_id.
func (s *Server) handleSetTokenAgent(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	tid, err := strconv.Atoi(r.PathValue("tid"))
	if err != nil || tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "令牌 ID 无效"})
		return
	}
	var req struct {
		AgentID int `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.AgentID < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的 agent ID"})
		return
	}
	if req.AgentID > 0 {
		if _, ok := s.Hub.Agent(req.AgentID); !ok {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "agent 不存在"})
			return
		}
	}
	if err := s.Store.SetTokenAgent(user.ID, tid, req.AgentID); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// ---------- 创建实例（用指定账号的 PAT） ----------

// handleAccountOptions returns the regions/types/images available to the
// account identified by token_id, so the UI can populate create-instance
// selectors. Results are cached in the database per token; pass ?refresh=1
// to force a re-fetch from the Linode API.
func (s *Server) handleAccountOptions(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	tokenIDStr := r.URL.Query().Get("token_id")
	pat, err := s.resolveToken(user, tokenIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	forceRefresh := r.URL.Query().Get("refresh") == "1"
	if tid := s.tokenIDFor(user, tokenIDStr); tid > 0 && !forceRefresh {
		if snap := s.Store.CachedAccountOptions(tid); snap != nil {
			// 缓存可能来自旧版本（写入时未排序），读出后统一排序再返回，
			// 保证命中缓存与强制刷新两种路径的顺序一致。
			sortAccountOptions(snap.Regions, snap.Types, snap.Images)
			writeJSON(w, http.StatusOK, map[string]any{
				"regions": orEmptyRegions(snap.Regions),
				"types":   orEmptyTypes(snap.Types),
				"images":  orEmptyImages(snap.Images),
			})
			return
		}
	}
	client := s.clientFor(pat)
	ctx := r.Context()
	regions, err := client.ListRegions(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "查询地区失败: " + err.Error()})
		return
	}
	types, err := client.ListTypes(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "查询机型失败: " + err.Error()})
		return
	}
	images, err := client.ListImages(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "查询镜像失败: " + err.Error()})
		return
	}
	// regions 按国家(country)升序排序，同国家内再按 id 升序保证顺序稳定；
	// types/images 按 id 字母升序排序。均保证前端下拉框顺序稳定。
	sortAccountOptions(regions, types, images)
	// Persist the freshly fetched snapshot for future reads.
	if tid := s.tokenIDFor(user, tokenIDStr); tid > 0 {
		if err := s.Store.CacheAccountOptions(tid, &store.AccountOptionsSnapshot{
			Regions: regions, Types: types, Images: images,
		}); err != nil {
			slog.Warn("cache account options failed", "token_id", tid, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"regions": orEmptyRegions(regions),
		"types":   orEmptyTypes(types),
		"images":  orEmptyImages(images),
	})
}

// sortAccountOptions 就地对 options 三个列表排序：
// regions 按 country 升序、同 country 内按 id 升序；types/images 按 id 升序。
// 缓存读取与实时拉取两条路径都调用它，保证返回顺序一致。
func sortAccountOptions(regions []linode.Region, types []linode.LinodeType, images []linode.Image) {
	slices.SortFunc(regions, func(a, b linode.Region) int {
		if c := strings.Compare(a.Country, b.Country); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(types, func(a, b linode.LinodeType) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(images, func(a, b linode.Image) int { return strings.Compare(a.ID, b.ID) })
}

type createInstanceRequest struct {
	TokenID  int    `json:"token_id"`
	Label    string `json:"label"`
	Region   string `json:"region"`
	Type     string `json:"type"`
	Image    string `json:"image"`
	RootPass string `json:"root_pass"`
	// Script is an optional boot script saved as the instance's configured
	// script (used when the instance is rebuilt). Empty means the instance
	// falls back to the built-in default template on rebuild.
	Script string `json:"script"`
}

// handleCreateInstance deploys a new Linode using the account's PAT.
func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	var req createInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Label == "" || req.Region == "" || req.Type == "" || req.Image == "" || req.RootPass == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "名称、地区、机型、镜像和 root 密码均不能为空"})
		return
	}
	if req.TokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请选择账号（PAT）"})
		return
	}
	pat, err := s.resolveToken(user, strconv.Itoa(req.TokenID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	booted := true
	// 可选开机脚本：创建后由后台协程在实例上通过 root 密码 SSH 执行，同时
	// 保存为该实例配置（之后重建也会执行）。
	script := strings.TrimSpace(req.Script)
	createOpts := linode.CreateOptions{
		Region:   req.Region,
		Type:     req.Type,
		Image:    req.Image,
		Label:    req.Label,
		RootPass: req.RootPass,
		Booted:   &booted,
	}
	inst, err := s.clientFor(pat).CreateInstance(r.Context(), createOpts)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "创建实例失败: " + err.Error()})
		return
	}
	// 默认开放所有端口：把「允许全部」Cloud Firewall 绑定到新实例。
	// 失败不阻塞创建（可能因 PAT 缺少 firewall 权限或实例尚未就绪），仅记录日志。
	if err := s.clientFor(pat).AttachAllowAllFirewall(r.Context(), req.Region, inst.ID); err != nil {
		slog.Warn("开放所有端口防火墙绑定失败（实例已创建，继续）", "instance_id", inst.ID, "err", err)
	}
	// 保存 root 密码为该实例的配置：删机重建时用同一密码创建新实例。
	if err := s.Store.SetInstanceRootPassword(inst.ID, req.RootPass); err != nil {
		slog.Warn("保存 root 密码失败（实例已创建，继续）", "instance_id", inst.ID, "err", err)
	}
	// 保存为该实例配置的脚本（重建时再次执行）。实例已创建成功，保存失败
	// 仅记录日志，不影响创建结果。
	if script != "" {
		if err := s.Store.SetInstanceScript(inst.ID, script); err != nil {
			slog.Warn("保存开机脚本失败（实例已创建，继续）", "instance_id", inst.ID, "err", err)
		}
		// 后台执行：创建请求立即返回，脚本在实例启动后通过 root 密码 SSH 运行。
		s.startBootScript(inst.ID, pat, req.RootPass, script)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"instance": instanceJSON{
			ID:         inst.ID,
			Label:      inst.Label,
			Status:     inst.Status,
			Region:     inst.Region,
			Type:       inst.Type,
			Image:      inst.Image,
			IPv4:       orEmpty(inst.IPv4),
			IPv6:       inst.IPv6,
			SSHCommand: s.sshCommand(s.listHost(inst)),
			Tags:       orEmpty(inst.Tags),
		},
	})
}

func orEmptyRegions(in []linode.Region) []linode.Region {
	if in == nil {
		return []linode.Region{}
	}
	return in
}

func orEmptyTypes(in []linode.LinodeType) []linode.LinodeType {
	if in == nil {
		return []linode.LinodeType{}
	}
	return in
}

func orEmptyImages(in []linode.Image) []linode.Image {
	if in == nil {
		return []linode.Image{}
	}
	return in
}
