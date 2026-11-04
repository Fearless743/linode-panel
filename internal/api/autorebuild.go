package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// DefaultBlockCheckAPI is the China-side IP-reachability probe used by the
// auto-rebuild monitor (same service the aws_auto_changeip tool uses).
const DefaultBlockCheckAPI = "https://api.24kplus.com/ipcheck"

// DefaultAutoRebuild* are the per-instance defaults applied when the UI leaves
// a field empty.
const (
	DefaultAutoRebuildThreshold = 3
	DefaultAutoRebuildInterval  = 5 * time.Minute
)

// ---------- 被墙检测 ----------

// blockCheckResult is the outcome of one reachability probe of an instance's
// public IP as seen from inside China.
type blockCheckResult struct {
	blocked bool   // 判定为被墙（探测端口不通）
	detail  string // 结果描述
	err     error  // 检测本身失败（接口超时等），既不算被墙也不算正常
}

// apiBlockCheck probes ip:port via the China-side ipcheck API.
func (m *AutoRebuildMonitor) apiBlockCheck(ctx context.Context, ip string, port int) blockCheckResult {
	// _t 为长随机数（模拟网页端 Math.random()），太短会被 403。
	u := fmt.Sprintf("%s?host=%s&port=%d&_t=%s",
		m.checkAPI, url.QueryEscape(ip), port,
		strconv.FormatFloat(rand.Float64(), 'f', -1, 64))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return blockCheckResult{err: err}
	}
	req.Header.Set("Origin", "https://www.idcoffer.com")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,zh-TW;q=0.8,zh-HK;q=0.7,en-US;q=0.6,en;q=0.5")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	resp, err := m.client.Do(req)
	if err != nil {
		return blockCheckResult{err: fmt.Errorf("请求检测接口: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return blockCheckResult{err: fmt.Errorf("检测接口返回 HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return blockCheckResult{err: fmt.Errorf("读取检测接口响应: %w", err)}
	}
	return parseBlockCheck(ip, port, body)
}

// parseBlockCheck parses the ipcheck response. Only the TCP verdict decides
// "被墙" (a reachable ping with a dead port is still blocked); ping is kept for
// the log. IP is echoed from the request because the API reports 0.0.0.0 for a
// blocked host.
func parseBlockCheck(ip string, port int, body []byte) blockCheckResult {
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Ping bool   `json:"ping"`
			TCP  bool   `json:"tcp"`
			IP   string `json:"ip"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return blockCheckResult{err: fmt.Errorf("解析检测接口响应: %w", err)}
	}
	if out.Code != 1 {
		return blockCheckResult{err: fmt.Errorf("接口异常: code=%d %s", out.Code, out.Message)}
	}
	if out.Data.TCP {
		return blockCheckResult{detail: fmt.Sprintf("国内探测: 端口 %s:%d 连通", ip, port)}
	}
	return blockCheckResult{
		blocked: true,
		detail:  fmt.Sprintf("国内探测: 端口 %s:%d 不通（ping=%v），疑似被墙", ip, port, out.Data.Ping),
	}
}

// ---------- 监控循环 ----------

// AutoRebuildMonitor periodically probes every instance that has auto-rebuild
// enabled; once an instance is blocked Threshold times in a row it triggers a
// delete+recreate rebuild so the instance comes back with a fresh IP.
type AutoRebuildMonitor struct {
	srv      *Server
	checkAPI string
	tick     time.Duration
	client   *http.Client
	log      *slog.Logger

	// checkFn is the probe used per instance. Injectable for tests; defaults
	// to apiBlockCheck.
	checkFn func(ctx context.Context, ip string, port int) blockCheckResult
}

// StartAutoRebuildMonitor launches the background monitor, returning it. The
// loop stops when ctx is cancelled. tick is the base cadence; each instance is
// only actually probed once its own IntervalSec has elapsed since the last
// check (so a slow tick with per-instance intervals is fine).
func (s *Server) StartAutoRebuildMonitor(ctx context.Context, tick time.Duration) *AutoRebuildMonitor {
	if tick <= 0 {
		tick = time.Minute
	}
	api := s.AutoRebuildCheckAPI
	if api == "" {
		api = DefaultBlockCheckAPI
	}
	m := &AutoRebuildMonitor{
		srv:      s,
		checkAPI: api,
		tick:     tick,
		client:   &http.Client{Timeout: 20 * time.Second},
		log:      slog.Default(),
	}
	m.checkFn = m.apiBlockCheck
	s.monitor = m
	go m.run(ctx)
	return m
}

func (m *AutoRebuildMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
	// Probe once shortly after startup so a restart recovers promptly.
	m.tickOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tickOnce(ctx)
		}
	}
}

// tickOnce probes every enabled instance whose interval has elapsed.
func (m *AutoRebuildMonitor) tickOnce(ctx context.Context) {
	for _, cfg := range m.srv.Store.AutoRebuilds() {
		if !cfg.Enabled {
			continue
		}
		if !m.due(cfg) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		m.checkInstance(ctx, cfg)
	}
}

// due reports whether cfg is ready for another probe.
func (m *AutoRebuildMonitor) due(cfg store.AutoRebuild) bool {
	if cfg.LastCheckAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, cfg.LastCheckAt)
	if err != nil {
		return true
	}
	gap := time.Duration(cfg.IntervalSec) * time.Second
	if gap <= 0 {
		gap = DefaultAutoRebuildInterval
	}
	return time.Since(last) >= gap
}

func (m *AutoRebuildMonitor) now() string { return time.Now().UTC().Format(time.RFC3339) }

// checkInstance probes one instance and, when the failure threshold is hit,
// triggers a rebuild.
func (m *AutoRebuildMonitor) checkInstance(ctx context.Context, cfg store.AutoRebuild) {
	id := cfg.InstanceID
	// 双机轮询接管的实例跳过，避免重复删机。
	if m.srv.isDualManaged(id) {
		return
	}
	// A rebuild is replacing this instance. Between the old instance's delete
	// and the cache row's re-point, GetInstance would return 404; that must not
	// be mistaken for an out-of-band deletion that drops the configuration.
	if m.srv.Tasks != nil && m.srv.Tasks.Busy(id) {
		_ = m.srv.Store.UpdateAutoRebuildState(id, cfg.FailCount, "rebuilding",
			"重建任务进行中，跳过检测", m.now(), "", "")
		return
	}
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		m.log.Warn("自动换IP: 取 PAT 失败", "instance_id", id, "error", err)
		return
	}
	inst, err := m.srv.clientFor(pat).GetInstance(ctx, id)
	if err != nil {
		var apiErr *linode.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			// The instance is gone (deleted out of band): drop the config.
			m.log.Info("自动换IP: 实例不存在，移除配置", "instance_id", id)
			_ = m.srv.Store.DeleteAutoRebuild(id)
			return
		}
		m.log.Warn("自动换IP: 读取实例失败", "instance_id", id, "error", err)
		_ = m.srv.Store.UpdateAutoRebuildState(id, cfg.FailCount, "error",
			"读取实例失败: "+err.Error(), m.now(), "", "")
		return
	}
	if inst.Status != "running" {
		// A stopped instance's port is unreachable by definition; skip so we
		// do not mistake "offline" for "blocked".
		_ = m.srv.Store.UpdateAutoRebuildState(id, cfg.FailCount, "idle",
			"实例未运行（"+inst.Status+"），跳过检测", m.now(), "", "")
		return
	}
	ip := ""
	if len(inst.IPv4) > 0 {
		ip = inst.IPv4[0]
	}
	if ip == "" {
		_ = m.srv.Store.UpdateAutoRebuildState(id, cfg.FailCount, "idle",
			"实例没有公网 IPv4，跳过检测", m.now(), "", "")
		return
	}

	res := m.checkFn(ctx, ip, cfg.Port)
	switch {
	case res.err != nil:
		// A probe failure is not evidence of blocking; do not count it.
		m.log.Warn("自动换IP: 检测出错（不计入失败）", "instance_id", id, "ip", ip, "error", res.err)
		_ = m.srv.Store.UpdateAutoRebuildState(id, cfg.FailCount, "error",
			"检测出错: "+res.err.Error(), m.now(), "", "")
	case res.blocked:
		fails := cfg.FailCount + 1
		threshold := cfg.Threshold
		if threshold <= 0 {
			threshold = DefaultAutoRebuildThreshold
		}
		m.log.Info("自动换IP: 疑似被墙", "instance_id", id, "ip", ip, "fails", fails, "threshold", threshold, "detail", res.detail)
		if fails >= threshold {
			m.triggerRebuild(ctx, cfg, ip, res.detail, fails)
			return
		}
		_ = m.srv.Store.UpdateAutoRebuildState(id, fails, "blocked", res.detail, m.now(), "", "")
	default:
		if cfg.FailCount > 0 {
			m.log.Info("自动换IP: 恢复正常", "instance_id", id, "ip", ip)
		}
		_ = m.srv.Store.UpdateAutoRebuildState(id, 0, "ok", res.detail, m.now(), "", "")
	}
}

// triggerRebuild starts a delete+recreate rebuild for a blocked instance and
// follows the task to re-point the config at the new instance id.
func (m *AutoRebuildMonitor) triggerRebuild(ctx context.Context, cfg store.AutoRebuild, ip, detail string, fails int) {
	id := cfg.InstanceID
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		m.log.Warn("自动换IP: 取 PAT 失败，暂不重建", "instance_id", id, "error", err)
		return
	}
	taskID, err := m.srv.Tasks.Start(id, m.srv.rebuildScriptFor(id), m.srv.rootPasswordFor(id), pat, "自动换IP")
	if err != nil {
		if errors.Is(err, ErrBusy) {
			m.log.Info("自动换IP: 已有重建任务进行中，等待下一轮", "instance_id", id)
			// Keep the counter at the threshold so the UI does not show it
			// growing while we wait for the other rebuild to finish.
			if t := cfg.Threshold; t > 0 && fails > t {
				fails = t
			}
			_ = m.srv.Store.UpdateAutoRebuildState(id, fails, "blocked",
				detail+"；另有重建任务进行中，等待重试", m.now(), "", "")
			return
		}
		m.log.Warn("自动换IP: 启动重建失败", "instance_id", id, "error", err)
		_ = m.srv.Store.UpdateAutoRebuildState(id, fails, "error", "启动重建失败: "+err.Error(), m.now(), "", "")
		return
	}
	m.log.Info("自动换IP: 已触发重建", "instance_id", id, "ip", ip, "task_id", taskID)
	_ = m.srv.Store.UpdateAutoRebuildState(id, 0, "rebuilding",
		"被墙，已触发重建换 IP（"+ip+"）", m.now(), m.now(), taskID)
	go m.awaitRebuild(cfg, taskID)
}

// awaitRebuild polls the rebuild task until it finishes. Configuration is
// keyed by the instance's stable cache id, so it follows the replacement
// instance automatically; only the monitor state needs to be updated.
func (m *AutoRebuildMonitor) awaitRebuild(cfg store.AutoRebuild, taskID string) {
	for {
		t, err := m.srv.Tasks.Get(taskID)
		if err != nil {
			return
		}
		if t.Status == StatusRunning {
			time.Sleep(5 * time.Second)
			continue
		}
		// Defensive: the direct rebuild path re-points the cache row as soon
		// as the new instance exists. Do it here too in case this task was
		// executed through an agent that did not report the id.
		if t.NewInstanceID > 0 && t.NewInstanceID != cfg.InstanceID {
			m.srv.repointInstanceCache(cfg.InstanceID, t.NewInstanceID)
		}
		if t.Status == StatusFailed {
			// Keep the config so the monitor retries, but clear the fail
			// counter so it does not rebuild again immediately.
			_ = m.srv.Store.UpdateAutoRebuildStateByCache(cfg.CacheID, 0, "error",
				"重建失败: "+t.Err, m.now(), "", "")
			return
		}
		_ = m.srv.Store.UpdateAutoRebuildStateByCache(cfg.CacheID, 0, "idle",
			"重建完成，等待下一轮检测", m.now(), "", "")
		return
	}
}

// ---------- HTTP handlers ----------

type autoRebuildRequest struct {
	Enabled     bool `json:"enabled"`
	Port        int  `json:"port"`
	Threshold   int  `json:"threshold"`
	IntervalSec int  `json:"interval_seconds"`
	TokenID     int  `json:"token_id"`
}

// handleGetAutoRebuild returns one instance's auto-rebuild config (defaults
// when never configured).
func (s *Server) handleGetAutoRebuild(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if cfg, found := s.Store.AutoRebuild(id); found {
		writeJSON(w, http.StatusOK, cfg)
		return
	}
	writeJSON(w, http.StatusOK, store.AutoRebuild{
		InstanceID:  id,
		Port:        s.sshPort(),
		Threshold:   DefaultAutoRebuildThreshold,
		IntervalSec: int(DefaultAutoRebuildInterval / time.Second),
	})
}

// handlePutAutoRebuild creates or updates an instance's auto-rebuild setting.
func (s *Server) handlePutAutoRebuild(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req autoRebuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求体无效"})
		return
	}
	user, _ := UserFrom(r.Context())

	if req.Port == 0 {
		req.Port = s.sshPort()
	}
	if req.Port < 1 || req.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "端口必须在 1-65535 之间"})
		return
	}
	if req.Threshold == 0 {
		req.Threshold = DefaultAutoRebuildThreshold
	}
	if req.Threshold < 1 || req.Threshold > 20 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "连续失败次数必须在 1-20 之间"})
		return
	}
	if req.IntervalSec == 0 {
		req.IntervalSec = int(DefaultAutoRebuildInterval / time.Second)
	}
	if req.IntervalSec < 30 || req.IntervalSec > 24*3600 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "检测间隔必须在 30 秒到 24 小时之间"})
		return
	}
	// Resolve which PAT this instance is rebuilt with.
	tokenID := ""
	if req.TokenID > 0 {
		tokenID = strconv.Itoa(req.TokenID)
	}
	tid := s.tokenIDFor(user, tokenID)
	if tid <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请先在账号管理中绑定 PAT"})
		return
	}
	if _, err := s.resolveToken(user, strconv.Itoa(tid)); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	cfg := store.AutoRebuild{
		InstanceID:  id,
		UserID:      user.ID,
		TokenID:     tid,
		Enabled:     req.Enabled,
		Port:        req.Port,
		Threshold:   req.Threshold,
		IntervalSec: req.IntervalSec,
	}
	if err := s.Store.SetAutoRebuild(cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "保存自动换IP配置失败"})
		return
	}
	// Immediately probe once when enabling, so the UI shows a fresh status
	// instead of waiting a full interval.
	if req.Enabled && s.monitor != nil {
		if saved, found := s.Store.AutoRebuild(id); found {
			go s.monitor.checkInstance(context.Background(), saved)
		}
	}
	out, _ := s.Store.AutoRebuild(id)
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteAutoRebuild removes an instance's auto-rebuild setting.
func (s *Server) handleDeleteAutoRebuild(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteAutoRebuild(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "删除自动换IP配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleAutoRebuildList lists configured instances so the UI can mark rows.
func (s *Server) handleAutoRebuildList(w http.ResponseWriter, r *http.Request) {
	items := s.Store.AutoRebuilds()
	if items == nil {
		items = []store.AutoRebuild{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"auto_rebuild": items})
}

// handleCheckAutoRebuild probes one instance right now and records the result.
func (s *Server) handleCheckAutoRebuild(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	cfg, found := s.Store.AutoRebuild(id)
	if !found {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "该实例未配置自动换IP"})
		return
	}
	if s.monitor == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "自动换IP监控未启用"})
		return
	}
	s.monitor.checkInstance(r.Context(), cfg)
	out, _ := s.Store.AutoRebuild(id)
	writeJSON(w, http.StatusOK, out)
}

// rebuildScriptFor resolves the boot script used when rebuilding id: its
// configured script when set, otherwise the built-in default template.
func (s *Server) rebuildScriptFor(id int) string {
	if script, ok := s.Store.InstanceScript(id); ok && script != "" {
		return script
	}
	return DefaultBootScript
}
