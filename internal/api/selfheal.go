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
	"strings"
	"sync"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/lish"
	"linode-panel/internal/store"
)

// DefaultBlockCheckAPI 是从国内探测 IP 可达性的接口（两种自愈模式共用）。
const DefaultBlockCheckAPI = "https://api.24kplus.com/ipcheck"

// 自愈监测默认参数（自动换 IP 与双机共用同一套检测设置）。
const (
	DefaultSelfHealPort      = 22
	DefaultSelfHealThreshold = 3
	DefaultSelfHealInterval  = 5 * time.Minute
	// MaxStandbyRebuilds bounds the "rebuild until clean" loop so a fully
	// blocked region does not burn money forever.
	MaxStandbyRebuilds = 20
	// MaxServiceProbeRetries bounds how many freshly scripted standbys are
	// tried before the rotation gives up (service port unreachable from CN).
	MaxServiceProbeRetries = 5
	// StandbyRebuildCooldown pauses automatic standby/promote rebuilds after a
	// full cycle still could not find a clean IP (Linode's region IP pool is
	// small and tends to hand back the same blocked addresses). Without it the
	// monitor rebuilds a fresh instance on every tick indefinitely.
	StandbyRebuildCooldown = 30 * time.Minute
	// dualStateCooldown is the persisted dual_state used while the rebuild
	// circuit breaker is open. A manual check/rotate clears it.
	dualStateCooldown = "cooldown"
)

// errStandbyPoolExhausted reports that repeated rebuilds kept landing on
// already-blocked IPs, i.e. the region's assignable pool is exhausted for now.
var errStandbyPoolExhausted = errors.New("备用机可用 IP 已耗尽")

// SelfHealMonitor 是唯一的「被墙自愈」后台监测器，统一处理两种模式：
//   - rebuild：探测账号下每台实例，达到阈值后删机重建换 IP；
//   - dual：探测 A/B 对，达到阈值后轮换到备用机并重建旧机。
//
// 两种模式共用国内探测接口、检测端口、开机脚本、root 密码与冷却/通知逻辑。
type SelfHealMonitor struct {
	srv      *Server
	checkAPI string
	tick     time.Duration
	client   *http.Client
	log      *slog.Logger

	checkFn func(ctx context.Context, ip string, port int) blockCheckResult
	lck     sync.Map
	// ops serializes check/rotate/rebuild per account (tokenID -> *sync.Mutex).
	ops sync.Map
	// cooldowns holds the per-token rebuild circuit breaker deadline. It is
	// in-memory on purpose: probe results keep updating dual_state/last_check_at
	// and must not accidentally close (or endlessly reopen) the breaker.
	cooldowns sync.Map
	// ports remembers the last effective detection port per token so a change
	// is visible in the log immediately (and proves which port is really used).
	ports sync.Map
}

// StartSelfHealMonitor 启动统一的后台自愈监测；ctx 取消即退出。
func (s *Server) StartSelfHealMonitor(ctx context.Context, tick time.Duration) *SelfHealMonitor {
	if tick <= 0 {
		tick = time.Minute
	}
	api := s.AutoRebuildCheckAPI
	if api == "" {
		api = DefaultBlockCheckAPI
	}
	m := &SelfHealMonitor{
		srv:      s,
		checkAPI: api,
		tick:     tick,
		client:   &http.Client{Timeout: 20 * time.Second},
		log:      slog.Default(),
	}
	m.checkFn = m.apiBlockCheck
	s.selfHealMonitor = m
	go m.run(ctx)
	return m
}

func (m *SelfHealMonitor) run(ctx context.Context) {
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

func (m *SelfHealMonitor) tickOnce(ctx context.Context) {
	for _, cfg := range m.srv.Store.SelfHeals() {
		if !cfg.Enabled || ctx.Err() != nil {
			continue
		}
		switch cfg.Mode {
		case store.SelfHealDual:
			if m.due(cfg) {
				m.check(ctx, cfg)
			}
		case store.SelfHealRebuild:
			for _, inst := range cfg.Instances {
				if ctx.Err() != nil {
					return
				}
				if !m.dueInstance(inst, cfg.IntervalSec) {
					continue
				}
				m.checkInstance(ctx, cfg, inst)
			}
		}
	}
}

// dueInstance 判断 rebuild 模式下单个实例是否到了探测时间。
func (m *SelfHealMonitor) dueInstance(inst store.SelfHealInstance, intervalSec int) bool {
	if inst.LastCheckAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, inst.LastCheckAt)
	if err != nil {
		return true
	}
	gap := time.Duration(intervalSec) * time.Second
	if gap <= 0 {
		gap = DefaultSelfHealInterval
	}
	return time.Since(last) >= gap
}

func (m *SelfHealMonitor) due(cfg store.SelfHeal) bool {
	if cfg.LastCheckAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, cfg.LastCheckAt)
	if err != nil {
		return true
	}
	gap := time.Duration(cfg.IntervalSec) * time.Second
	if gap <= 0 {
		gap = DefaultSelfHealInterval
	}
	return time.Since(last) >= gap
}

func (m *SelfHealMonitor) now() string { return time.Now().UTC().Format(time.RFC3339) }

// inCooldown reports whether the account's standby rebuild circuit breaker is
// still open. It only gates the expensive rebuild paths (lost active instance
// and missing standby), never the active IP probe.
func (m *SelfHealMonitor) inCooldown(cfg store.SelfHeal) bool {
	v, ok := m.cooldowns.Load(cfg.TokenID)
	if !ok {
		return false
	}
	until, ok := v.(time.Time)
	return ok && time.Now().Before(until)
}

// clearCooldown closes the circuit breaker after a rebuild succeeded (or the
// user asked for a manual retry).
func (m *SelfHealMonitor) clearCooldown(tokenID int) { m.cooldowns.Delete(tokenID) }

// coolDown opens the rebuild circuit breaker for the account so the next
// automatic tick does not immediately rebuild another instance.
func (m *SelfHealMonitor) coolDown(cfg store.SelfHeal, detail string) {
	m.log.Warn("双机轮询: 备用机重建进入冷却，暂停自动重建",
		"token_id", cfg.TokenID, "cooldown", StandbyRebuildCooldown.String(), "detail", detail)
	m.cooldowns.Store(cfg.TokenID, time.Now().Add(StandbyRebuildCooldown))
	_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, dualStateCooldown, "error",
		fmt.Sprintf("%s（%s 后自动重试）", detail, StandbyRebuildCooldown), m.now())
}

// rebuildFailed records a failed standby/promote rebuild: a pool exhausted
// error opens the circuit breaker, anything else is a normal retryable error.
func (m *SelfHealMonitor) rebuildFailed(cfg store.SelfHeal, err error) {
	if errors.Is(err, errStandbyPoolExhausted) {
		m.coolDown(cfg, err.Error())
		return
	}
	_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", err.Error(), m.now())
}

// healCheckPort 是双机被墙检测用的 TCP 端口（未配置时默认 22）。
//
// 检测端口与开机脚本执行无关：检测可以指向代理/业务端口（443、8443 等）
// 来判断国内是否可达，而开机脚本现在经 Lish 控制台执行，不依赖 SSH 端口。
func healCheckPort(cfg store.SelfHeal) int {
	if cfg.Port > 0 {
		return cfg.Port
	}
	return DefaultSelfHealPort
}

// notePort logs when the effective detection port changes, so "I changed the
// port but it still probes 22" is directly verifiable from the log.
func (m *SelfHealMonitor) notePort(tokenID, port int) {
	v, ok := m.ports.LoadOrStore(tokenID, port)
	if !ok {
		m.log.Info("双机轮询: 使用检测端口", "token_id", tokenID, "port", port)
		return
	}
	if prev, _ := v.(int); prev != port {
		m.ports.Store(tokenID, port)
		m.log.Info("双机轮询: 检测端口已更新", "token_id", tokenID, "old_port", prev, "port", port)
	}
}

// freshActiveID 读回最新的 active（快照可能已过期，避免轮换后仍用旧值覆盖）。
func (m *SelfHealMonitor) freshActiveID(tokenID int, fallback int) int {
	if d, ok := m.srv.Store.SelfHealConfigOf(tokenID); ok && d.ActiveInstanceID > 0 {
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
	for _, d := range s.Store.SelfHeals() {
		if !d.Enabled || d.Mode != store.SelfHealDual {
			continue
		}
		if d.ActiveInstanceID == instanceID || d.StandbyInstanceID == instanceID {
			return true
		}
	}
	return false
}

// tokenLock returns the mutex that serializes every monitor operation for one
// account (detection, rotation, post-rotation standby rebuild). Without it a
// tick or a manual check can start while rotate() is still in its synchronous
// phase (no rebuild task yet), mistake the "rotating" state for a stuck one,
// see the already-deleted active as lost and promote the standby at the same
// time — leaving the pair without an active instance.
func (m *SelfHealMonitor) tokenLock(tokenID int) *sync.Mutex {
	v, _ := m.ops.LoadOrStore(tokenID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// isBusy reports whether a detection/rotation/rebuild currently holds the
// account lock. It is advisory only (the lock may free immediately after).
func (m *SelfHealMonitor) isBusy(tokenID int) bool {
	mu := m.tokenLock(tokenID)
	if mu.TryLock() {
		mu.Unlock()
		return false
	}
	return true
}

func (m *SelfHealMonitor) check(ctx context.Context, cfg store.SelfHeal) { m.tryCheck(ctx, cfg) }

// checkNow 立即探测一次，按模式分派到双机检查或各实例检查。
func (m *SelfHealMonitor) checkNow(ctx context.Context, cfg store.SelfHeal) {
	if cfg.Mode == store.SelfHealDual {
		m.check(ctx, cfg)
		return
	}
	for _, inst := range cfg.Instances {
		m.checkInstance(ctx, cfg, inst)
	}
}

// tryCheck runs one detection round and reports whether it actually ran.
// false means another detection/rotation/rebuild for the account is still in
// flight; the manual-check API surfaces this instead of pretending success.
func (m *SelfHealMonitor) tryCheck(ctx context.Context, cfg store.SelfHeal) bool {
	mu := m.tokenLock(cfg.TokenID)
	if !mu.TryLock() {
		m.log.Info("双机轮询: 已有检测/轮换/重建在进行，跳过本轮", "token_id", cfg.TokenID)
		return false
	}
	defer mu.Unlock()
	// 快照可能已过期（轮换会改指针与状态），拿到锁后重新读取。
	if fresh, ok := m.srv.Store.SelfHealConfigOf(cfg.TokenID); ok {
		cfg = fresh
	}
	m.checkLocked(ctx, cfg)
	return true
}

// checkLocked is tryCheck's body; the caller must hold the account lock.
func (m *SelfHealMonitor) checkLocked(ctx context.Context, cfg store.SelfHeal) {
	if cfg.ActiveInstanceID <= 0 {
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "未配置使用中实例", m.now())
		return
	}
	// 轮换/重建进行中不重复进入，避免与正在执行的 rotate/rebuildStandbyLoop 竞态。
	// 同进程内已由 tokenLock 串行化；这里只用于进程重启等导致的内存锁丢失场景：
	// 状态停在 rotating/rebuilding 且确实没有运行中任务时才视为过期自愈。
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
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "rotating", "rebuilding", "轮换/重建任务进行中，跳过检测", m.now())
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
			// 使用中实例已在上游消失：扶正备用机。扶正本身不建新机，
			// 即使处于重建冷却也应执行，否则会长时间“无主”；只有备用机也
			// 缺失、需要删建时才会受冷却限制（见 promoteStandbyAfterLoss）。
			m.log.Warn("双机轮询: 使用中实例已不存在，尝试扶正备用机", "active", cfg.ActiveInstanceID)
			m.promoteStandbyAfterLoss(ctx, cfg, pat)
			return
		}
		m.log.Warn("双机轮询: 读取使用中实例失败", "active", cfg.ActiveInstanceID, "error", err)
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "", "error", "读取使用中实例失败: "+err.Error(), m.now())
		return
	}
	if active.Status != "running" {
		// 未运行不自动开机：手动关机是用户意图，监视器不覆盖（与自动换 IP 的跳过语义一致）。
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "", "idle", "使用中实例未运行（"+active.Status+"），跳过检测", m.now())
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
	// 缺备补备：active 健康但 standby 缺失时直接建一台关机备用机，不要等下次轮换。
	// 冷却期内跳过补备（但继续探测 active）。
	if cfg.StandbyInstanceID <= 0 && !m.inCooldown(cfg) {
		if err := m.ensureNeutralStandby(ctx, cfg, pat, active); err != nil {
			m.rebuildFailed(cfg, err)
			return
		}
		// 补备完成后继续本轮检测；刷新 cfg
		if fresh, ok := m.srv.Store.SelfHealConfigOf(cfg.TokenID); ok {
			cfg = fresh
		}
	}
	ip := ""
	if len(active.IPv4) > 0 {
		ip = active.IPv4[0]
	}
	if ip == "" {
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "", "idle", "使用中实例没有公网 IPv4，跳过检测", m.now())
		return
	}
	checkPort := healCheckPort(cfg)
	m.notePort(cfg.TokenID, checkPort)
	res := m.checkFn(ctx, ip, checkPort)
	switch {
	case res.err != nil:
		m.log.Warn("双机轮询: 检测出错（不计入失败）", "active", active.ID, "ip", ip, "port", healCheckPort(cfg), "error", res.err)
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "", "error", "检测出错: "+res.err.Error(), m.now())
	case res.blocked:
		fails := cfg.FailCount + 1
		threshold := cfg.Threshold
		if threshold <= 0 {
			threshold = DefaultSelfHealThreshold
		}
		m.log.Info("双机轮询: 疑似被墙", "active", active.ID, "ip", ip, "port", healCheckPort(cfg), "fails", fails, "threshold", threshold)
		if fails >= threshold {
			// 冷却期内说明地区 IP 池暂时无干净地址，轮换只会删掉当前实例
			// 再重建出一台同样被墙的机器；保留现状等待人工处理或冷却结束。
			if m.inCooldown(cfg) {
				m.log.Warn("双机轮询: 备用机重建冷却中，暂停自动轮换（保留当前实例）", "active", active.ID, "ip", ip)
				return
			}
			m.rotate(ctx, cfg, pat, active)
			return
		}
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, fails, "", "blocked", res.detail, m.now())
	default:
		if cfg.FailCount > 0 {
			m.log.Info("双机轮询: 恢复正常", "active", active.ID, "ip", ip, "port", healCheckPort(cfg))
		}
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "idle", "ok", res.detail, m.now())
	}
}

// rotate 执行一次轮换：先保证 B 干净并执行脚本，成功后再删 A 并交换角色，
// 最后异步重建旧 A（不执行脚本，循环直到 SSH 干净后关机）。
// 顺序不能反过来：先删 A 会在 B 不可用（IP 池耗尽等）时留下两台都没有的“无主”状态。
// 成功后 TG 通知新 IP 与距上次被墙间隔。
func (m *SelfHealMonitor) rotate(ctx context.Context, cfg store.SelfHeal, pat string, active linode.Instance) {
	m.log.Info("双机轮询: 达到阈值，开始轮换", "token_id", cfg.TokenID, "active", active.ID, "standby", cfg.StandbyInstanceID)
	_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount+1, "rotating", "rebuilding", "被墙，开始轮换", m.now())
	api := m.srv.clientFor(pat)
	label := m.srv.dualAccountLabel(cfg)
	oldIP := ""
	if len(active.IPv4) > 0 {
		oldIP = active.IPv4[0]
	}

	rootPass, _ := m.srv.Store.AccountRootPassword(cfg.TokenID)
	if rootPass == "" {
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", "未保存 root 密码，无法执行开机脚本", m.now())
		m.notifyDualFailed(label, "未保存 root 密码")
		return
	}

	// 1. 准备 B：确保存在并运行，执行账号开机脚本（经 Lish 控制台），再用
	//    「检测端口」从国内验证业务端口；
	//    不通就换一台重试。只有全部通过才删 A，任何失败都保留当前使用中实例。
	standbyID := cfg.StandbyInstanceID
	if standbyID == active.ID {
		// 防御：指针异常重合时不能把当前主当备用机删建。
		standbyID = 0
	}
	// B 一旦开机，任何失败路径都必须让它回到关机待命，否则主备同时运行。
	// 成功交换角色后 promoted 置位：此时 B 已是事实上的使用中实例，必须保持 running，
	// 旧 A 的清理与重建交给后面的 rebuildStandbyLoop 收尾。
	promoted := false
	defer func() {
		if !promoted && standbyID > 0 {
			_ = api.Shutdown(context.Background(), standbyID)
		}
	}()
	specRegion, specType, specImage := active.Region, active.Type, active.Image
	standbyLabel := healStandbyLabel(active.Label)
	if row, ok := m.srv.Store.CachedInstance(standbyID); ok && row.Region != "" {
		specRegion, specType, specImage = row.Region, row.Type, row.Image
		if row.Label != "" {
			standbyLabel = row.Label
		}
	}
	script := strings.TrimSpace(cfg.Script)
	if script == "" {
		script = DefaultBootScript
	}
	blockedIPs := make(map[string]bool, MaxServiceProbeRetries)
	var host string
	clean := false
	for attempt := 0; attempt < MaxServiceProbeRetries; attempt++ {
		cleanStandby, err := m.ensureCleanStandby(ctx, cfg, pat, standbyID, specRegion, specType, specImage, standbyLabel, rootPass, active)
		if err != nil {
			m.rebuildFailed(cfg, fmt.Errorf("备用机准备失败: %w", err))
			m.notifyDualFailed(label, "备用机准备失败: "+err.Error())
			return
		}
		standbyID = cleanStandby

		// 兜底：确保 B 在运行（正常情况下 ensureCleanStandby 返回时已在运行）。
		if cur, gerr := api.GetInstance(ctx, standbyID); gerr != nil {
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", "读取备用机失败: "+gerr.Error(), m.now())
			m.notifyDualFailed(label, "读取备用机失败: "+gerr.Error())
			return
		} else if cur.Status != "running" {
			if err := api.Boot(ctx, standbyID); err != nil {
				_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", "备用机开机失败: "+err.Error(), m.now())
				m.notifyDualFailed(label, "备用机开机失败: "+err.Error())
				return
			}
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		err = m.srv.waitForInstanceStatus(waitCtx, api, standbyID, "running")
		cancel()
		if err != nil {
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", "等待备用机运行失败: "+err.Error(), m.now())
			m.notifyDualFailed(label, "等待备用机运行失败: "+err.Error())
			return
		}

		ips, err := api.GetInstanceIPs(ctx, standbyID)
		if err != nil {
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", "获取备用机 IP 失败: "+err.Error(), m.now())
			m.notifyDualFailed(label, "获取备用机 IP 失败: "+err.Error())
			return
		}
		host = ips.PublicIPv4()
		if host == "" {
			host = ips.SLAACv6()
		}
		if host == "" {
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", "备用机没有公网 IP", m.now())
			m.notifyDualFailed(label, "备用机没有公网 IP")
			return
		}

		// 先执行脚本把业务/代理端口拉起来，再判断该端口是否国内可达。
		if err := m.runScriptWithRetry(ctx, standbyID, pat, rootPass, script); err != nil {
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", "备用机执行开机脚本失败: "+err.Error(), m.now())
			m.notifyDualFailed(label, "备用机执行开机脚本失败: "+err.Error())
			return
		}

		if fresh, ok := m.srv.Store.SelfHealConfigOf(cfg.TokenID); ok {
			cfg = fresh
		}
		checkPort := healCheckPort(cfg)
		m.notePort(cfg.TokenID, checkPort)
		res := m.checkFn(ctx, host, checkPort)
		if res.err != nil {
			// 检测接口异常不代表 IP 有问题：保留 A，稍后重试，避免误删当前主。
			_ = api.Shutdown(ctx, standbyID)
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "备用机业务端口探测出错: "+res.err.Error(), m.now())
			m.notifyDualFailed(label, "备用机业务端口探测出错: "+res.err.Error())
			return
		}
		if !res.blocked {
			clean = true
			break
		}
		if blockedIPs[host] {
			_ = api.Shutdown(ctx, standbyID)
			detail := fmt.Sprintf("%v：备用机业务端口在重复 IP %s 上仍不可达", errStandbyPoolExhausted, host)
			m.coolDown(cfg, detail)
			m.notifyDualFailed(label, detail)
			return
		}
		blockedIPs[host] = true
		m.log.Info("双机轮询: 备用机业务端口不通，换一台重建", "id", standbyID, "ip", host,
			"port", checkPort, "attempt", attempt+1)
		standbyID, err = m.recreateStandbyOnce(ctx, cfg, pat, standbyID, rootPass)
		if err != nil {
			m.rebuildFailed(cfg, fmt.Errorf("备用机重建失败: %w", err))
			m.notifyDualFailed(label, "备用机重建失败: "+err.Error())
			return
		}
	}
	if !clean {
		_ = api.Shutdown(ctx, standbyID)
		detail := fmt.Sprintf("%v：连续 %d 次重建后业务端口仍不可达", errStandbyPoolExhausted, MaxServiceProbeRetries)
		m.coolDown(cfg, detail)
		m.notifyDualFailed(label, detail)
		return
	}

	// 2. B 已就绪且脚本执行成功、业务端口国内可达，现在才删 A
	//    （缓存行保留供重建时读取规格）。
	if err := api.DeleteInstance(ctx, active.ID); err != nil {
		m.log.Warn("双机轮询: 删除使用中实例失败（继续）", "active", active.ID, "error", err)
	}
	_ = m.srv.Store.MarkInstanceMissing(cfg.TokenID, active.ID)

	// 3. 交换角色：B 成为 active，旧 A 成为 standby（随后重建）。
	// 把新主的标签还原为根名（去掉 -standby），使新备机命名不与新主撞名——否则两台会同名“都是主”。
	m.renameActiveLabel(ctx, api, standbyID, standbyLabel)
	if err := m.srv.Store.UpdateSelfHealInstances(cfg.TokenID, standbyID, active.ID); err != nil {
		m.log.Warn("双机轮询: 更新角色失败", "error", err)
	}
	promoted = true // 旧 A 已删除，B 必须保持 running；旧 A 的重建收尾由 rebuildStandbyLoop 负责
	_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "rebuilding_standby", "ok", fmt.Sprintf("已轮换到实例 #%d，原使用中 #%d 进入重建", standbyID, active.ID), m.now())
	m.log.Info("双机轮询: 已轮换", "new_active", standbyID, "old_active", active.ID)
	m.notifyDualRotated(cfg.TokenID, label, oldIP, host, standbyID)

	// 4. 异步重建旧 A（不执行脚本，循环直到 SSH 可达后关机）。
	// 与检测/轮换共用账号锁：等 rotate 返回（释放锁）后再开始，避免检测误判“状态卡死”。
	oldID := active.ID
	go func() {
		mu := m.tokenLock(cfg.TokenID)
		mu.Lock()
		defer mu.Unlock()
		m.rebuildStandbyLoop(cfg, pat, oldID, rootPass)
	}()
}

// ensureCleanStandby 保证备用机存在、已开机且有公网 IP：缺失则按 spec 重建。
// 不做任何端口探测——面板唯一的「检测端口（TCP）」由 rotate 在执行开机脚本后验证。
func (m *SelfHealMonitor) ensureCleanStandby(ctx context.Context, cfg store.SelfHeal, pat string, standbyID int, region, typ, image, label, rootPass string, active linode.Instance) (int, error) {
	api := m.srv.clientFor(pat)
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
		_ = m.srv.Store.UpdateSelfHealInstances(cfg.TokenID, m.freshActiveID(cfg.TokenID, cfg.ActiveInstanceID), id)
	}
	// 只保证实例已开机并有公网 IP。这里不做任何端口探测：面板只有一个
	// 「检测端口（TCP）」，它的验证统一留给 rotate 在执行开机脚本之后进行。
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
		if len(inst.IPv4) > 0 && inst.IPv4[0] != "" {
			m.clearCooldown(cfg.TokenID)
			return id, nil
		}
		// 没有公网 IP 则重建一次再试。
		id, err = m.recreateStandbyOnce(ctx, cfg, pat, id, rootPass)
		if err != nil {
			return 0, err
		}
	}
	// 重建不等同于关机：失败时把最后的 standby 关掉，避免与 active 双运行残留。
	_ = api.Shutdown(ctx, id)
	return 0, fmt.Errorf("备用机连续 %d 次重建仍没有公网 IP", MaxStandbyRebuilds)
}

// recreateStandbyOnce 删机重建一台备用机（不执行脚本），返回新实例 id。
// 调用方负责更新 SelfHeal 的 standby 指向（此处直接更新）。
func (m *SelfHealMonitor) recreateStandbyOnce(ctx context.Context, cfg store.SelfHeal, pat string, oldID int, rootPass string) (int, error) {
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
	if err := m.srv.Store.UpdateSelfHealInstances(cfg.TokenID, m.freshActiveID(cfg.TokenID, cfg.ActiveInstanceID), newInst.ID); err != nil {
		m.log.Warn("双机轮询: 更新备用机指向失败", "error", err)
	}
	return newInst.ID, nil
}

// rebuildStandbyLoop 把旧 active（现 standby）删机重建（不执行脚本），然后关机待命。
// 不做端口探测：面板唯一的「检测端口（TCP）」只在轮换时对已执行脚本的备用机验证。
// 重建必须开机，退出时无论以何种路径都关掉 standby，避免与 active 双运行残留。
func (m *SelfHealMonitor) rebuildStandbyLoop(cfg store.SelfHeal, pat string, standbyID int, rootPass string) {
	api := m.srv.clientFor(pat)
	id := standbyID
	defer func() {
		if id > 0 {
			_ = api.Shutdown(context.Background(), id)
		}
	}()
	for i := 0; i < MaxStandbyRebuilds; i++ {
		// 用重建任务执行删机重建（跳过脚本），任务日志可在任务页查看。
		// 任务失败则继续重建，直到成功或达上限（上限防费用失控）。
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
			m.log.Warn("双机轮询: 备用重建失败，继续下一轮", "id", id, "attempt", i+1)
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "rebuilding_standby", "error", "备用重建失败，继续重建", m.now())
			// newID 失败优先沿用当前 id 重试（其缓存 spec 仍在），避免 standby 指针悬空。
			continue
		}
		id = newID
		// 任务完成时 Repoint 已把 standby 更新到 newID；这里只补新鲜 active，
		// 不能在读不到 active 时回退到 standby，否则会把 active 写成 standby。
		if fresh, ok := m.srv.Store.SelfHealConfigOf(cfg.TokenID); ok && fresh.ActiveInstanceID > 0 && fresh.ActiveInstanceID != id {
			_ = m.srv.Store.UpdateSelfHealInstances(cfg.TokenID, fresh.ActiveInstanceID, id)
		}
		ip := ""
		if inst, gerr := api.GetInstance(context.Background(), id); gerr == nil && len(inst.IPv4) > 0 {
			ip = inst.IPv4[0]
		}
		// 就绪：关机待命，回到 idle。
		_ = api.Shutdown(context.Background(), id)
		m.clearCooldown(cfg.TokenID)
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "idle", "idle", fmt.Sprintf("备用机 #%d 已就绪（%s），关机待命", id, ip), m.now())
		m.log.Info("双机轮询: 备用机已就绪", "id", id, "ip", ip)
		return
	}
	_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error", fmt.Sprintf("备用机连续 %d 次重建失败", MaxStandbyRebuilds), m.now())
}

// renameActiveLabel 把新主的标签还原为根名（去掉 -standby 后缀），使下次建备机不会与主同名。
func (m *SelfHealMonitor) renameActiveLabel(ctx context.Context, api *linode.Client, id int, curLabel string) {
	if curLabel == "" || !strings.HasSuffix(strings.TrimSpace(curLabel), "-standby") {
		return
	}
	newLabel := strings.TrimSuffix(strings.TrimSpace(curLabel), "-standby")
	if newLabel == "" {
		newLabel = "dual"
	}
	newLabel = sanitizeLinodeLabel(newLabel)
	if err := api.UpdateLabel(ctx, id, newLabel); err != nil {
		m.log.Warn("双机轮询: 新主改名失败（继续）", "id", id, "error", err)
	}
}

// ensureNeutralStandby 在缺备时按使用中实例规格建一台关机备用机（不执行脚本）。
// 返回错误时调用方负责记录状态（IP 池耗尽会开启重建冷却）。
func (m *SelfHealMonitor) ensureNeutralStandby(ctx context.Context, cfg store.SelfHeal, pat string, active linode.Instance) error {
	// Per-token guard: background tick + manual check may race creating two standbys.
	muI, _ := m.lck.LoadOrStore(cfg.TokenID, &sync.Mutex{})
	mu := muI.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	// Re-read: another creator may have filled standby meanwhile.
	if fresh, ok := m.srv.Store.SelfHealConfigOf(cfg.TokenID); ok && fresh.StandbyInstanceID > 0 {
		return nil
	}
	rootPass, _ := m.srv.Store.AccountRootPassword(cfg.TokenID)
	if rootPass == "" {
		m.log.Warn("双机轮询: 缺备但无 root 密码，跳过补备", "token_id", cfg.TokenID)
		return nil
	}
	// Label 必须全局唯一：若新主仍带 -standby 后缀，先把主改回根名再建备机。
	api2 := m.srv.clientFor(pat)
	m.renameActiveLabel(ctx, api2, active.ID, active.Label)
	label := healStandbyLabel(active.Label)
	specRegion, specType, specImage := active.Region, active.Type, active.Image
	if row, ok := m.srv.Store.CachedInstance(cfg.StandbyInstanceID); ok && row.Region != "" {
		specRegion, specType, specImage = row.Region, row.Type, row.Image
		if row.Label != "" {
			label = row.Label
		}
	}
	// 防御：若计算出的 standby 名与当前主同名，强制区分。
	if label == strings.TrimSpace(active.Label) {
		label = healStandbyLabel(strings.TrimSuffix(strings.TrimSpace(active.Label), "-standby") + "-standby")
		for label == strings.TrimSpace(active.Label) {
			label += "2"
		}
	}
	// 有 standby 指针但上游为 404 也视为缺备，ensureCleanStandby 内部处理缺失。
	standbyID := cfg.StandbyInstanceID
	if _, err := m.ensureCleanStandby(ctx, cfg, pat, standbyID, specRegion, specType, specImage, label, rootPass, active); err != nil {
		m.log.Warn("双机轮询: 补备失败", "token_id", cfg.TokenID, "error", err)
		return err
	}
	return nil
}

// currentActive 读取最新的 active id（轮换后可能已变化）；读不到时返回 0，
// 调用方应跳过写入，避免把 standby 误写为 active。
func (m *SelfHealMonitor) currentActive(tokenID, _ int) int {
	if d, ok := m.srv.Store.SelfHealConfigOf(tokenID); ok && d.ActiveInstanceID > 0 {
		return d.ActiveInstanceID
	}
	return 0
}

// waitOffline 等待实例进入 offline；已是离线/未知状态直接通过，
// 避免对 fake 等未知状态无限等待。
func (m *SelfHealMonitor) waitOffline(ctx context.Context, api *linode.Client, id int) error {
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

func (m *SelfHealMonitor) awaitTaskNewID(taskID string) int {
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
// 扶正已有实例不产生新实例，不受重建冷却限制；只有备用机也缺失、需要
// 删机重建时才检查冷却，避免 IP 池耗尽时反复删建。
func (m *SelfHealMonitor) promoteStandbyAfterLoss(ctx context.Context, cfg store.SelfHeal, pat string) {
	if cfg.StandbyInstanceID <= 0 {
		if m.inCooldown(cfg) {
			return
		}
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "使用中实例已不存在且无备用机", m.now())
		return
	}
	// 旧 active 已消失：保留缓存行（标为不可用）供迁移/清理，避免静默丢失配置。
	_ = m.srv.Store.MarkInstanceMissing(cfg.TokenID, cfg.ActiveInstanceID)
	api := m.srv.clientFor(pat)
	inst, err := api.GetInstance(ctx, cfg.StandbyInstanceID)
	if err != nil {
		if m.inCooldown(cfg) {
			m.log.Info("双机轮询: 重建冷却中，暂不重建备用机", "token_id", cfg.TokenID)
			return
		}
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
		rootPass, _ := m.srv.Store.AccountRootPassword(cfg.TokenID)
		standbyLabel := healStandbyLabel(specLabel)
		standbyID := cfg.StandbyInstanceID
		// ensureCleanStandby 内部处理缺失：立即可用 spec 立刻新建（其构造阶段仍需探活）
		// 但这里 active 本身已失，按缓存重建 standby 再开机扶正。
		newID, cerr := m.ensureCleanStandby(ctx, cfg, pat, standbyID, specRegion, specType, specImage, standbyLabel, rootPass, linode.Instance{})
		if cerr != nil {
			m.rebuildFailed(cfg, fmt.Errorf("备用机也不可用且重建失败: %v / %w", err, cerr))
			return
		}
		inst, err = api.GetInstance(ctx, newID)
		if err != nil {
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "备用机也不可用: "+err.Error(), m.now())
			return
		}
	}
	if inst.Status != "running" {
		if err := api.Boot(ctx, inst.ID); err != nil {
			_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "备用机开机失败: "+err.Error(), m.now())
			return
		}
	}
	rootPass, _ := m.srv.Store.AccountRootPassword(cfg.TokenID)
	script := cfg.Script
	if script == "" {
		script = DefaultBootScript
	}
	var scriptErr error
	if rootPass != "" {
		scriptErr = m.runScriptWithRetry(ctx, inst.ID, pat, rootPass, script)
	}
	m.renameActiveLabel(ctx, api, inst.ID, inst.Label)
	// 扶正后 standby 指向已不存在的旧 active 无法再重建；置 0，下次轮换时重建。
	_ = m.srv.Store.UpdateSelfHealInstances(cfg.TokenID, inst.ID, 0)
	m.clearCooldown(cfg.TokenID)
	if scriptErr != nil {
		// 已经扶正（否则整对机器都没有主），但必须把脚本失败明确暴露出来：
		// 密码错误时新主看起来在运行，实际代理配置没生效。
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "error", "error",
			fmt.Sprintf("已扶正备用机 #%d，但开机脚本失败: %v", inst.ID, scriptErr), m.now())
		m.notifyDualFailed(m.srv.dualAccountLabel(cfg), "已扶正备用机但开机脚本失败: "+scriptErr.Error())
		return
	}
	_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, 0, "idle", "ok", fmt.Sprintf("使用中实例丢失，已扶正备用机 #%d", inst.ID), m.now())
}

// ForceRotate 立即执行一次轮换（不等被墙阈值）。
// 注意：handleRotateDual 已做状态拦截并预占 rotating 锁，ForceRotate 不再重复拦截 rotating 本身，
// 否则预占锁会使自身误判、永久卡死。
func (m *SelfHealMonitor) ForceRotate(ctx context.Context, cfg store.SelfHeal) {
	// 与后台检测共用账号锁：检测/轮换/重建进行中时不叠加重入。
	mu := m.tokenLock(cfg.TokenID)
	if !mu.TryLock() {
		m.log.Info("双机轮询: 已有检测/轮换/重建在进行，忽略立即替换", "token_id", cfg.TokenID)
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "已有检测/轮换在进行，请稍后重试", m.now())
		return
	}
	defer mu.Unlock()
	// 重新读取最新配置
	if fresh, ok := m.srv.Store.SelfHealConfigOf(cfg.TokenID); ok {
		cfg = fresh
	}
	// 用户明确点击“立即替换”：解除该账号的重建冷却，允许立即补备/重建。
	m.clearCooldown(cfg.TokenID)
	if m.srv.Tasks != nil && (m.srv.Tasks.Busy(cfg.ActiveInstanceID) || (cfg.StandbyInstanceID > 0 && m.srv.Tasks.Busy(cfg.StandbyInstanceID))) {
		m.log.Info("双机轮询: 重建任务进行中，忽略重复触发", "token_id", cfg.TokenID)
		return
	}
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "取账号凭证失败: "+err.Error(), m.now())
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
		_ = m.srv.Store.UpdateSelfHealState(cfg.TokenID, cfg.FailCount, "error", "error", "读取使用中实例失败: "+err.Error(), m.now())
		m.notifyDualFailed(m.srv.dualAccountLabel(cfg), "读取使用中实例失败: "+err.Error())
		return
	}
	m.rotate(ctx, cfg, pat, active)
}

// runScriptWithRetry 通过 Lish 控制台（root 密码登录）执行脚本；连接类失败
// 最多重试 10 次，密码/认证错误立即失败。
func (m *SelfHealMonitor) runScriptWithRetry(ctx context.Context, instanceID int, pat, password, script string) error {
	run := m.srv.lishRunner()
	lishCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// 新机从 "running" 到控制台可登录通常需要几十秒：连接类失败多给几次机会。
	// 密码错误则立即失败，不做无意义的重复尝试。
	const maxAttempts = 10
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err := run(lishCtx, lish.Options{
			InstanceID: instanceID,
			PAT:        pat,
			Password:   password,
			APIBase:    m.srv.APIBase,
			Timeout:    5 * time.Minute,
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
		if lish.IsAuthError(err) {
			return fmt.Errorf("root 密码不正确或控制台登录失败（请更新账号配置里的 root 密码）: %w", err)
		}
		if !lish.IsConnectError(err) {
			return fmt.Errorf("Lish 执行开机脚本失败: %w", err)
		}
		lastErr = err
		select {
		case <-lishCtx.Done():
			return fmt.Errorf("Lish 连接备用机超时: %w", lastErr)
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("Lish 连接失败（已尝试 %d 次）: %w", maxAttempts, lastErr)
}

func (s *Server) dualAccountLabel(cfg store.SelfHeal) string {
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

// ---------- 国内被墙探测（两种模式共用） ----------

// blockCheckResult 是从国内探测实例公网 IP 的结果。
type blockCheckResult struct {
	blocked bool
	detail  string
	err     error
}

// apiBlockCheck 通过国内 ipcheck 接口探测 ip:port 的 TCP 连通性。
func (m *SelfHealMonitor) apiBlockCheck(ctx context.Context, ip string, port int) blockCheckResult {
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

// parseBlockCheck 只以 TCP 端口是否可达判定被墙；检测接口自身出错不计入失败。
func parseBlockCheck(ip string, port int, body []byte) blockCheckResult {
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Ping bool `json:"ping"`
			TCP  bool `json:"tcp"`
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

// ---------- rebuild 模式：删机重建换 IP ----------

// checkInstance 探测 rebuild 模式下的一台实例，达到阈值则触发重建。
func (m *SelfHealMonitor) checkInstance(ctx context.Context, cfg store.SelfHeal, inst store.SelfHealInstance) {
	id := inst.InstanceID
	// 若实例被其它账号的双机对接管，交给 dual 模式处理，避免重复删机。
	if m.srv.isDualManaged(id) {
		return
	}
	if m.srv.Tasks != nil && m.srv.Tasks.Busy(id) {
		_ = m.srv.Store.UpdateAutoRebuildState(id, inst.FailCount, "rebuilding",
			"重建任务进行中，跳过检测", m.now(), "", "")
		return
	}
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		m.log.Warn("自愈: 取 PAT 失败", "instance_id", id, "error", err)
		return
	}
	cur, err := m.srv.clientFor(pat).GetInstance(ctx, id)
	if err != nil {
		var apiErr *linode.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			m.log.Info("自愈: 实例不存在，标记缓存失效", "instance_id", id)
			_ = m.srv.Store.MarkInstanceMissing(cfg.TokenID, id)
			return
		}
		m.log.Warn("自愈: 读取实例失败", "instance_id", id, "error", err)
		_ = m.srv.Store.UpdateAutoRebuildState(id, inst.FailCount, "error", "读取实例失败: "+err.Error(), m.now(), "", "")
		return
	}
	if cur.Status != "running" {
		_ = m.srv.Store.UpdateAutoRebuildState(id, inst.FailCount, "idle",
			"实例未运行（"+cur.Status+"），跳过检测", m.now(), "", "")
		return
	}
	ip := ""
	if len(cur.IPv4) > 0 {
		ip = cur.IPv4[0]
	}
	if ip == "" {
		_ = m.srv.Store.UpdateAutoRebuildState(id, inst.FailCount, "idle", "实例没有公网 IPv4，跳过检测", m.now(), "", "")
		return
	}
	port := cfg.Port
	if port <= 0 {
		port = DefaultSelfHealPort
	}
	res := m.checkFn(ctx, ip, port)
	switch {
	case res.err != nil:
		m.log.Warn("自愈: 检测出错（不计入失败）", "instance_id", id, "ip", ip, "port", port, "error", res.err)
		_ = m.srv.Store.UpdateAutoRebuildState(id, inst.FailCount, "error", "检测出错: "+res.err.Error(), m.now(), "", "")
	case res.blocked:
		fails := inst.FailCount + 1
		threshold := cfg.Threshold
		if threshold <= 0 {
			threshold = DefaultSelfHealThreshold
		}
		m.log.Info("自愈: 疑似被墙", "instance_id", id, "ip", ip, "port", port, "fails", fails, "threshold", threshold)
		if fails >= threshold {
			m.triggerRebuild(ctx, cfg, inst, ip, res.detail, fails)
			return
		}
		_ = m.srv.Store.UpdateAutoRebuildState(id, fails, "blocked", res.detail, m.now(), "", "")
	default:
		if inst.FailCount > 0 {
			m.log.Info("自愈: 恢复正常", "instance_id", id, "ip", ip, "port", port)
		}
		_ = m.srv.Store.UpdateAutoRebuildState(id, 0, "ok", res.detail, m.now(), "", "")
	}
}

func (m *SelfHealMonitor) triggerRebuild(ctx context.Context, cfg store.SelfHeal, inst store.SelfHealInstance, ip, detail string, fails int) {
	id := inst.InstanceID
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		m.log.Warn("自愈: 取 PAT 失败，暂不重建", "instance_id", id, "error", err)
		return
	}
	taskID, err := m.srv.Tasks.Start(id, m.srv.rebuildScriptFor(id), m.srv.rootPasswordFor(id), pat, "自愈换IP")
	if err != nil {
		if errors.Is(err, ErrBusy) {
			if t := cfg.Threshold; t > 0 && fails > t {
				fails = t
			}
			_ = m.srv.Store.UpdateAutoRebuildState(id, fails, "blocked", detail+"；另有重建任务进行中，等待重试", m.now(), "", "")
			return
		}
		_ = m.srv.Store.UpdateAutoRebuildState(id, fails, "error", "启动重建失败: "+err.Error(), m.now(), "", "")
		return
	}
	m.log.Info("自愈: 已触发重建", "instance_id", id, "ip", ip, "task_id", taskID)
	_ = m.srv.Store.UpdateAutoRebuildState(id, 0, "rebuilding", "被墙，已触发重建换 IP（"+ip+"）", m.now(), m.now(), taskID)
	go m.awaitRebuild(inst, taskID, ip, inst.LastBlockedAt)
}

func (m *SelfHealMonitor) awaitRebuild(inst store.SelfHealInstance, taskID, oldIP, prevBlockedAt string) {
	for {
		t, err := m.srv.Tasks.Get(taskID)
		if err != nil {
			return
		}
		if t.Status == StatusRunning {
			time.Sleep(5 * time.Second)
			continue
		}
		if t.NewInstanceID > 0 && t.NewInstanceID != inst.InstanceID {
			m.srv.repointInstanceCache(inst.InstanceID, t.NewInstanceID)
		}
		if t.Status == StatusFailed {
			_ = m.srv.Store.UpdateAutoRebuildStateByCache(inst.CacheID, 0, "error", "重建失败: "+t.Err, m.now(), "", "")
			return
		}
		_ = m.srv.Store.UpdateAutoRebuildStateByCache(inst.CacheID, 0, "idle", "重建完成，等待下一轮检测", m.now(), "", "")
		newID := t.NewInstanceID
		if newID <= 0 {
			newID = inst.InstanceID
		}
		label := ""
		if cached, ok := m.srv.Store.CachedInstance(newID); ok && cached.Label != "" {
			label = cached.Label
		}
		m.notifyAutoRebuilt(label, newID, oldIP, t.NewIP, prevBlockedAt)
		return
	}
}

// rebuildScriptFor 解析重建时使用的开机脚本。
func (s *Server) rebuildScriptFor(id int) string {
	if script, ok := s.Store.InstanceScript(id); ok && script != "" {
		return script
	}
	return DefaultBootScript
}
