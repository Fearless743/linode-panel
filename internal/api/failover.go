package api

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// FailoverMonitor 周期性地检查每个已启用替换的实例，当承载它的账号被封禁、
// 或账号无余额且实例确实不可用（无法运行/启动）、且连续失败达到阈值时，用相同
// 配置在所属账号组内的另一个健康账号上创建一个替换实例，并把替换配置迁移到新
// 实例。
//
// 结构仿照 AutoRebuildMonitor：同样的 tickOnce/due/check 节奏、slog 中文日志
// 与可注入的探测/客户端，便于单测。
type FailoverMonitor struct {
	srv  *Server
	tick time.Duration
	log  *slog.Logger

	// probe 探测某 PAT 的账号健康（默认 srv.probeAccountHealth）。
	probe func(ctx context.Context, pat string) (string, string)
	// clientFor 按 PAT 构造 Linode 客户端（默认 srv.clientFor）。
	clientFor func(pat string) LinodeAPI
}

// StartFailoverMonitor 启动后台替换监控并返回它；ctx 取消即退出。tick<=0 时
// 退化为 time.Minute。启动时先 tick 一次（与 autorebuild 一致），便于重启后
// 立刻恢复检查。
func (s *Server) StartFailoverMonitor(ctx context.Context, tick time.Duration) *FailoverMonitor {
	if tick <= 0 {
		tick = time.Minute
	}
	m := &FailoverMonitor{
		srv:  s,
		tick: tick,
		log:  slog.Default(),
	}
	m.probe = s.probeAccountHealth
	m.clientFor = func(pat string) LinodeAPI { return s.clientFor(pat) }
	go m.run(ctx)
	return m
}

func (m *FailoverMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
	// 启动后先检查一轮，别等一个完整 tick。
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

// tickOnce 串行处理所有到期的已启用配置。替换是同步的（一轮可能较慢），这里
// 刻意不起额外 goroutine：实现简单、状态不会并发交错，代价只是慢一轮。
func (m *FailoverMonitor) tickOnce(ctx context.Context) {
	for _, cfg := range m.srv.Store.Failovers() {
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

// due 报告 cfg 是否已到下一次检查时间；逻辑与 AutoRebuildMonitor.due 一致。
func (m *FailoverMonitor) due(cfg store.Failover) bool {
	if cfg.LastCheckAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, cfg.LastCheckAt)
	if err != nil {
		return true
	}
	gap := time.Duration(cfg.IntervalSec) * time.Second
	if gap <= 0 {
		gap = DefaultFailoverInterval
	}
	return time.Since(last) >= gap
}

// now 返回当前 UTC 时间（RFC3339），用作状态时间戳。
func (m *FailoverMonitor) now() string { return time.Now().UTC().Format(time.RFC3339) }

// check 探测一个实例承载账号的健康状况，并在实例确实不可用、且连续失败达到
// 阈值时触发替换。
//
// 判定口径（账号无余额 ≠ 实例不能用：Linode 欠费有宽限期，不会立刻停机）：
//   - HealthOK：账号与实例均正常，归零 FailCount。
//   - HealthBanned：账号被封禁，直接累计 FailCount，达阈值即替换。
//   - HealthNoBalance：只是资金预警，需叠加「实例不可用」才累计，见 checkNoBalance。
//   - HealthError：检测本身失败，不累计（保持原有语义）。
func (m *FailoverMonitor) check(ctx context.Context, cfg store.Failover) {
	id := cfg.InstanceID
	pat, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		m.log.Warn("账号替换: 取账号凭证失败", "instance_id", id, "token_id", cfg.TokenID, "error", err)
		_ = m.srv.Store.UpdateFailoverState(id, cfg.FailCount, "error",
			"取账号凭证失败: "+err.Error(), m.now(), "")
		return
	}
	status, detail := m.probe(ctx, pat)
	switch status {
	case HealthOK:
		if cfg.FailCount > 0 {
			m.log.Info("账号替换: 账号恢复正常", "instance_id", id, "token_id", cfg.TokenID)
		}
		_ = m.srv.Store.UpdateFailoverState(id, 0, HealthOK, detail, m.now(), "")
	case HealthBanned:
		// 封号：账号层即可判定，直接累计。
		m.accumulate(ctx, cfg, HealthBanned, detail)
	case HealthNoBalance:
		// 无余额：不以资金预警为准，只在实例确实不可用时才累计。
		st, d := m.checkNoBalance(ctx, cfg, pat)
		switch st {
		case HealthOK:
			if cfg.FailCount > 0 {
				m.log.Info("账号替换: 账号无余额但实例可用，失败计数归零",
					"instance_id", id, "token_id", cfg.TokenID, "detail", d)
			}
			_ = m.srv.Store.UpdateFailoverState(id, 0, HealthOK, d, m.now(), "")
		case HealthBanned, HealthNoBalance:
			m.accumulate(ctx, cfg, st, d)
		default:
			m.log.Warn("账号替换: 实例检测出错（不计入失败）",
				"instance_id", id, "token_id", cfg.TokenID, "detail", d)
			_ = m.srv.Store.UpdateFailoverState(id, cfg.FailCount, "error", d, m.now(), "")
		}
	default:
		// HealthError：检测本身失败，不构成「账号不可用」的证据，不累计失败。
		m.log.Warn("账号替换: 账号检测出错（不计入失败）",
			"instance_id", id, "token_id", cfg.TokenID, "detail", detail)
		_ = m.srv.Store.UpdateFailoverState(id, cfg.FailCount, "error", detail, m.now(), "")
	}
}

// usableInstanceStatuses 列出「实例可用」的状态：除 running 外还包含 booting/
// rebooting/provisioning/migrating 等瞬态，避免把正在启动/迁移中的实例误判为
// 不可用而多计失败。
var usableInstanceStatuses = []string{"running", "booting", "rebooting", "provisioning", "migrating"}

// isUsableInstanceStatus 报告状态是否属于「可用」集合。
func isUsableInstanceStatus(status string) bool {
	for _, s := range usableInstanceStatuses {
		if status == s {
			return true
		}
	}
	return false
}

// checkNoBalance 处理「账号无余额」：资金预警本身不算故障，只有实例确实不可用
// 才计失败。返回判定状态与可读详情：
//   - HealthOK：实例处于可用状态（running/booting 等瞬态），或 stopped 但能成功
//     启动 → 可用，不累计；
//   - HealthBanned / HealthNoBalance：实例不可用（无法启动，或读取/启动实例时报
//     账号不可用）→ 累计（后续 replace 使用组预填配置建机）；
//   - HealthError：其他读取/启动失败（瞬时 5xx、网络抖动等）→ 检测本身失败，
//     不累计，等待下一轮。
//
// Boot 在 Linode 侧是异步任务，只要调用成功即视为「能启动」，不等待状态变为
// running，避免把宽限期中的正常实例误判为不可用。
func (m *FailoverMonitor) checkNoBalance(ctx context.Context, cfg store.Failover, pat string) (string, string) {
	inst, err := m.clientFor(pat).GetInstance(ctx, cfg.InstanceID)
	if err != nil {
		if ClassifyAccountError(err) == HealthBanned {
			return HealthBanned, "账号不可用: " + err.Error()
		}
		return HealthError, "账号无余额且读取实例失败: " + err.Error()
	}
	if isUsableInstanceStatus(inst.Status) {
		return HealthOK, "账号无余额但实例状态 " + inst.Status + "，视为可用"
	}
	// 实例未运行：尝试启动；能启动即视为可用。
	if err := m.clientFor(pat).Boot(ctx, inst.ID); err != nil {
		switch ClassifyAccountError(err) {
		case HealthBanned:
			return HealthBanned, "账号不可用: " + err.Error()
		case HealthNoBalance:
			// 欠费导致无法启动：实例确实不可用，按 no_balance 累计。
			return HealthNoBalance, "账号无余额且实例无法启动: " + err.Error()
		default:
			// 瞬时 5xx / 网络抖动：无法归类，不累计，等待下一轮。
			return HealthError, "启动实例失败: " + err.Error()
		}
	}
	return HealthOK, "账号无余额，实例已尝试启动，视为可用"
}

// accumulate 在账号/实例确不可用时累计失败次数，达阈值触发替换；阈值取自配置，
// 缺省用 DefaultFailoverThreshold。
func (m *FailoverMonitor) accumulate(ctx context.Context, cfg store.Failover, status, detail string) {
	id := cfg.InstanceID
	fails := cfg.FailCount + 1
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = DefaultFailoverThreshold
	}
	m.log.Info("账号替换: 账号不可用",
		"instance_id", id, "token_id", cfg.TokenID,
		"status", status, "fails", fails, "threshold", threshold, "detail", detail)
	if fails < threshold {
		_ = m.srv.Store.UpdateFailoverState(id, fails, status, detail, m.now(), "")
		return
	}
	m.replace(ctx, cfg, status, detail)
}

// replace 在账号组内另选一个健康账号，用组预填的目标开机配置创建替换实例，
// 并把替换配置迁移到新实例。过程中旧实例的清理是尽力而为，失败不影响替换成功。
//
// 严格模式：不再读取旧实例配置，而是直接读取组内预填的 InstanceConfig。
// 若组未配置目标开机配置，则不触发替换，状态记 error，详情为「未填写账号组目标开机配置」。
func (m *FailoverMonitor) replace(ctx context.Context, cfg store.Failover, status, detail string) {
	id := cfg.InstanceID
	m.log.Info("账号替换: 达到阈值，开始替换",
		"instance_id", id, "token_id", cfg.TokenID, "status", status, "detail", detail)

	oldPAT, err := m.srv.Store.TokenPAT(cfg.UserID, cfg.TokenID)
	if err != nil {
		// 理论上 check 已取过一次；这里兜底，重置计数等待下一轮。
		_ = m.srv.Store.UpdateFailoverState(id, 0, "error",
			"取账号凭证失败: "+err.Error(), m.now(), "")
		return
	}

	g, ok := m.srv.Store.AccountGroup(cfg.UserID, cfg.GroupID)
	if !ok {
		m.log.Warn("账号替换: 账号组不存在，停止替换", "instance_id", id, "group_id", cfg.GroupID)
		_ = m.srv.Store.UpdateFailoverState(id, 0, "error",
			"账号组不存在，已停止替换", m.now(), "")
		return
	}

	// 读取组预填目标开机配置；严格模式下必须有配置才继续。
	cfgJSON, hasConfig := m.srv.Store.InstanceConfig(cfg.UserID, cfg.GroupID)
	if !hasConfig || cfgJSON == nil {
		m.log.Warn("账号替换: 组未填写目标开机配置，停止替换",
			"instance_id", id, "group_id", cfg.GroupID)
		_ = m.srv.Store.UpdateFailoverState(id, 0, "error",
			"未填写账号组目标开机配置", m.now(), "")
		return
	}

	// 候选：组内成员中排除故障账号自身，按升序轮选；仅探测为 HealthOK 的可用。
	candidates := make([]int, 0, len(g.TokenIDs))
	for _, tid := range g.TokenIDs {
		if tid != cfg.TokenID {
			candidates = append(candidates, tid)
		}
	}
	newTokenID, newPAT := 0, ""
	for i := range candidates {
		tid, _ := NextReplacement(candidates, i, cfg.TokenID)
		tpat, err := m.srv.Store.TokenPAT(cfg.UserID, tid)
		if err != nil {
			m.log.Warn("账号替换: 读取候选账号凭证失败", "instance_id", id, "token_id", tid, "error", err)
			continue
		}
		st, sd := m.probe(ctx, tpat)
		if st == HealthOK {
			newTokenID, newPAT = tid, tpat
			m.log.Info("账号替换: 选中候选账号",
				"instance_id", id, "from_token", cfg.TokenID, "to_token", tid, "detail", sd)
			break
		}
		m.log.Info("账号替换: 候选账号不可用",
			"instance_id", id, "token_id", tid, "status", st, "detail", sd)
	}
	if newTokenID <= 0 {
		m.log.Warn("账号替换: 组内没有可用账号，等待下一轮", "instance_id", id, "group_id", cfg.GroupID)
		_ = m.srv.Store.UpdateFailoverState(id, 0, "error",
			"组内没有可用账号，等待下一轮", m.now(), "")
		return
	}

	// 按组预填配置创建替换实例。
	booted := true
	if cfgJSON.Booted != nil {
		booted = *cfgJSON.Booted
	}
	m.log.Info("账号替换: 在新账号创建替换实例",
		"instance_id", id, "from_token", cfg.TokenID, "to_token", newTokenID,
		"region", cfgJSON.Region, "type", cfgJSON.Type, "image", cfgJSON.Image, "label", cfgJSON.Label)
	// 若旧实例保存过 root 密码，替换实例沿用同一密码。
	rootPass, _ := m.srv.Store.InstanceRootPassword(id)
	newInst, err := m.clientFor(newPAT).CreateInstance(ctx, linode.CreateOptions{
		Region:   cfgJSON.Region,
		Type:     cfgJSON.Type,
		Image:    cfgJSON.Image,
		Label:    cfgJSON.Label,
		RootPass: rootPass,
		Booted:   &booted,
	})
	if err != nil {
		m.log.Warn("账号替换: 在新账号创建实例失败",
			"instance_id", id, "to_token", newTokenID, "error", err)
		// 计数归零，避免对同一个目标无限重试；下一轮重新选账号。
		_ = m.srv.Store.UpdateFailoverState(id, 0, "error",
			"在新账号创建实例失败: "+err.Error(), m.now(), "")
		return
	}
	newID := newInst.ID
	m.log.Info("账号替换: 替换实例已创建",
		"instance_id", id, "from_token", cfg.TokenID, "to_token", newTokenID, "new_instance_id", newID)

	// 默认开放所有端口：绑定失败不阻塞替换。
	region := newInst.Region
	if region == "" {
		region = cfgJSON.Region
	}
	if err := m.clientFor(newPAT).AttachAllowAllFirewall(ctx, region, newID); err != nil {
		m.log.Warn("账号替换: 开放所有端口防火墙绑定失败（继续）",
			"new_instance_id", newID, "error", err)
	}

	// 把组预填的开机脚本写入新实例（空脚本则跳过）。
	if script := strings.TrimSpace(cfgJSON.Script); script != "" {
		if err := m.srv.Store.SetInstanceScript(newID, script); err != nil {
			m.log.Warn("账号替换: 写入开机脚本失败", "to", newID, "error", err)
		}
	}
	// 保存 root 密码到替换实例，供后续重建继续继承。
	if rootPass != "" {
		if err := m.srv.Store.SetInstanceRootPassword(newID, rootPass); err != nil {
			m.log.Warn("账号替换: 保存 root 密码失败", "to", newID, "error", err)
		}
	}

	// 迁移替换配置到新实例，并把承载账号改成候选账号（否则下一轮会继续探测
	// 故障的旧账号）。MoveFailover 保留设置项、重置状态；SetFailover 只改设置项。
	if err := m.srv.Store.MoveFailover(id, newID); err != nil {
		m.log.Warn("账号替换: 迁移替换配置失败", "from", id, "to", newID, "error", err)
	} else {
		threshold := cfg.Threshold
		if threshold <= 0 {
			threshold = DefaultFailoverThreshold
		}
		interval := cfg.IntervalSec
		if interval <= 0 {
			interval = int(DefaultFailoverInterval / time.Second)
		}
		_ = m.srv.Store.SetFailover(store.Failover{
			InstanceID:  newID,
			UserID:      cfg.UserID,
			GroupID:     cfg.GroupID,
			TokenID:     newTokenID,
			Enabled:     cfg.Enabled,
			Threshold:   threshold,
			IntervalSec: interval,
		})
	}

	label := m.candidateLabel(cfg.UserID, newTokenID)
	m.log.Info("账号替换: 已完成替换",
		"instance_id", id, "from_token", cfg.TokenID, "to_token", newTokenID,
		"new_instance_id", newID, "candidate", label)
	_ = m.srv.Store.UpdateFailoverState(newID, 0, HealthOK,
		"已替换到账号 "+label+" 的新实例 #"+strconv.Itoa(newID), m.now(), "")

	// 尽力清理旧实例；账号封号时本来就可能不可达，失败不影响替换结果。
	if err := m.clientFor(oldPAT).Shutdown(ctx, id); err != nil {
		m.log.Warn("账号替换: 关闭旧实例失败（不影响替换）", "instance_id", id, "error", err)
	}
	if err := m.clientFor(oldPAT).DeleteInstance(ctx, id); err != nil {
		m.log.Warn("账号替换: 删除旧实例失败（不影响替换）", "instance_id", id, "error", err)
	}
}

// candidateLabel 返回候选账号(Token)的显示名；取不到时退回 token id。
func (m *FailoverMonitor) candidateLabel(userID, tokenID int) string {
	for _, t := range m.srv.Store.TokensOf(userID) {
		if t.ID == tokenID {
			return t.Label
		}
	}
	return strconv.Itoa(tokenID)
}
