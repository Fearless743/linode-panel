package store

import "time"

// 自愈模式的取值。一个账号同一时间只有一种被墙处理策略：
//   - off     关闭
//   - rebuild 被墙后删机重建换 IP（保留一台实例）
//   - dual    双机轮询替换（保留 A/B 两台，被墙时轮换）
const (
	SelfHealOff     = "off"
	SelfHealRebuild = "rebuild"
	SelfHealDual    = "dual"
)

// SelfHeal 是账号级「被墙自愈」的统一视图。
// 设置项（模式/检测参数/脚本/root 密码）都保存在 account_config 一行里；
// 运行态按模式区分：dual 用账号级 dual_* 字段，rebuild 用每个实例的
// instance_config 字段（通过 Instances 暴露）。
type SelfHeal struct {
	TokenID int    `json:"token_id"`
	UserID  int    `json:"user_id"`
	Mode    string `json:"mode"`

	Enabled     bool `json:"enabled"`
	Port        int  `json:"port"`
	Threshold   int  `json:"threshold"`
	IntervalSec int  `json:"interval_seconds"`

	Script      string `json:"script"`
	HasRootPass bool   `json:"has_root_password"`

	// dual 模式下的 A/B 指针与账号级运行态。
	ActiveInstanceID  int    `json:"active_instance_id,omitempty"`
	StandbyInstanceID int    `json:"standby_instance_id,omitempty"`
	State             string `json:"state,omitempty"`
	FailCount         int    `json:"fail_count,omitempty"`
	LastStatus        string `json:"last_status,omitempty"`
	LastDetail        string `json:"last_detail,omitempty"`
	LastCheckAt       string `json:"last_check_at,omitempty"`

	// rebuild 模式下每个实例的运行态。
	Instances []SelfHealInstance `json:"instances,omitempty"`

	UpdatedAt string `json:"updated_at"`
}

// SelfHealInstance 是 rebuild 模式下单个实例的探测状态。
type SelfHealInstance struct {
	CacheID       int    `json:"-"`
	InstanceID    int    `json:"instance_id"`
	FailCount     int    `json:"fail_count"`
	LastStatus    string `json:"last_status"`
	LastDetail    string `json:"last_detail"`
	LastCheckAt   string `json:"last_check_at"`
	LastBlockedAt string `json:"last_blocked_at"`
	LastTaskID    string `json:"last_task_id"`
}

func normalizeMode(mode string) string {
	switch mode {
	case SelfHealRebuild, SelfHealDual:
		return mode
	default:
		return SelfHealOff
	}
}

// ensureHealModeColumn 给老库补 heal_mode 列，并根据旧的开关推断初始模式，
// 使升级后原来的自动换 IP / 双机配置继续生效。
func (s *Store) ensureHealModeColumn() error {
	has, err := s.columnExists("account_config", "heal_mode")
	if err != nil {
		return err
	}
	if !has {
		if _, err := s.db.Exec(`ALTER TABLE account_config ADD COLUMN heal_mode TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return s.backfillHealMode()
}

// backfillHealMode 按旧开关为尚未设置模式的行推断模式（升级老库用）。
func (s *Store) backfillHealMode() error {
	if _, err := s.db.Exec(`UPDATE account_config SET heal_mode=?
		WHERE heal_mode='' AND (dual_enabled!=0 OR dual_active_instance_id!=0 OR dual_standby_instance_id!=0)`, SelfHealDual); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE account_config SET heal_mode=?
		WHERE heal_mode='' AND (auto_enabled!=0 OR has_auto!=0)`, SelfHealRebuild)
	return err
}

// scanSelfHealLocked 从 account_config 行读取统一视图的公共部分。
func (s *Store) scanSelfHealLocked(tokenID int) (SelfHeal, bool) {
	row := s.db.QueryRow(`SELECT token_id, user_id, script, has_script, has_password,
		COALESCE(heal_mode,''), auto_enabled, auto_port, auto_threshold, auto_interval,
		dual_enabled, dual_port, dual_threshold, dual_interval,
		dual_active_instance_id, dual_standby_instance_id, dual_state, dual_fail_count,
		dual_last_status, dual_last_detail, dual_last_check_at, updated_at
		FROM account_config WHERE token_id=?`, tokenID)
	var c SelfHeal
	var hasS, hasP, autoEn, dualEn int
	var mode string
	var autoPort, autoTh, autoIv, dualPort, dualTh, dualIv int
	if err := row.Scan(&c.TokenID, &c.UserID, &c.Script, &hasS, &hasP, &mode, &autoEn,
		&autoPort, &autoTh, &autoIv, &dualEn, &dualPort, &dualTh, &dualIv,
		&c.ActiveInstanceID, &c.StandbyInstanceID, &c.State, &c.FailCount,
		&c.LastStatus, &c.LastDetail, &c.LastCheckAt, &c.UpdatedAt); err != nil {
		return SelfHeal{}, false
	}
	c.Mode = normalizeMode(mode)
	c.HasRootPass = hasP != 0
	// 检测端口/阈值/间隔只有一个有效值：rebuild 用 auto_*，dual 用 dual_*。
	if c.Mode == SelfHealDual {
		c.Enabled = dualEn != 0
		c.Port, c.Threshold, c.IntervalSec = dualPort, dualTh, dualIv
	} else {
		c.Enabled = autoEn != 0
		c.Port, c.Threshold, c.IntervalSec = autoPort, autoTh, autoIv
	}
	return c, true
}

// SelfHealConfigOf 返回处于 dual 模式的账号配置，供双机轮换逻辑使用。
func (s *Store) SelfHealConfigOf(tokenID int) (SelfHeal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.scanSelfHealLocked(tokenID)
	if !ok || c.Mode != SelfHealDual {
		return SelfHeal{}, false
	}
	return c, true
}

// SelfHealOf 返回一个账号的统一自愈配置（从未配置过时 mode=off）。
func (s *Store) SelfHealOf(tokenID int) SelfHeal {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.scanSelfHealLocked(tokenID)
	if !ok {
		return SelfHeal{TokenID: tokenID, Mode: SelfHealOff}
	}
	if c.Mode == SelfHealRebuild {
		c.Instances = s.selfHealInstancesLocked(tokenID)
	}
	return c
}

// SelfHeals 返回所有已配置（mode != off）的账号自愈配置。
func (s *Store) SelfHeals() []SelfHeal {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT token_id FROM account_config
		WHERE COALESCE(heal_mode,'') IN (?,?) ORDER BY token_id`, SelfHealRebuild, SelfHealDual)
	if err != nil {
		return nil
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	out := make([]SelfHeal, 0, len(ids))
	for _, id := range ids {
		c, ok := s.scanSelfHealLocked(id)
		if !ok {
			continue
		}
		if c.Mode == SelfHealRebuild {
			c.Instances = s.selfHealInstancesLocked(id)
		}
		out = append(out, c)
	}
	return out
}

func (s *Store) selfHealInstancesLocked(tokenID int) []SelfHealInstance {
	rows, err := s.db.Query(`SELECT c.id, c.instance_id,
		COALESCE(i.fail_count,0), COALESCE(i.last_status,''), COALESCE(i.last_detail,''),
		COALESCE(i.last_check_at,''), COALESCE(i.last_blocked_at,''), COALESCE(i.last_task_id,'')
		FROM instance_cache c
		LEFT JOIN instance_config i ON i.cache_id=c.id
		WHERE c.token_id=? AND c.alive=1 ORDER BY c.instance_id, c.id`, tokenID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SelfHealInstance
	for rows.Next() {
		var inst SelfHealInstance
		if err := rows.Scan(&inst.CacheID, &inst.InstanceID, &inst.FailCount, &inst.LastStatus,
			&inst.LastDetail, &inst.LastCheckAt, &inst.LastBlockedAt, &inst.LastTaskID); err == nil {
			out = append(out, inst)
		}
	}
	return out
}

// SetSelfHeal 写入账号的统一自愈配置。模式互斥：切到某模式时会清掉另一种模式
// 的启用开关，避免被墙时两条路径同时删机。
func (s *Store) SetSelfHeal(c SelfHeal, rootPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureAccountRowLocked(c.TokenID, c.UserID); err != nil {
		return err
	}
	mode := normalizeMode(c.Mode)
	if c.Port <= 0 {
		c.Port = 22
	}
	if c.Threshold <= 0 {
		c.Threshold = 3
	}
	if c.IntervalSec <= 0 {
		c.IntervalSec = 300
	}
	en := 0
	if c.Enabled && mode != SelfHealOff {
		en = 1
	}

	// 已保存的脚本/密码：显式提供才覆盖，留空沿用旧值。
	var oldScript, encPass string
	var hasS, hasP int
	_ = s.db.QueryRow(`SELECT script, root_password, has_script, has_password FROM account_config WHERE token_id=?`, c.TokenID).
		Scan(&oldScript, &encPass, &hasS, &hasP)
	script, scriptFlag := oldScript, hasS
	if c.Script != "" {
		script, scriptFlag = c.Script, 1
	}
	if rootPassword != "" {
		enc, err := s.encryptSecret(rootPassword)
		if err != nil {
			return err
		}
		encPass, hasP = enc, 1
	}

	now := time.Now().UTC().Format(time.RFC3339)
	autoEn, dualEn := 0, 0
	hasAuto := 0
	active, standby := c.ActiveInstanceID, c.StandbyInstanceID
	if mode == SelfHealRebuild {
		autoEn, hasAuto = en, 1
		active, standby = 0, 0
	} else if mode == SelfHealDual {
		dualEn = en
	}
	_, err := s.db.Exec(`UPDATE account_config SET
		heal_mode=?, user_id=?, script=?, has_script=?, root_password=?, has_password=?,
		auto_enabled=?, has_auto=?, auto_port=?, auto_threshold=?, auto_interval=?,
		dual_enabled=?, dual_port=?, dual_threshold=?, dual_interval=?,
		dual_active_instance_id=?, dual_standby_instance_id=?, updated_at=?
		WHERE token_id=?`,
		mode, c.UserID, script, scriptFlag, encPass, hasP,
		autoEn, hasAuto, c.Port, c.Threshold, c.IntervalSec,
		dualEn, c.Port, c.Threshold, c.IntervalSec,
		active, standby, now, c.TokenID)
	return err
}

// DeleteSelfHeal 关闭自愈并清空双机指针与运行态。
func (s *Store) DeleteSelfHeal(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET
		heal_mode=?, auto_enabled=0, has_auto=0,
		dual_enabled=0, dual_active_instance_id=0, dual_standby_instance_id=0,
		dual_state='idle', dual_fail_count=0, dual_last_status='', dual_last_detail='',
		dual_last_check_at='', dual_last_blocked_at='', updated_at=?
		WHERE token_id=?`, SelfHealOff, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// UpdateSelfHealState 记录 dual 模式的账号级运行态。
func (s *Store) UpdateSelfHealState(tokenID, failCount int, state, status, detail, checkAt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET dual_fail_count=?,
		dual_state=CASE WHEN ?='' THEN dual_state ELSE ? END,
		dual_last_status=CASE WHEN ?='' THEN dual_last_status ELSE ? END,
		dual_last_detail=CASE WHEN ?='' THEN dual_last_detail ELSE ? END,
		dual_last_check_at=CASE WHEN ?='' THEN dual_last_check_at ELSE ? END,
		updated_at=? WHERE token_id=?`,
		failCount, state, state, status, status, detail, detail, checkAt, checkAt,
		time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// UpdateSelfHealInstances 更新 dual 模式的 A/B 指针。
func (s *Store) UpdateSelfHealInstances(tokenID, activeID, standbyID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET dual_active_instance_id=?, dual_standby_instance_id=?, updated_at=? WHERE token_id=?`,
		activeID, standbyID, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}
