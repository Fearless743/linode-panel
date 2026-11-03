package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"linode-panel/internal/agenthub"
	"linode-panel/internal/linode"
	"linode-panel/internal/sshrun"
	"linode-panel/internal/store"
)

// sshProbeTimeout bounds one TCP connect to the instance's SSH port.
const sshProbeTimeout = 3 * time.Second

// rebuildPollTimeout bounds waiting for the instance to reach a status.
const rebuildPollTimeout = 10 * time.Minute

// run executes the rebuild sequence for t, appending each step to t.Log.
//
// Sequence (fails fast; every step logs before running):
//  0. read the old instance (region/type/image for the rebuild)
//  1. shutdown the old instance
//  2. delete the old instance
//  3. create a fresh instance (same region/type/image, booted) with a
//     fresh one-off SSH key
//  4. poll until the new instance is running (3s interval, 10m cap)
//  5. run the boot script over SSH (5m timeout)
//  6. probe the SSH port; task succeeds only when the probe connects
func (m *Manager) run(t *Task, pat string) {
	ctx := context.Background()
	oldID := t.InstanceID

	// Each rebuild uses the requesting user's PAT via its own client so a
	// per-user token never leaks across users.
	api := m.api
	if pat != "" {
		api = m.newAPI(pat)
	}

	old, err := api.GetInstance(ctx, oldID)
	// missing means the instance is already gone upstream (404): there is
	// nothing to shut down or delete, so the replacement is created directly
	// from the panel's cached spec.
	missing := false
	if err != nil {
		cached, ok := m.oldInstanceFallback(oldID, err)
		if !ok {
			m.fail(t, fmt.Errorf("读取旧实例失败: %w", err))
			return
		}
		old = cached
		missing = true
		m.log(t, "step 0: old instance id=%d no longer exists (404); recreating from cached spec region=%s type=%s image=%s label=%s",
			oldID, old.Region, old.Type, old.Image, old.Label)
	} else {
		m.log(t, "step 0: old instance id=%d region=%s type=%s image=%s", oldID, old.Region, old.Type, old.Image)
	}
	oldIP := ""
	if len(old.IPv4) > 0 {
		oldIP = old.IPv4[0]
	}
	m.setOldIP(t, oldIP)

	if !missing {
		m.log(t, "step 1: shutdown old instance %d", oldID)
		if err := api.Shutdown(ctx, oldID); err != nil {
			m.fail(t, fmt.Errorf("关机旧实例失败: %w", err))
			return
		}

		m.log(t, "step 2: delete old instance %d", oldID)
		if err := api.DeleteInstance(ctx, oldID); err != nil {
			m.fail(t, fmt.Errorf("删除旧实例失败: %w", err))
			return
		}
	} else {
		m.log(t, "step 1-2: skip shutdown/delete of old instance %d (already gone)", oldID)
	}

	privPEM, pubKey, err := sshrun.GenerateKey()
	if err != nil {
		m.fail(t, fmt.Errorf("生成 SSH 密钥失败: %w", err))
		return
	}
	booted := true
	createOpts := linode.CreateOptions{
		Region:         old.Region,
		Type:           old.Type,
		Image:          old.Image,
		Label:          old.Label,
		Tags:           old.Tags,
		RootPass:       t.rootPass,
		AuthorizedKeys: []string{pubKey},
		Booted:         &booted,
	}
	m.log(t, "step 3: create new instance (region=%s type=%s image=%s label=%s)", old.Region, old.Type, old.Image, old.Label)
	newInst, err := api.CreateInstance(ctx, createOpts)
	if err != nil {
		m.fail(t, fmt.Errorf("创建新实例失败: %w", err))
		return
	}
	newID := newInst.ID
	m.setNewInstanceID(t, newID)
	// The old instance is gone; the replacement inherits the panel-side
	// configuration because the cache row keeps its stable id and only its
	// Linode instance id changes.
	if m.RepointCache != nil {
		m.RepointCache(oldID, newID)
	}

	m.log(t, "step 4: wait for instance %d to be running", newID)
	if err := m.waitStatus(t, api, ctx, newID, "running"); err != nil {
		m.fail(t, err)
		return
	}

	m.log(t, "step 5: run boot script via SSH")
	ips, err := api.GetInstanceIPs(ctx, newID)
	if err != nil {
		m.fail(t, fmt.Errorf("获取新实例 IP 失败: %w", err))
		return
	}
	host := ips.PublicIPv4()
	if host == "" {
		host = ips.SLAACv6()
	}
	if host == "" {
		m.fail(t, fmt.Errorf("新实例没有可用的公网 IP"))
		return
	}
	m.mu.Lock()
	sshPort := m.sshPort
	m.mu.Unlock()
	res, runErr := m.runBootScript(t, ctx, host, sshPort, privPEM, t.Script)
	if runErr != nil {
		m.fail(t, fmt.Errorf("SSH 执行开机脚本失败: %w", runErr))
		return
	}
	m.log(t, "  ssh exit=%d", res.Exit)
	if res.Exit != 0 {
		detail := res.Stderr
		if detail == "" {
			detail = res.Stdout
		}
		m.fail(t, fmt.Errorf("开机脚本退出码 %d: %s", res.Exit, detail))
		return
	}
	if res.Stderr != "" {
		m.log(t, "  stderr: %s", res.Stderr)
	}
	m.log(t, "step 6: probe SSH port on %s", host)

	if err := m.ProbeFn(ctx, host); err != nil {
		m.fail(t, fmt.Errorf("SSH 端口探测失败: %w", err))
		return
	}

	m.succeed(t, host)
}

// runBootScript runs the boot script over SSH, retrying connection failures
// (dial or handshake: the instance is up but sshd is not listening yet) up to
// m.BootScriptAttempts times with m.BootScriptRetryDelay between attempts.
// Errors after the session started are returned immediately: a partially
// applied script may not be safe to re-run.
func (m *Manager) runBootScript(t *Task, ctx context.Context, host string, port int, privPEM []byte, script string) (sshrun.Result, error) {
	attempts := m.BootScriptAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := m.BootScriptRetryDelay
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err := m.RunFn(ctx, sshrun.Options{
			Host:               host,
			Port:               port,
			PrivateKey:         privPEM,
			InsecureSkipVerify: true,
			Timeout:            5 * time.Minute,
		}, script)
		if err == nil {
			return res, nil
		}
		if !isSSHConnectError(err) {
			return res, err
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		m.log(t, "step 5: SSH 连接失败（%v），%s 后重试（第 %d/%d 次）", err, delay, attempt+1, attempts)
		select {
		case <-ctx.Done():
			return res, lastErr
		case <-time.After(delay):
		}
	}
	return sshrun.Result{}, fmt.Errorf("SSH 连接失败（已尝试 %d 次）: %w", attempts, lastErr)
}

// oldInstanceFallback resolves the spec to recreate when GetInstance reports
// the old instance as missing upstream. Only a 404 is tolerated (the instance
// was already deleted); other errors still fail the rebuild. The cached row
// must carry region/type/image, otherwise no replacement can be created.
func (m *Manager) oldInstanceFallback(id int, err error) (linode.Instance, bool) {
	var apiErr *linode.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return linode.Instance{}, false
	}
	if m.ResolveInstance == nil {
		return linode.Instance{}, false
	}
	cached, ok := m.ResolveInstance(id)
	if !ok || cached.Region == "" || cached.Type == "" || cached.Image == "" {
		return linode.Instance{}, false
	}
	return cached, true
}

// waitStatus polls api.GetInstance until the instance reaches want or the
// poll timeout elapses.
func (m *Manager) waitStatus(t *Task, api LinodeAPI, ctx context.Context, id int, want string) error {
	interval := m.PollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	timeout := m.PollTimeout
	if timeout <= 0 {
		timeout = rebuildPollTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		inst, err := api.GetInstance(ctx, id)
		if err == nil && inst.Status == want {
			return nil
		}
		if err != nil {
			m.log(t, "  poll: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("等待实例 %d 变为 %s 超时", id, want)
		case <-ticker.C:
		}
	}
}

// probeHost is the default ProbeFn: a TCP dial to host at the Manager's
// configured SSH port, bounded by sshProbeTimeout.
func (m *Manager) probeHost(ctx context.Context, host string) error {
	m.mu.Lock()
	port := m.sshPort
	m.mu.Unlock()
	return probe(ctx, host, port)
}

// probe attempts a TCP connection to host:port within sshProbeTimeout.
func probe(ctx context.Context, host string, port int) error {
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, sshProbeTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// HTTP handlers for the rebuild and task routes.

type rebuildRequest struct {
	Script  string `json:"script"`
	TokenID int    `json:"token_id"`
	// RootPassword optionally overrides the instance's saved root password;
	// when non-empty it is saved as the instance's configuration first.
	RootPassword string `json:"root_password"`
}

type rebuildResponse struct {
	TaskID string `json:"task_id"`
}

func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req rebuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	// 脚本解析顺序：请求体显式提供 → 该实例已配置的脚本 → 内置默认脚本。
	script := req.Script
	if script == "" {
		script = s.rebuildScriptFor(id)
	}
	if script == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "开机脚本不能为空"})
		return
	}
	// 显式提供的脚本固化到该实例（成为该实例的配置）。
	if req.Script != "" {
		if err := s.Store.SetInstanceScript(id, req.Script); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存实例脚本失败"})
			return
		}
	}
	// root 密码同样固化到该实例：请求体显式提供 → 保存并使用；否则使用
	// 该实例已保存的密码（没有则仅用一次性 SSH 密钥登录）。
	if req.RootPassword != "" {
		if err := s.Store.SetInstanceRootPassword(id, req.RootPassword); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存 root 密码失败"})
			return
		}
	}
	rootPass := s.rootPasswordFor(id)
	// 执行通道：显式 ?via_agent=1/0 优先；否则跟随账号绑定的 Agent。
	// 目标账号来自请求体 token_id（与直发路径一致）。
	user, _ := UserFrom(r.Context())
	tokenID := ""
	if req.TokenID > 0 {
		tokenID = strconv.Itoa(req.TokenID)
	}
	if s.viaAgentForRequest(user, tokenID, r.URL.Query().Get("via_agent")) {
		params := map[string]any{
			"id":            id,
			"script":        script,
			"root_password": rootPass,
		}
		// If the instance is already gone upstream, the agent cannot read its
		// region/type/image. Carry the panel's cached spec so it can create the
		// replacement directly instead of failing on the 404.
		if old, ok := s.Store.CachedInstance(id); ok {
			params["old_instance"] = old
		}
		cmdID, ok := s.enqueueAgentCommandForToken(w, r, tokenID, agenthub.CmdReboot, params)
		if !ok {
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"command_id": cmdID})
		return
	}
	pat, err := s.resolveToken(user, tokenID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	taskID, err := s.Tasks.Start(id, script, rootPass, pat, s.tokenLabel(user, tokenID))
	if err != nil {
		if errors.Is(err, ErrBusy) {
			writeJSON(w, http.StatusConflict, errorResponse{Error: "该实例已有重建任务进行中"})
			return
		}
		writeError(w, err)
		return
	}
	// Rebuild replaces the instance with a new one; drop the token's cached
	// snapshot so the list refetches instead of showing the deleted instance.
	if tid := s.tokenIDFor(user, tokenID); tid > 0 {
		if err := s.Store.InvalidateInstanceCache(tid); err != nil {
			slog.Warn("invalidate instance cache failed", "token_id", tid, "error", err)
		}
	}
	writeJSON(w, http.StatusAccepted, rebuildResponse{TaskID: taskID})
}

// tokenLabel returns the display label for user's selected token, or "PAT"
// when the token cannot be found.
func (s *Server) tokenLabel(user store.User, tokenID string) string {
	for _, t := range s.Store.TokensOf(user.ID) {
		if tokenID == "" || strconv.Itoa(t.ID) == tokenID {
			return t.Label
		}
	}
	return "PAT"
}

// rootPasswordFor returns the instance's saved root password, or "" when none
// is configured (the rebuild then authenticates with the one-off SSH key only).
func (s *Server) rootPasswordFor(id int) string {
	pass, _ := s.Store.InstanceRootPassword(id)
	return pass
}

// handleRebuildTasks lists tasks (newest first, logs reversed).
func (s *Server) handleRebuildTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string][]Task{"tasks": s.Tasks.List()})
}

// handleRebuildTask returns one task.
func (s *Server) handleRebuildTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.Tasks.Get(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "任务不存在"})
			return
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// bootScriptRequest is the request body for saving an instance's script.
type bootScriptRequest struct {
	Script string `json:"script"`
}

// handleGetInstanceScript returns the script effective for one instance:
// its configured script when set, otherwise the built-in default template.
// last_run is the state of the most recent create-time execution (absent when
// the script has not been run by the panel since startup).
func (s *Server) handleGetInstanceScript(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	lastRun, hasRun := s.bootScriptRun(id)
	if script, configured := s.Store.InstanceScript(id); configured && script != "" {
		resp := map[string]any{"script": script, "configured": true}
		if hasRun {
			resp["last_run"] = lastRun
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp := map[string]any{"script": DefaultBootScript, "configured": false}
	if hasRun {
		resp["last_run"] = lastRun
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePutInstanceScript persists an instance's own boot script.
func (s *Server) handlePutInstanceScript(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req bootScriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if req.Script == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "脚本不能为空"})
		return
	}
	if err := s.Store.SetInstanceScript(id, req.Script); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存实例脚本失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleDeleteInstanceScript removes an instance's script so it uses the
// built-in default template again.
func (s *Server) handleDeleteInstanceScript(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteInstanceScript(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "删除实例脚本失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleInstanceScriptStatus lists the ids of instances that have a custom
// script configured, so the UI can mark them.
func (s *Server) handleInstanceScriptStatus(w http.ResponseWriter, r *http.Request) {
	ids := s.Store.InstanceScriptIDs()
	if ids == nil {
		ids = []int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": ids})
}

// rootPasswordRequest is the request body for saving an instance's root
// password.
type rootPasswordRequest struct {
	Password string `json:"password"`
}

// handleGetInstanceRootPassword reports whether a root password is saved for
// the instance. The password itself is never returned.
func (s *Server) handleGetInstanceRootPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": s.Store.InstanceRootPasswordConfigured(id),
	})
}

// handlePutInstanceRootPassword saves the root password used when the instance
// is rebuilt.
func (s *Server) handlePutInstanceRootPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req rootPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "密码不能为空"})
		return
	}
	if err := s.Store.SetInstanceRootPassword(id, req.Password); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存 root 密码失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleDeleteInstanceRootPassword removes the saved root password so a later
// rebuild authenticates with the one-off SSH key only.
func (s *Server) handleDeleteInstanceRootPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteInstanceRootPassword(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "删除 root 密码失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}
