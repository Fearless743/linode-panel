package store

import (
	"database/sql"
	"errors"
	"time"
)

// AccountConfig 是账号级配置（替代原单实例 script / root password / auto-rebuild）。
// 同账号下所有实例共用；实例表 instance_cache 仅为缓存，不再存配置。
type AccountConfig struct {
	TokenID    int    `json:"token_id"`
	UserID     int    `json:"user_id"`
	Script     string `json:"script"`
	HasScript  bool   `json:"has_script"`
	HasPass    bool   `json:"has_root_password"`
	Enabled    bool   `json:"auto_enabled"`
	Port       int    `json:"auto_port"`
	Threshold  int    `json:"auto_threshold"`
	IntervalSec int    `json:"auto_interval_seconds"`
	UpdatedAt  string `json:"updated_at"`
	// Dual enabled is a switch inside account config (single place)
	DualEnabled bool `json:"dual_enabled"`
	DualActiveInstanceID int `json:"dual_active_instance_id,omitempty"`
	DualStandbyInstanceID int `json:"dual_standby_instance_id,omitempty"`
	DualState string `json:"dual_state,omitempty"`
	DualFailCount int `json:"dual_fail_count,omitempty"`
	DualLastStatus string `json:"dual_last_status,omitempty"`
	DualLastDetail string `json:"dual_last_detail,omitempty"`
	DualLastCheckAt string `json:"dual_last_check_at,omitempty"`
	DualPort int `json:"dual_port,omitempty"`
	DualThreshold int `json:"dual_threshold,omitempty"`
	DualIntervalSec int `json:"dual_interval_seconds,omitempty"`
}

const accountConfigCols = `token_id, user_id, script, has_script, has_password,
	auto_enabled, auto_port, auto_threshold, auto_interval, updated_at`

// ensureAccountConfigTable 创建账号级表，并把原单实例配置一次性迁移过去。
func (s *Store) ensureAccountConfigTable() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS account_config (
		token_id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL DEFAULT 0,
		script TEXT NOT NULL DEFAULT '',
		has_script INTEGER NOT NULL DEFAULT 0,
		root_password TEXT NOT NULL DEFAULT '',
		has_password INTEGER NOT NULL DEFAULT 0,
		auto_enabled INTEGER NOT NULL DEFAULT 0,
		auto_port INTEGER NOT NULL DEFAULT 22,
		auto_threshold INTEGER NOT NULL DEFAULT 3,
		auto_interval INTEGER NOT NULL DEFAULT 300,
		updated_at TEXT NOT NULL DEFAULT '',
		has_auto INTEGER NOT NULL DEFAULT 0,
		dual_enabled INTEGER NOT NULL DEFAULT 0,
		dual_port INTEGER NOT NULL DEFAULT 22,
		dual_threshold INTEGER NOT NULL DEFAULT 3,
		dual_interval INTEGER NOT NULL DEFAULT 300,
		dual_active_instance_id INTEGER NOT NULL DEFAULT 0,
		dual_standby_instance_id INTEGER NOT NULL DEFAULT 0,
		dual_state TEXT NOT NULL DEFAULT 'idle',
		dual_fail_count INTEGER NOT NULL DEFAULT 0,
		dual_last_status TEXT NOT NULL DEFAULT '',
		dual_last_detail TEXT NOT NULL DEFAULT '',
		dual_last_check_at TEXT NOT NULL DEFAULT '',
		dual_last_blocked_at TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return err
	}
	// has_auto 区分“已配置但关闭”与“从未配置”：旧表无此列，老库补列。
	hasAuto, err := s.columnExists("account_config", "has_auto")
	if err != nil {
		return err
	}
	if !hasAuto {
		if _, err := s.db.Exec(`ALTER TABLE account_config ADD COLUMN has_auto INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		// 从有自动换 IP 参数的旧单实例迁移行补 has_auto=1
		_, _ = s.db.Exec(`UPDATE account_config SET has_auto=1 WHERE auto_port!=0 OR auto_threshold!=0`)
		// 实际迁移行已写在下面 migrate 里；新表首次写入时 migrate 会带 has_auto，
		// 这里若表刚建且为空，回填无意义。
	}
	// dual_* old DBs without columns: add missing columns
	dualCols := []struct{ name, ddl string }{
		{"dual_enabled", `ALTER TABLE account_config ADD COLUMN dual_enabled INTEGER NOT NULL DEFAULT 0`},
		{"dual_port", `ALTER TABLE account_config ADD COLUMN dual_port INTEGER NOT NULL DEFAULT 22`},
		{"dual_threshold", `ALTER TABLE account_config ADD COLUMN dual_threshold INTEGER NOT NULL DEFAULT 3`},
		{"dual_interval", `ALTER TABLE account_config ADD COLUMN dual_interval INTEGER NOT NULL DEFAULT 300`},
		{"dual_active_instance_id", `ALTER TABLE account_config ADD COLUMN dual_active_instance_id INTEGER NOT NULL DEFAULT 0`},
		{"dual_standby_instance_id", `ALTER TABLE account_config ADD COLUMN dual_standby_instance_id INTEGER NOT NULL DEFAULT 0`},
		{"dual_state", `ALTER TABLE account_config ADD COLUMN dual_state TEXT NOT NULL DEFAULT 'idle'`},
		{"dual_fail_count", `ALTER TABLE account_config ADD COLUMN dual_fail_count INTEGER NOT NULL DEFAULT 0`},
		{"dual_last_status", `ALTER TABLE account_config ADD COLUMN dual_last_status TEXT NOT NULL DEFAULT ''`},
		{"dual_last_detail", `ALTER TABLE account_config ADD COLUMN dual_last_detail TEXT NOT NULL DEFAULT ''`},
		{"dual_last_check_at", `ALTER TABLE account_config ADD COLUMN dual_last_check_at TEXT NOT NULL DEFAULT ''`},
		{"dual_last_blocked_at", `ALTER TABLE account_config ADD COLUMN dual_last_blocked_at TEXT NOT NULL DEFAULT ''`},
	}
	for _, c := range dualCols {
		has, err := s.columnExists("account_config", c.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.Exec(c.ddl); err != nil {
				return err
			}
		}
	}
	// 清理孤儿账号配置：已删除 token 的配置行不再有归属。
	_, _ = s.db.Exec(`DELETE FROM account_config WHERE token_id NOT IN (SELECT id FROM tokens) AND token_id!=0`)
	// Merge old Dual (account_dual) into account_config single place.
	_ = s.mergeDualIntoAccountConfig()
	// Merge after deletion also skips orphans; ensure orphans from legacy table don't resurface.
	_, _ = s.db.Exec(`DELETE FROM account_config WHERE token_id NOT IN (SELECT id FROM tokens) AND token_id!=0`)
	// 已迁移则不再重复（以 token 行存在与否判断）
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM account_config`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	migrated, err := s.migrateInstanceConfigsToAccount()
	if err != nil {
		return err
	}
	_ = migrated
	return nil
}

func (s *Store) mergeDualIntoAccountConfig() error {
	has, err := s.tableExists("account_dual")
	if err != nil || !has {
		return nil
	}
	rows, err := s.db.Query(`SELECT token_id, user_id, enabled, port, threshold, interval_sec,
		active_instance_id, standby_instance_id, state, fail_count, last_status, last_detail, last_check_at, last_blocked_at
		FROM account_dual`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type d struct {
		tokenID, userID, enabled, port, th, iv, active, standby, fail int
		state, lastStatus, lastDetail, lastCheck, lastBlocked string
	}
	var list []d
	for rows.Next() {
		var dd d
		if err := rows.Scan(&dd.tokenID, &dd.userID, &dd.enabled, &dd.port, &dd.th, &dd.iv,
			&dd.active, &dd.standby, &dd.state, &dd.fail, &dd.lastStatus, &dd.lastDetail, &dd.lastCheck, &dd.lastBlocked); err != nil {
			continue
		}
		list = append(list, dd)
	}
	_ = rows.Close()
	for _, dd := range list {
		// Skip orphan tokens that no longer exist.
		var owned int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE id=?`, dd.tokenID).Scan(&owned)
		if owned == 0 {
			continue
		}
		if dd.port <= 0 {
			dd.port = 22
		}
		if dd.th <= 0 {
			dd.th = 3
		}
		if dd.iv <= 0 {
			dd.iv = 300
		}
		// Only fill dual fields when account row doesn't already carry active pointer,
		// to avoid overwriting newer account data on every open.
		var curActive int
		_ = s.db.QueryRow(`SELECT dual_active_instance_id FROM account_config WHERE token_id=?`, dd.tokenID).Scan(&curActive)
		if curActive != 0 {
			continue
		}
		_, _ = s.db.Exec(`INSERT INTO account_config
			(token_id, user_id, dual_enabled, dual_port, dual_threshold, dual_interval,
			 dual_active_instance_id, dual_standby_instance_id, dual_state, dual_fail_count,
			 dual_last_status, dual_last_detail, dual_last_check_at, dual_last_blocked_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(token_id) DO UPDATE SET dual_enabled=excluded.dual_enabled,
				dual_port=excluded.dual_port, dual_threshold=excluded.dual_threshold,
				dual_interval=excluded.dual_interval,
				dual_active_instance_id=excluded.dual_active_instance_id,
				dual_standby_instance_id=excluded.dual_standby_instance_id,
				dual_state=excluded.dual_state, dual_fail_count=excluded.dual_fail_count,
				dual_last_status=excluded.dual_last_status, dual_last_detail=excluded.dual_last_detail,
				dual_last_check_at=excluded.dual_last_check_at, dual_last_blocked_at=excluded.dual_last_blocked_at`,
			dd.tokenID, dd.userID, dd.enabled, dd.port, dd.th, dd.iv, dd.active, dd.standby, dd.state, dd.fail, dd.lastStatus, dd.lastDetail, dd.lastCheck, dd.lastBlocked)
	}
	return nil
}

// migrateInstanceConfigsToAccount 把 instance_config 的单实例配置按 token 搬到账号级。
// 同账号多实例冲突时取最小 cache_id 的那一行（稳定确定性）。
func (s *Store) migrateInstanceConfigsToAccount() (int, error) {
	rows, err := s.db.Query(`
		SELECT c.token_id, ic.user_id, ic.token_id, ic.script, ic.has_script,
			ic.password, ic.has_password, ic.enabled, ic.port, ic.threshold, ic.interval_sec, ic.has_auto_rebuild
		FROM instance_config ic
		JOIN instance_cache c ON c.id = ic.cache_id
		WHERE (ic.has_script=1 OR ic.has_password=1 OR ic.has_auto_rebuild=1)
		ORDER BY c.token_id, ic.cache_id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var migrated int
	type byToken struct {
		userID, tokID int
		script, password string
		hasScript, hasPass bool
		enabled bool
		port, threshold, interval int
		hasAuto bool
	}
	best := map[int]*byToken{}
	var order []int
	for rows.Next() {
		var cacheTok, userID, innerTok int
		var script, encPass string
		var hasScript, hasPass, enabled, hasAuto int
		var port, threshold, interval int
		if err := rows.Scan(&cacheTok, &userID, &innerTok, &script, &hasScript,
			&encPass, &hasPass, &enabled, &port, &threshold, &interval, &hasAuto); err != nil {
			continue
		}
		tok := innerTok
		if tok <= 0 {
			tok = cacheTok
		}
		cur, ok := best[tok]
		if !ok {
			best[tok] = &byToken{userID: userID, tokID: tok}
			order = append(order, tok)
			cur = best[tok]
			if hasScript == 1 {
				cur.script = script
				cur.hasScript = true
			}
			if hasPass == 1 {
				cur.password = encPass
				cur.hasPass = true
			}
			if hasAuto == 1 {
				cur.enabled = enabled != 0
				cur.port = port
				cur.threshold = threshold
				cur.interval = interval
				cur.hasAuto = true
			}
			continue
		}
		// 已有行则只补缺失项（保留首条）
		if hasScript == 1 && !cur.hasScript {
			cur.script = script
			cur.hasScript = true
		}
		if hasPass == 1 && !cur.hasPass {
			cur.password = encPass
			cur.hasPass = true
		}
		if hasAuto == 1 && !cur.hasAuto {
			cur.enabled = enabled != 0
			cur.port = port
			cur.threshold = threshold
			cur.interval = interval
			cur.hasAuto = true
		}
	}
	rows.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, tok := range order {
		b := best[tok]
		hasS, hasP := 0, 0
		if b.hasScript {
			hasS = 1
		}
		if b.hasPass {
			hasP = 1
		}
		en := 0
		if b.enabled {
			en = 1
		}
		port, th, iv := b.port, b.threshold, b.interval
		if port <= 0 {
			port = 22
		}
		if th <= 0 {
			th = 3
		}
		if iv <= 0 {
			iv = 300
		}
		hasA := 0
		if b.hasAuto {
			hasA = 1
		}
		if _, err := s.db.Exec(`INSERT INTO account_config
			(token_id, user_id, script, has_script, root_password, has_password,
			 auto_enabled, auto_port, auto_threshold, auto_interval, updated_at, has_auto)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(token_id) DO UPDATE SET
				user_id=excluded.user_id, script=excluded.script, has_script=excluded.has_script,
				root_password=excluded.root_password, has_password=excluded.has_password,
				auto_enabled=excluded.auto_enabled, auto_port=excluded.auto_port,
				auto_threshold=excluded.auto_threshold, auto_interval=excluded.auto_interval,
				updated_at=excluded.updated_at, has_auto=excluded.has_auto`,
			b.tokID, b.userID, b.script, hasS, b.password, hasP, en, port, th, iv, now, hasA); err != nil {
			return migrated, err
		}
		migrated++
	}
	// 迁移后清掉原单实例的 script/password 标识，避免后续再双写。
	// 注意：fail_count/last_* 等运行态保留在 instance_config 中配合账号参数使用。
	_, _ = s.db.Exec(`UPDATE instance_config SET has_script=0, script='', has_password=0, password=''`)
	return migrated, nil
}

func (s *Store) accountConfigLocked(tokenID int) (AccountConfig, string, bool) {
	row := s.db.QueryRow(`SELECT token_id, user_id, script, has_script, has_password,
		auto_enabled, auto_port, auto_threshold, auto_interval, updated_at,
		root_password, dual_enabled, dual_port, dual_threshold, dual_interval,
		dual_active_instance_id, dual_standby_instance_id, dual_state, dual_fail_count,
		dual_last_status, dual_last_detail, dual_last_check_at FROM account_config WHERE token_id=?`, tokenID)
	var c AccountConfig
	var enc string
	var hasS, hasP, en, dualEn int
	if err := row.Scan(&c.TokenID, &c.UserID, &c.Script, &hasS, &hasP, &en,
		&c.Port, &c.Threshold, &c.IntervalSec, &c.UpdatedAt, &enc,
		&dualEn, &c.DualPort, &c.DualThreshold, &c.DualIntervalSec,
		&c.DualActiveInstanceID, &c.DualStandbyInstanceID, &c.DualState, &c.DualFailCount,
		&c.DualLastStatus, &c.DualLastDetail, &c.DualLastCheckAt); err != nil {
		return AccountConfig{}, "", false
	}
	c.HasScript = hasS != 0
	c.HasPass = hasP != 0
	c.Enabled = en != 0
	c.DualEnabled = dualEn != 0
	plain := ""
	if enc != "" && c.HasPass {
		if p, err := s.decryptSecret(enc); err == nil {
			plain = p
		}
	}
	return c, plain, true
}

// tokenForInstanceLocked 返回实例所属 token：优先 alive 且非 0 的缓存行。
func (s *Store) tokenForInstanceLocked(instanceID int, ensure bool) (int, bool, error) {
	var token int
	err := s.db.QueryRow(`SELECT token_id FROM instance_cache
		WHERE instance_id=? AND alive=1 AND token_id!=0 ORDER BY id LIMIT 1`, instanceID).Scan(&token)
	if err == nil && token > 0 {
		return token, true, nil
	}
	err = s.db.QueryRow(`SELECT token_id FROM instance_cache WHERE instance_id=? ORDER BY id LIMIT 1`, instanceID).Scan(&token)
	if err == nil {
		return token, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if !ensure {
		return 0, false, nil
	}
	res, err := s.db.Exec(`INSERT INTO instance_cache (token_id, instance_id, fetched_at) VALUES(0, ?, '')`, instanceID)
	if err != nil {
		return 0, false, err
	}
	_ = res
	return 0, true, nil
}

// AccountConfig 返回账号级配置（未配置时 ok=false）。
func (s *Store) AccountConfig(tokenID int) (AccountConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, ok := s.accountConfigLocked(tokenID)
	return c, ok
}

// AccountScript 返回账号的开机脚本。
func (s *Store) AccountScript(tokenID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, ok := s.accountConfigLocked(tokenID)
	if !ok || !c.HasScript {
		return "", false
	}
	return c.Script, true
}

// AccountRootPassword 返回账号的 root 密码明文。
func (s *Store) AccountRootPassword(tokenID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, plain, ok := s.accountConfigLocked(tokenID)
	if !ok || plain == "" {
		return "", false
	}
	return plain, true
}

// AccountRootPasswordConfigured 报告账号是否已保存 root 密码（不暴露原文）。
func (s *Store) AccountRootPasswordConfigured(tokenID int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, ok := s.accountConfigLocked(tokenID)
	return ok && c.HasPass
}

func (s *Store) ensureAccountRowLocked(tokenID, userID int) error {
	if userID <= 0 {
		// 尽量从 tokens 补 user_id
		_ = s.db.QueryRow(`SELECT user_id FROM tokens WHERE id=?`, tokenID).Scan(&userID)
	}
	_, err := s.db.Exec(`INSERT INTO account_config(token_id, user_id, updated_at) VALUES(?,?,?)
		ON CONFLICT(token_id) DO UPDATE SET user_id=excluded.user_id`, tokenID, userID, time.Now().UTC().Format(time.RFC3339))
	return err
}

// SetAccountScript 写入账号级脚本。
func (s *Store) SetAccountScript(tokenID, userID int, script string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureAccountRowLocked(tokenID, userID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE account_config SET script=?, has_script=1, updated_at=? WHERE token_id=?`,
		script, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// DeleteAccountScript 清除账号级脚本。
func (s *Store) DeleteAccountScript(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET script='', has_script=0, updated_at=? WHERE token_id=?`,
		time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// SetAccountRootPassword 写入账号级 root 密码（加密储存）。
func (s *Store) SetAccountRootPassword(tokenID, userID int, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureAccountRowLocked(tokenID, userID); err != nil {
		return err
	}
	enc, err := s.encryptSecret(password)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE account_config SET root_password=?, has_password=1, updated_at=? WHERE token_id=?`,
		enc, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// DeleteAccountRootPassword 清除账号级 root 密码。
func (s *Store) DeleteAccountRootPassword(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET root_password='', has_password=0, updated_at=? WHERE token_id=?`,
		time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// SetAccountAutoRebuild 写入账号级自动换 IP 参数（不覆盖运行态）。
func (s *Store) SetAccountAutoRebuild(tokenID, userID int, enabled bool, port, threshold, interval int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureAccountRowLocked(tokenID, userID); err != nil {
		return err
	}
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.Exec(`UPDATE account_config
		SET auto_enabled=?, auto_port=?, auto_threshold=?, auto_interval=?, has_auto=1, updated_at=?
		WHERE token_id=?`, en, port, threshold, interval, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// DeleteAccountAutoRebuild 关闭账号级自动换 IP。
func (s *Store) DeleteAccountAutoRebuild(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET auto_enabled=0, has_auto=0, updated_at=? WHERE token_id=?`,
		time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}
