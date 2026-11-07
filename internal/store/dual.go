package store

import "time"

// DualConfig 是账号维度的双机轮询配置与运行状态。
//
// 一个账号只管理一对 A/B 实例：ActiveInstanceID 为当前使用中实例，
// StandbyInstanceID 为闲置备用实例（关机）。脚本与 root 密码按账号保存，
// 不再按实例保存：启用时从模板实例继承，之后在账号维度编辑。
type DualConfig struct {
	TokenID           int    `json:"token_id"`
	UserID            int    `json:"user_id"`
	Enabled           bool   `json:"enabled"`
	Port              int    `json:"port"`
	Threshold         int    `json:"threshold"`
	IntervalSec       int    `json:"interval_seconds"`
	ActiveInstanceID  int    `json:"active_instance_id"`
	StandbyInstanceID int    `json:"standby_instance_id"`
	State             string `json:"state"` // idle | provisioning | rotating | rebuilding_standby | error
	FailCount         int    `json:"fail_count"`
	LastStatus        string `json:"last_status"`
	LastDetail        string `json:"last_detail"`
	LastCheckAt       string `json:"last_check_at"`
	// Script 与 RootPassword 按账号保存（RootPassword 返回明文，存储时加密）。
	Script       string `json:"script"`
	HasRootPass  bool   `json:"has_root_password"`
	UpdatedAt    string `json:"updated_at"`
}

const dualCols = `token_id, user_id, enabled, port, threshold, interval_sec,
	active_instance_id, standby_instance_id, state, fail_count, last_status,
	last_detail, last_check_at, script, root_password, updated_at`

func scanDualConfig(sc interface{ Scan(...any) error }) (DualConfig, string, error) {
	var (
		d       DualConfig
		enabled int
		encPass string
	)
	if err := sc.Scan(
		&d.TokenID, &d.UserID, &enabled, &d.Port, &d.Threshold, &d.IntervalSec,
		&d.ActiveInstanceID, &d.StandbyInstanceID, &d.State, &d.FailCount,
		&d.LastStatus, &d.LastDetail, &d.LastCheckAt, &d.Script, &encPass, &d.UpdatedAt,
	); err != nil {
		return DualConfig{}, "", err
	}
	d.Enabled = enabled != 0
	d.HasRootPass = encPass != ""
	return d, encPass, nil
}

// SetDualConfig upsert 账号的双机配置（状态字段保留，只更新设置项与脚本）。
// 已存在行时也同步更新 A/B 指针：handlePutDual 传入的指针即期望值，
// 允许换主/换备与迁移后重绑；不传指针的调用方应先读旧值再传入（现有调用点均如此）。
func (s *Store) SetDualConfig(d DualConfig, rootPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	enabled := 0
	if d.Enabled {
		enabled = 1
	}
	encPass := ""
	if rootPassword != "" {
		enc, err := s.encryptSecret(rootPassword)
		if err != nil {
			return err
		}
		encPass = enc
	} else {
		// 保留已有密码：读出旧值。
		_ = s.db.QueryRow(`SELECT root_password FROM account_dual WHERE token_id=?`, d.TokenID).Scan(&encPass)
	}
	_, err := s.db.Exec(
		`INSERT INTO account_dual
		   (token_id, user_id, enabled, port, threshold, interval_sec,
		    active_instance_id, standby_instance_id, script, root_password, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(token_id) DO UPDATE SET
		   user_id=excluded.user_id, enabled=excluded.enabled, port=excluded.port,
		   threshold=excluded.threshold, interval_sec=excluded.interval_sec,
		   active_instance_id=excluded.active_instance_id,
		   standby_instance_id=excluded.standby_instance_id,
		   script=excluded.script, root_password=excluded.root_password,
		   updated_at=excluded.updated_at`,
		d.TokenID, d.UserID, enabled, d.Port, d.Threshold, d.IntervalSec,
		d.ActiveInstanceID, d.StandbyInstanceID, d.Script, encPass,
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// DualConfigOf 返回某账号的双机配置。
func (s *Store) DualConfigOf(tokenID int) (DualConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT `+dualCols+` FROM account_dual WHERE token_id=?`, tokenID)
	d, _, err := scanDualConfig(row)
	if err != nil {
		return DualConfig{}, false
	}
	return d, true
}

// DualConfigs 返回所有双机配置。
func (s *Store) DualConfigs() []DualConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + dualCols + ` FROM account_dual ORDER BY token_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []DualConfig
	for rows.Next() {
		d, _, err := scanDualConfig(rows)
		if err != nil {
			break
		}
		out = append(out, d)
	}
	return out
}

// DualRootPassword 返回账号保存的 root 密码明文。
func (s *Store) DualRootPassword(tokenID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var enc string
	if err := s.db.QueryRow(`SELECT root_password FROM account_dual WHERE token_id=?`, tokenID).Scan(&enc); err != nil || enc == "" {
		return "", false
	}
	plain, err := s.decryptSecret(enc)
	if err != nil || plain == "" {
		return "", false
	}
	return plain, true
}

// UpdateDualInstances 更新双机对的 A/B 实例 id（轮换时调用）。
func (s *Store) UpdateDualInstances(tokenID, activeID, standbyID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE account_dual SET active_instance_id=?, standby_instance_id=?, updated_at=? WHERE token_id=?`,
		activeID, standbyID, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// UpdateDualState 记录双机最近一次检查/轮换状态。
func (s *Store) UpdateDualState(tokenID, failCount int, state, status, detail, checkAt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE account_dual SET
		   fail_count=?,
		   state=CASE WHEN ?='' THEN state ELSE ? END,
		   last_status=CASE WHEN ?='' THEN last_status ELSE ? END,
		   last_detail=CASE WHEN ?='' THEN last_detail ELSE ? END,
		   last_check_at=CASE WHEN ?='' THEN last_check_at ELSE ? END,
		   updated_at=?
		 WHERE token_id=?`,
		failCount,
		state, state,
		status, status,
		detail, detail,
		checkAt, checkAt,
		time.Now().UTC().Format(time.RFC3339),
		tokenID,
	)
	return err
}

// RepointDualInstances 把双机配置中指向 oldID 的实例 id 改为 newID（删机重建后调用）。
// 仅更新 active/standby 指针，不改变其它设置；无匹配行时无操作。
func (s *Store) RepointDualInstances(oldID, newID int) error {
	if oldID <= 0 || newID <= 0 || oldID == newID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE account_dual SET active_instance_id=? WHERE active_instance_id=?`, newID, oldID); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`UPDATE account_dual SET standby_instance_id=? WHERE standby_instance_id=?`, newID, oldID); err != nil {
		return err
	}
	return nil
}

// DeleteDualConfig 删除账号的双机配置（两台实例本身保留）。
func (s *Store) DeleteDualConfig(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM account_dual WHERE token_id=?`, tokenID)
	return err
}

// DualInstanceRole 返回实例在某账号双机对中的角色："active"/"standby"/""。
func (s *Store) DualInstanceRole(tokenID, instanceID int) string {
	if d, ok := s.DualConfigOf(tokenID); ok {
		if d.ActiveInstanceID == instanceID && instanceID > 0 {
			return "active"
		}
		if d.StandbyInstanceID == instanceID && instanceID > 0 {
			return "standby"
		}
	}
	return ""
}

// ClearDualInstance 清空所有双机配置中指向 instanceID 的指针（实例被删后调用）。
func (s *Store) ClearDualInstance(instanceID int) error {
	if instanceID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`UPDATE account_dual SET active_instance_id=0 WHERE active_instance_id=?`, instanceID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE account_dual SET standby_instance_id=0 WHERE standby_instance_id=?`, instanceID)
	return err
}

// ReassignDualInstance 把 source 账号双机对中指向 oldID 的角色指针清空，
// 并在 target 账号已有双机对且对应角色为空时指向 newID（单实例跨账号迁移用）。
// target 无双机对时仅清空源指针。无匹配时无操作。
func (s *Store) ReassignDualInstance(sourceTokenID, targetTokenID, oldID, newID int) error {
	if sourceTokenID <= 0 || oldID <= 0 || newID <= 0 || oldID == newID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	read := func(tokenID int) (DualConfig, bool) {
		row := s.db.QueryRow(`SELECT `+dualCols+` FROM account_dual WHERE token_id=?`, tokenID)
		d, _, err := scanDualConfig(row)
		return d, err == nil
	}
	src, ok := read(sourceTokenID)
	if !ok {
		return nil
	}
	role := ""
	if src.ActiveInstanceID == oldID {
		src.ActiveInstanceID = 0
		role = "active"
	} else if src.StandbyInstanceID == oldID {
		src.StandbyInstanceID = 0
		role = "standby"
	} else {
		return nil
	}
	if _, err := s.db.Exec(
		`UPDATE account_dual SET active_instance_id=?, standby_instance_id=?, updated_at=? WHERE token_id=?`,
		src.ActiveInstanceID, src.StandbyInstanceID,
		time.Now().UTC().Format(time.RFC3339), sourceTokenID); err != nil {
		return err
	}
	if targetTokenID <= 0 || targetTokenID == sourceTokenID {
		return nil
	}
	dst, ok := read(targetTokenID)
	if !ok {
		return nil
	}
	updated := false
	if role == "active" && dst.ActiveInstanceID == 0 {
		dst.ActiveInstanceID = newID
		updated = true
	}
	if role == "standby" && dst.StandbyInstanceID == 0 {
		dst.StandbyInstanceID = newID
		updated = true
	}
	if updated {
		_, err := s.db.Exec(
			`UPDATE account_dual SET active_instance_id=?, standby_instance_id=?, updated_at=? WHERE token_id=?`,
			dst.ActiveInstanceID, dst.StandbyInstanceID,
			time.Now().UTC().Format(time.RFC3339), targetTokenID)
		return err
	}
	return nil
}
