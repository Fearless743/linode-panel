package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/sshrun"
	"linode-panel/internal/store"
)

// 双机轮询默认参数。
const (
	DefaultDualPort      = 22
	DefaultDualThreshold = 3
	DefaultDualInterval  = 5 * time.Minute
	// MaxStandbyRebuilds bounds the "rebuild until clean" loop so a fully
	// blocked region does not burn money forever.
	MaxStandbyRebuilds = 20
)

// DualMonitor 定期从国内探测双机配置的 active 实例；连续被墙达阈值时轮换到
// standby（开机并执行账号级开机脚本），再把旧 active 删机重建（不执行脚本）
// 循环直到拿到未墙 IP 后关机待命。
type DualMonitor struct {
	srv      *Server
	checkAPI string
	tick     time.Duration
	client   *http.Client
	log      *slog.Logger

	checkFn func(ctx context.Context, ip string, port int) blockCheckResult
}

// StartDualMonitor 启动后台双机监测；ctx 取消即退出。
func (s *Server) StartDualMonitor(ctx context.Context, tick time.Duration) *DualMonitor {
	if tick <= 0 {
		tick = time.Minute
	}
	api := s.AutoRebuildCheckAPI
	if api == "" {
		api = DefaultBlockCheckAPI
	}
	m := &DualMonitor{
		srv:      s,
		checkAPI: api,
		tick:     tick,
		client:   &http.Client{Timeout: 20 * time.Second},
		log:      slog.Default(),
	}
	// 复用自动换 IP 的探测实现（同一 checkAPI 与 blockCheckResult 语义）。
	ab := &AutoRebuildMonitor{checkAPI: api, client: m.client}
	m.checkFn = ab.apiBlockCheck
	s.dualMonitor = m
	go m.run(ctx)
	return m
}

func (m *DualMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
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

func (m *DualMonitor) tickOnce(ctx context.Context) {
	for _, cfg := range m.srv.Store.DualConfigs() {
		if !cfg.Enabled {
			continue
		}
		if !m.due(cfg) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		m.check(ctx, cfg)
	}
}

func (m *DualMonitor) due(cfg store.DualConfig) bool {
	if cfg.LastCheckAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, cfg.LastCheckAt)
	if err != nil {
		return true
	}
	gap := time.Duration(cfg.IntervalSec) * time.Second
	if gap <= 0 {
		gap = DefaultDualInterval
	}
	return time.Since(last) >= gap
}

func (m *DualMonitor) now() string { return time.Now().UTC().Format(time.RFC3339) }

// dualPort 双机链路探测与脚本 SSH 端口：未配置时默认 22。
// 双机的检测、备用机探测与开机脚本共用同一个端口，避免检测与执行用不同端口。
func dualPort(cfg store.DualConfig) int {
	if cfg.Port > 0 {
		return cfg.Port
	}
	return DefaultDualPort
}

// freshActiveID 读回最新的 active（快照可能已过期，避免轮换后仍用旧值覆盖）。
func (m *DualMonitor) freshActiveID(tokenID int, fallback int) int {
	if d, ok := m.srv.Store.DualConfigOf(tokenID); ok && d.ActiveInstanceID > 0 {
		return d.ActiveInstanceID
	}
	return fallback
}

// isDualManaged 报告实例是否被任一已启用的双机配置接管（用于隔离自动换 IP /
// 避免重复删机）。
func (s *Server) isDualManaged(instanceID int) bool {
	if instanceID <= 0 {
		return false
	}
	for _, d := range s.Store.DualConfigs() {
		if !d.Enabled {
			continue
		}
		if d.ActiveInstanceID == instanceID || d.StandbyInstanceID == instanceID {
			return true
		}
	}
	return false
}

func (m *DualMonitor) check(ctx context.Context, cfg store.DualConfig) {
	if cfg.ActiveInstanceID <= 0 {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "error", "error", "未配置使用中实例", m.now())
		return
	}
	// 轮换/重建进行中不重复进入，避免与正在执行的 rotate/rebuildStandbyLoop 竞态。
	// 若状态卡在 rotating/rebuilding 但并无运行中任务（ForceRotate 误拦截/进程重启导致），视为过期自愈。
	busy := m.srv.Tasks != nil && (m.srv.Tasks.Busy(cfg.ActiveInstanceID) || (cfg.StandbyInstanceID > 0 && m.srv.Tasks.Busy(cfg.StandbyInstanceID)))
	if cfg.State == "rotating" || cfg.State == "rebuilding_standby" {
		if !busy {
			m.log.Warn("双机轮询: 轮换状态疑似卡死（无运行中任务），恢复检测", "token_id", cfg.TokenID, "state", cfg.State)
		} else {
			m.log.Info("双机轮询: 轮换/重建进行中，跳过检测", "token_id", cfg.TokenID)
			return
		}
	}
	if busy {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "rotating", "rebuilding", "轮换/重建任务进行中，跳过检测", m.now())
		return
	}
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		m.log.Warn("双机轮询: 取账号凭证失败", "token_id", cfg.TokenID, "error", err)
		return
	}
	api := m.srv.clientFor(pat)
	active, err := api.GetInstance(ctx, cfg.ActiveInstanceID)
	if err != nil {
		var apiErr *linode.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			// 使用中实例已在上游消失：尝试直接扶正备用机。
			m.log.Warn("双机轮询: 使用中实例已不存在，尝试扶正备用机", "active", cfg.ActiveInstanceID)
			m.promoteStandbyAfterLoss(ctx, cfg, pat)
			return
		}
		m.log.Warn("双机轮询: 读取使用中实例失败", "active", cfg.ActiveInstanceID, "error", err)
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "", "error", "读取使用中实例失败: "+err.Error(), m.now())
		return
	}
	if active.Status != "running" {
		// 未运行不自动开机：手动关机是用户意图，监视器不覆盖（与自动换 IP 的跳过语义一致）。
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "", "idle", "使用中实例未运行（"+active.Status+"），跳过检测", m.now())
		return
	}
	// 稳态自愈：active 运行时 standby 预期为 offline；若带外被开机（Linode 控制台手动开机等），
	// 关掉备用机，避免两台同时运行。轮换/重建中已在顶部返回，不会误关正在探测的备用机。
	if cfg.StandbyInstanceID > 0 && cfg.StandbyInstanceID != active.ID {
		if st, err := api.GetInstance(ctx, cfg.StandbyInstanceID); err == nil && st.Status == "running" {
			m.log.Warn("双机轮询: 备用机未关机（带外开机），已关机避免双运行", "standby", cfg.StandbyInstanceID)
			_ = api.Shutdown(ctx, cfg.StandbyInstanceID)
		}
	}
	ip := ""
	if len(active.IPv4) > 0 {
		ip = active.IPv4[0]
	}
	if ip == "" {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "", "idle", "使用中实例没有公网 IPv4，跳过检测", m.now())
		return
	}
	res := m.checkFn(ctx, ip, dualPort(cfg))
	switch {
	case res.err != nil:
		m.log.Warn("双机轮询: 检测出错（不计入失败）", "active", active.ID, "ip", ip, "error", res.err)
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "", "error", "检测出错: "+res.err.Error(), m.now())
	case res.blocked:
		fails := cfg.FailCount + 1
		threshold := cfg.Threshold
		if threshold <= 0 {
			threshold = DefaultDualThreshold
		}
		m.log.Info("双机轮询: 疑似被墙", "active", active.ID, "ip", ip, "fails", fails, "threshold", threshold)
		if fails >= threshold {
			m.rotate(ctx, cfg, pat, active)
			return
		}
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, fails, "", "blocked", res.detail, m.now())
	default:
		if cfg.FailCount > 0 {
			m.log.Info("双机轮询: 恢复正常", "active", active.ID, "ip", ip)
		}
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "idle", "ok", res.detail, m.now())
	}
}

// rotate 执行一次轮换：删 A → 保证 B 干净 → 启动 B 并执行脚本 → 交换角色 →
// 异步重建旧 A（不执行脚本，循环直到 SSH 干净后关机）。
// 成功后 TG 通知新 IP 与距上次被墙间隔。
func (m *DualMonitor) rotate(ctx context.Context, cfg store.DualConfig, pat string, active linode.Instance) {
	m.log.Info("双机轮询: 达到阈值，开始轮换", "token_id", cfg.TokenID, "active", active.ID, "standby", cfg.StandbyInstanceID)
	_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount+1, "rotating", "rebuilding", "被墙，开始轮换", m.now())
	api := m.srv.clientFor(pat)
	label := m.srv.dualAccountLabel(cfg)
	oldIP := ""
	if len(active.IPv4) > 0 {
		oldIP = active.IPv4[0]
	}

	rootPass, _ := m.srv.Store.DualRootPassword(cfg.TokenID)
	if rootPass == "" {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "未保存 root 密码，无法执行开机脚本", m.now())
		m.notifyDualFailed(label, "未保存 root 密码")
		return
	}

	// 1. 删 A（不再关机），随后把 B 切为主。删后旧主缓存保留供 rebuildStandbyLoop 从缓存 spec 重建。
	if err := api.DeleteInstance(ctx, active.ID); err != nil {
		m.log.Warn("双机轮询: 删除使用中实例失败（继续）", "active", active.ID, "error", err)
	}
	_ = m.srv.Store.MarkInstanceMissing(cfg.TokenID, active.ID)

	// 2. 保证 B 存在且 IP 干净（SSH 端口国内可达），不干净则重建直到干净。
	standbyID := cfg.StandbyInstanceID
	specRegion, specType, specImage := active.Region, active.Type, active.Image
	standbyLabel := dualStandbyLabel(active.Label)
	if row, ok := m.srv.Store.CachedInstance(standbyID); ok && row.Region != "" {
		specRegion, specType, specImage = row.Region, row.Type, row.Image
		if row.Label != "" {
			standbyLabel = row.Label
		}
	}
	cleanStandby, err := m.ensureCleanStandby(ctx, cfg, pat, standbyID, specRegion, specType, specImage, standbyLabel, rootPass, active)
	if err != nil {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "备用机准备失败: "+err.Error(), m.now())
		m.notifyDualFailed(label, "备用机准备失败: "+err.Error())
		return
	}
	standbyID = cleanStandby

	// 3. 启动 B（不新建）：仅当离线时才开机，已在运行时直接进入脚本。
	if cur, gerr := api.GetInstance(ctx, standbyID); gerr != nil {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "读取备用机失败: "+gerr.Error(), m.now())
		m.notifyDualFailed(label, "读取备用机失败: "+gerr.Error())
		return
	} else if cur.Status != "running" {
		if err := api.Boot(ctx, standbyID); err != nil {
			_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "备用机开机失败: "+err.Error(), m.now())
			m.notifyDualFailed(label, "备用机开机失败: "+err.Error())
			return
		}
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	err = m.srv.waitForInstanceStatus(waitCtx, api, standbyID, "running")
	cancel()
	if err != nil {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "等待备用机运行失败: "+err.Error(), m.now())
		m.notifyDualFailed(label, "等待备用机运行失败: "+err.Error())
		return
	}

	// 4. 取 B 的 IP 并执行账号级开机脚本（为空则跳过）。
	ips, err := api.GetInstanceIPs(ctx, standbyID)
	if err != nil {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "获取备用机 IP 失败: "+err.Error(), m.now())
		m.notifyDualFailed(label, "获取备用机 IP 失败: "+err.Error())
		return
	}
	host := ips.PublicIPv4()
	if host == "" {
		host = ips.SLAACv6()
	}
	if host == "" {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "备用机没有公网 IP", m.now())
		m.notifyDualFailed(label, "备用机没有公网 IP")
		return
	}
	script := strings.TrimSpace(cfg.Script)
	if script == "" {
		script = DefaultBootScript
	}
	if err := m.runScriptWithRetry(ctx, host, dualPort(cfg), rootPass, script); err != nil {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", "备用机执行开机脚本失败: "+err.Error(), m.now())
		m.notifyDualFailed(label, "备用机执行开机脚本失败: "+err.Error())
		return
	}

	// 5. 交换角色：B 成为 active，旧 A 成为 standby（随后重建）。
	if err := m.srv.Store.UpdateDualInstances(cfg.TokenID, standbyID, active.ID); err != nil {
		m.log.Warn("双机轮询: 更新角色失败", "error", err)
	}
	_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "rebuilding_standby", "ok", fmt.Sprintf("已轮换到实例 #%d，原使用中 #%d 进入重建", standbyID, active.ID), m.now())
	m.log.Info("双机轮询: 已轮换", "new_active", standbyID, "old_active", active.ID)
	m.notifyDualRotated(cfg.TokenID, label, oldIP, host, standbyID)

	// 6. 异步重建旧 A（不执行脚本，循环直到 SSH 干净后关机）。
	go m.rebuildStandbyLoop(cfg, pat, active.ID, rootPass)
}

// ensureCleanStandby 保证备用机存在且 SSH 端口国内可达：缺失则按 spec 重建；
// 若被墙则删机重建（不执行脚本）直到干净或达上限。返回干净的 standby 实例 id。
func (m *DualMonitor) ensureCleanStandby(ctx context.Context, cfg store.DualConfig, pat string, standbyID int, region, typ, image, label, rootPass string, active linode.Instance) (int, error) {
	api := m.srv.clientFor(pat)
	sshPort := dualPort(cfg)
	id := standbyID
	// 缺失则创建。
	if id > 0 {
		if _, err := api.GetInstance(ctx, id); err != nil {
			var apiErr *linode.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				if derr := m.srv.Store.DeleteInstanceCache(cfg.TokenID, id); derr != nil {
					_ = m.srv.Store.MarkInstanceMissing(cfg.TokenID, id)
				}
				id = 0
			} else {
				return 0, fmt.Errorf("读取备用机失败: %w", err)
			}
		}
	}
	if id <= 0 {
		if region == "" || typ == "" || image == "" {
			return 0, fmt.Errorf("备用机缺失且无可用配置重建")
		}
		booted := false
		newInst, err := api.CreateInstance(ctx, linode.CreateOptions{
			Region: region, Type: typ, Image: image, Label: label, RootPass: rootPass, Booted: &booted,
		})
		if err != nil {
			return 0, fmt.Errorf("创建备用机失败: %w", err)
		}
		id = newInst.ID
		if err := api.AttachAllowAllFirewall(ctx, region, id); err != nil {
			m.log.Warn("双机轮询: 备用机防火墙绑定失败（继续）", "id", id, "error", err)
		}
		if err := m.srv.Store.UpsertInstanceCache(cfg.TokenID, newInst); err != nil {
			m.log.Warn("双机轮询: 更新实例缓存失败", "error", err)
		}
		_ = m.srv.Store.UpdateDualInstances(cfg.TokenID, m.freshActiveID(cfg.TokenID, cfg.ActiveInstanceID), id)
	}
	// 先开机再探测：离线时 22 端口必然不通，会被误判为被墙而无限重建。
	for i := 0; i < MaxStandbyRebuilds; i++ {
		inst, err := api.GetInstance(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("读取备用机失败: %w", err)
		}
		if inst.Status != "running" {
			if err := api.Boot(ctx, id); err != nil {
				c, gerr := api.GetInstance(ctx, id)
				if gerr != nil || c.Status == "offline" || c.Status == "" {
					return 0, fmt.Errorf("备用机开机失败: %w", err)
				}
				inst = c
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			if err := m.srv.waitForInstanceStatus(waitCtx, api, id, "running"); err != nil {
				cancel()
				return 0, fmt.Errorf("等待备用机运行失败: %w", err)
			}
			cancel()
			inst, err = api.GetInstance(ctx, id)
			if err != nil {
				return 0, fmt.Errorf("读取备用机失败: %w", err)
			}
		}
		ip := ""
		if len(inst.IPv4) > 0 {
			ip = inst.IPv4[0]
		}
		if ip == "" {
			// 无 IP 则重建一次。
			id, err = m.recreateStandbyOnce(ctx, cfg, pat, id, rootPass)
			if err != nil {
				return 0, err
			}
			continue
		}
		res := m.checkFn(ctx, ip, sshPort)
		if res.err != nil {
			m.log.Warn("双机轮询: 备用机探测出错，重试", "id", id, "error", res.err)
			time.Sleep(5 * time.Second)
			continue
		}
		if !res.blocked {
			return id, nil
		}
		m.log.Info("双机轮询: 备用机 IP 被墙，重建", "id", id, "ip", ip, "attempt", i+1)
		id, err = m.recreateStandbyOnce(ctx, cfg, pat, id, rootPass)
		if err != nil {
			return 0, err
		}
	}
	// 重建不等同于关机：失败时把最后的 standby 关掉，避免与 active 双运行残留。
	_ = api.Shutdown(ctx, id)
	return 0, fmt.Errorf("备用机连续 %d 次重建仍被墙，停止以避免费用失控", MaxStandbyRebuilds)
}

// recreateStandbyOnce 删机重建一台备用机（不执行脚本），返回新实例 id。
// 调用方负责更新 DualConfig 的 standby 指向（此处直接更新）。
func (m *DualMonitor) recreateStandbyOnce(ctx context.Context, cfg store.DualConfig, pat string, oldID int, rootPass string) (int, error) {
	api := m.srv.clientFor(pat)
	old, err := api.GetInstance(ctx, oldID)
	if err != nil {
		if cached, ok := m.srv.Store.CachedInstance(oldID); ok && cached.Region != "" {
			old = cached
		} else {
			return 0, fmt.Errorf("读取备用机失败: %w", err)
		}
	}
	_ = api.Shutdown(ctx, oldID)
	_ = api.DeleteInstance(ctx, oldID)
	if derr := m.srv.Store.DeleteInstanceCache(cfg.TokenID, oldID); derr != nil {
		_ = m.srv.Store.MarkInstanceMissing(cfg.TokenID, oldID)
	}
	booted := true
	newInst, err := api.CreateInstance(ctx, linode.CreateOptions{
		Region: old.Region, Type: old.Type, Image: old.Image, Label: old.Label,
		Tags: old.Tags, RootPass: rootPass, Booted: &booted,
	})
	if err != nil {
		return 0, fmt.Errorf("重建备用机失败: %w", err)
	}
	if err := api.AttachAllowAllFirewall(ctx, old.Region, newInst.ID); err != nil {
		m.log.Warn("双机轮询: 备用机防火墙绑定失败（继续）", "id", newInst.ID, "error", err)
	}
	if err := m.srv.Store.UpsertInstanceCache(cfg.TokenID, newInst); err != nil {
		m.log.Warn("双机轮询: 更新实例缓存失败", "error", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	_ = m.srv.waitForInstanceStatus(waitCtx, api, newInst.ID, "running")
	cancel()
	if err := m.srv.Store.UpdateDualInstances(cfg.TokenID, m.freshActiveID(cfg.TokenID, cfg.ActiveInstanceID), newInst.ID); err != nil {
		m.log.Warn("双机轮询: 更新备用机指向失败", "error", err)
	}
	return newInst.ID, nil
}

// rebuildStandbyLoop 把旧 active（现 standby）删机重建（不执行脚本），循环直到
// SSH 端口国内可达，然后关机待命。
// 探测用重建必须开机，但该循环退出时无论以何种路径都关掉 standby，避免与 active 双运行残留。
func (m *DualMonitor) rebuildStandbyLoop(cfg store.DualConfig, pat string, standbyID int, rootPass string) {
	sshPort := dualPort(cfg)
	api := m.srv.clientFor(pat)
	id := standbyID
	defer func() {
		if id > 0 {
			_ = api.Shutdown(context.Background(), id)
		}
	}()
	for i := 0; i < MaxStandbyRebuilds; i++ {
		// 用重建任务执行删机重建（跳过脚本），任务日志可在任务页查看。
		// 被墙或失败都不退出：继续重建，直到拿到没被墙的实例待命（上限防费用失控）。
		taskID, err := m.srv.Tasks.StartWithOptions(id, "", rootPass, pat, "双机备用重建", true)
		if err != nil {
			m.log.Warn("双机轮询: 备用重建启动失败，重试", "id", id, "error", err, "attempt", i+1)
			time.Sleep(5 * time.Second)
			continue
		}
		newID := m.awaitTaskNewID(taskID)
		if newID <= 0 {
			if t, terr := m.srv.Tasks.Get(taskID); terr == nil && t.NewInstanceID > 0 {
				newID = t.NewInstanceID
			}
		}
		if newID <= 0 {
			// 任务失败且未产生新实例：继续下一轮重建，直到干净或达上限。
			m.log.Warn("双机轮询: 备用重建失败，继续下一轮", "id", id, "attempt", i+1)
			_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "rebuilding_standby", "error", "备用重建失败，继续重建直到未被墙", m.now())
			// newID 失败优先沿用当前 id 重试（其缓存 spec 仍在），避免 standby 指针悬空。
			continue
		}
		id = newID
		// 任务完成时 Repoint 已把 standby 更新到 newID；这里只补新鲜 active，
		// 不能在读不到 active 时回退到 standby，否则会把 active 写成 standby。
		if fresh, ok := m.srv.Store.DualConfigOf(cfg.TokenID); ok && fresh.ActiveInstanceID > 0 && fresh.ActiveInstanceID != id {
			_ = m.srv.Store.UpdateDualInstances(cfg.TokenID, fresh.ActiveInstanceID, id)
		}
		// 探测新 IP 的 SSH 是否被墙：失败/被墙都不退出，继续重建直到干净。
		inst, err := api.GetInstance(context.Background(), id)
		if err != nil {
			m.log.Warn("双机轮询: 读取重建后备用机失败，重建", "id", id, "error", err, "attempt", i+1)
			continue
		}
		ip := ""
		if len(inst.IPv4) > 0 {
			ip = inst.IPv4[0]
		}
		if ip == "" {
			continue
		}
		res := m.checkFn(context.Background(), ip, sshPort)
		if res.err != nil {
			m.log.Warn("双机轮询: 备用机探测出错，重建", "id", id, "error", res.err, "attempt", i+1)
			_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "rebuilding_standby", "error", "备用机探测出错，重建: "+res.err.Error(), m.now())
			continue
		}
		if res.blocked {
			m.log.Info("双机轮询: 重建后仍被墙，继续重建", "id", id, "ip", ip, "attempt", i+1)
			continue
		}
		// 干净：关机待命，回到 idle。
		_ = api.Shutdown(context.Background(), id)
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "idle", "idle", fmt.Sprintf("备用机 #%d 已就绪（%s），关机待命", id, ip), m.now())
		m.log.Info("双机轮询: 备用机已就绪", "id", id, "ip", ip)
		return
	}
	_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "error", "error", fmt.Sprintf("备用机连续 %d 次重建仍被墙，已停止", MaxStandbyRebuilds), m.now())
}

// currentActive 读取最新的 active id（轮换后可能已变化）；读不到时返回 0，
// 调用方应跳过写入，避免把 standby 误写为 active。
func (m *DualMonitor) currentActive(tokenID, _ int) int {
	if d, ok := m.srv.Store.DualConfigOf(tokenID); ok && d.ActiveInstanceID > 0 {
		return d.ActiveInstanceID
	}
	return 0
}

// waitOffline 等待实例进入 offline；已是离线/未知状态直接通过，
// 避免对 fake 等未知状态无限等待。
func (m *DualMonitor) waitOffline(ctx context.Context, api *linode.Client, id int) error {
	inst, err := api.GetInstance(ctx, id)
	if err != nil {
		var apiErr *linode.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	switch inst.Status {
	case "offline", "":
		return nil
	case "running", "shutting_down", "booting", "rebooting":
		// 继续等待
	default:
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return m.srv.waitForInstanceStatus(waitCtx, api, id, "offline")
}

func (m *DualMonitor) awaitTaskNewID(taskID string) int {
	for i := 0; i < 240; i++ {
		t, err := m.srv.Tasks.Get(taskID)
		if err != nil {
			return 0
		}
		if t.Status != StatusRunning {
			return t.NewInstanceID
		}
		time.Sleep(5 * time.Second)
	}
	return 0
}

// promoteStandbyAfterLoss 在 active 已消失时尝试扶正 standby。
func (m *DualMonitor) promoteStandbyAfterLoss(ctx context.Context, cfg store.DualConfig, pat string) {
	if cfg.StandbyInstanceID <= 0 {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "error", "error", "使用中实例已不存在且无备用机", m.now())
		return
	}
	// 旧 active 已消失：保留缓存行（标为不可用）供迁移/清理，避免静默丢失配置。
	_ = m.srv.Store.MarkInstanceMissing(cfg.TokenID, cfg.ActiveInstanceID)
	api := m.srv.clientFor(pat)
	inst, err := api.GetInstance(ctx, cfg.StandbyInstanceID)
	if err != nil {
		// 备用机也缺失（重建中误删/带外删除等）：按缓存 spec 重建一台再扶正，不要直接报错卡死。
		specRegion, specType, specImage, specLabel := "", "", "", ""
		if row, ok := m.srv.Store.CachedInstance(cfg.ActiveInstanceID); ok && row.Region != "" {
			specRegion, specType, specImage = row.Region, row.Type, row.Image
			specLabel = row.Label
		}
		if specRegion == "" {
			if row, ok := m.srv.Store.CachedInstance(cfg.StandbyInstanceID); ok && row.Region != "" {
				specRegion, specType, specImage = row.Region, row.Type, row.Image
				specLabel = row.Label
			}
		}
		if specRegion == "" {
			// 指针指向的实例缓存都无规格时，兜底该账号缓存里任意一条带规格的行。
			for _, r := range m.srv.Store.CachedInstanceRows(cfg.TokenID) {
				if r.Instance.Region != "" {
					specRegion, specType, specImage = r.Instance.Region, r.Instance.Type, r.Instance.Image
					specLabel = r.Instance.Label
					break
				}
			}
		}
		rootPass, _ := m.srv.Store.DualRootPassword(cfg.TokenID)
		standbyLabel := dualStandbyLabel(specLabel)
		standbyID := cfg.StandbyInstanceID
		// ensureCleanStandby 内部处理缺失：立即可用 spec 立刻新建（其构造阶段仍需探活）
		// 但这里 active 本身已失，按缓存重建 standby 再开机扶正。
		newID, cerr := m.ensureCleanStandby(ctx, cfg, pat, standbyID, specRegion, specType, specImage, standbyLabel, rootPass, linode.Instance{})
		if cerr != nil {
			_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "error", "error", "备用机也不可用且重建失败: "+err.Error()+" / "+cerr.Error()+"（双机实例均已在上游删除且缓存无规格，请先在实例页创建新实例，再回到账号配置重新选择模板）", m.now())
			return
		}
		inst, err = api.GetInstance(ctx, newID)
		if err != nil {
			_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "error", "error", "备用机也不可用: "+err.Error(), m.now())
			return
		}
	}
	if inst.Status != "running" {
		if err := api.Boot(ctx, inst.ID); err != nil {
			_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "error", "error", "备用机开机失败: "+err.Error(), m.now())
			return
		}
	}
	rootPass, _ := m.srv.Store.DualRootPassword(cfg.TokenID)
	script := cfg.Script
	if script == "" {
		script = DefaultBootScript
	}
	ips, err := api.GetInstanceIPs(ctx, inst.ID)
	if err == nil {
		host := ips.PublicIPv4()
		if host == "" {
			host = ips.SLAACv6()
		}
		if host != "" && rootPass != "" {
			_ = m.runScriptWithRetry(ctx, host, dualPort(cfg), rootPass, script)
		}
	}
	// 扶正后 standby 指向已不存在的旧 active 无法再重建；置 0，下次轮换时重建。
	_ = m.srv.Store.UpdateDualInstances(cfg.TokenID, inst.ID, 0)
	_ = m.srv.Store.UpdateDualState(cfg.TokenID, 0, "idle", "ok", fmt.Sprintf("使用中实例丢失，已扶正备用机 #%d", inst.ID), m.now())
}

// ForceRotate 立即执行一次轮换（不等被墙阈值）。
// 注意：handleRotateDual 已做状态拦截并预占 rotating 锁，ForceRotate 不再重复拦截 rotating 本身，
// 否则预占锁会使自身误判、永久卡死。
func (m *DualMonitor) ForceRotate(ctx context.Context, cfg store.DualConfig) {
	// 重新读取最新配置
	if fresh, ok := m.srv.Store.DualConfigOf(cfg.TokenID); ok {
		cfg = fresh
	}
	if m.srv.Tasks != nil && (m.srv.Tasks.Busy(cfg.ActiveInstanceID) || (cfg.StandbyInstanceID > 0 && m.srv.Tasks.Busy(cfg.StandbyInstanceID))) {
		m.log.Info("双机轮询: 重建任务进行中，忽略重复触发", "token_id", cfg.TokenID)
		return
	}
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "error", "error", "取账号凭证失败: "+err.Error(), m.now())
		m.notifyDualFailed(m.srv.dualAccountLabel(cfg), "取账号凭证失败: "+err.Error())
		return
	}
	api := m.srv.clientFor(pat)
	active, err := api.GetInstance(ctx, cfg.ActiveInstanceID)
	if err != nil {
		var apiErr *linode.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			m.promoteStandbyAfterLoss(ctx, cfg, pat)
			return
		}
		_ = m.srv.Store.UpdateDualState(cfg.TokenID, cfg.FailCount, "error", "error", "读取使用中实例失败: "+err.Error(), m.now())
		m.notifyDualFailed(m.srv.dualAccountLabel(cfg), "读取使用中实例失败: "+err.Error())
		return
	}
	m.rotate(ctx, cfg, pat, active)
}

// runScriptWithRetry 通过 root 密码 SSH 执行脚本，连接失败最多重试 3 次。
func (m *DualMonitor) runScriptWithRetry(ctx context.Context, host string, port int, password, script string) error {
	if port == 0 {
		port = 22
	}
	run := m.srv.sshRunner()
	sshCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		res, err := run(sshCtx, sshrun.Options{
			Host: host, Port: port, Password: password,
			InsecureSkipVerify: true, Timeout: 5 * time.Minute,
		}, script)
		if err == nil {
			if res.Exit != 0 {
				detail := res.Stderr
				if detail == "" {
					detail = res.Stdout
				}
				return fmt.Errorf("开机脚本退出码 %d: %s", res.Exit, detail)
			}
			return nil
		}
		if !isSSHConnectError(err) {
			return fmt.Errorf("SSH 执行开机脚本失败: %w", err)
		}
		lastErr = err
		select {
		case <-sshCtx.Done():
			return fmt.Errorf("SSH 连接备用机超时: %w", lastErr)
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("SSH 连接失败（已尝试 3 次）: %w", lastErr)
}

func (s *Server) dualAccountLabel(cfg store.DualConfig) string {
	for _, t := range s.Store.TokensOf(cfg.UserID) {
		if t.ID == cfg.TokenID {
			if t.Label != "" {
				return t.Label + " (#" + strconv.Itoa(t.ID) + ")"
			}
			return "#" + strconv.Itoa(t.ID)
		}
	}
	return "#" + strconv.Itoa(cfg.TokenID)
}