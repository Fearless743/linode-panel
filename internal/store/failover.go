package store

import "time"

// Failover 是「同账号组内跨账号替换」的按实例配置及其最新监控状态。
//
// 设置项（Enabled/GroupID/TokenID/Threshold/IntervalSec/UserID）由 Web UI 写入；
// 状态字段（FailCount/LastStatus/...）由后台监控维护，供 UI 展示发生了什么。
// 与 AutoRebuild 的区别：AutoRebuild 是同账号删机重建换 IP，Failover 是在所属
// 账号组（GroupID）内换另一个账号（TokenID）用相同配置重建。
//
// 配置按实例的稳定缓存 id（CacheID）关联；InstanceID 是当前 Linode 实例 id，
// 由缓存行解析得到。
type Failover struct {
	CacheID int `json:"-"`
	// InstanceID 是当前 Linode 实例 id。
	InstanceID int `json:"instance_id"`
	UserID     int `json:"user_id"`
	// GroupID 是该实例所属的账号组。
	GroupID int `json:"group_id"`
	// TokenID 是当前承载该实例的账号。
	TokenID int  `json:"token_id"`
	Enabled bool `json:"enabled"`
	// Threshold 是连续失败几次触发替换。
	Threshold int `json:"threshold"`
	// IntervalSec 是对该实例两次检查之间的最小间隔。
	IntervalSec int `json:"interval_seconds"`

	FailCount  int    `json:"fail_count"`
	LastStatus string `json:"last_status"` // ok | blocked | no_balance | banned | rebuilding | error | idle
	LastDetail string `json:"last_detail"`
	// LastCheckAt 是最近一次检查时间。
	LastCheckAt string `json:"last_check_at"`
	LastTaskID  string `json:"last_task_id"`
	// ReplacedBy 是替换后新实例的 id（0 表示尚未替换）。
	ReplacedBy int    `json:"replaced_by_instance_id"`
	UpdatedAt  string `json:"updated_at"`
}

const failoverCols = `f.cache_id, c.instance_id, f.user_id, f.group_id, f.token_id,
	f.enabled, f.threshold, f.interval_sec, f.fail_count, f.last_status,
	f.last_detail, f.last_check_at, f.last_task_id, f.replaced_by, f.updated_at`

const failoverFrom = ` FROM instance_failover f JOIN instance_cache c ON c.id = f.cache_id`

// scanFailover 按 failoverCols 的顺序读取一行。
func scanFailover(sc interface{ Scan(...any) error }) (Failover, error) {
	var (
		f       Failover
		enabled int
	)
	if err := sc.Scan(
		&f.CacheID, &f.InstanceID, &f.UserID, &f.GroupID, &f.TokenID, &enabled,
		&f.Threshold, &f.IntervalSec, &f.FailCount, &f.LastStatus, &f.LastDetail,
		&f.LastCheckAt, &f.LastTaskID, &f.ReplacedBy, &f.UpdatedAt,
	); err != nil {
		return Failover{}, err
	}
	f.Enabled = enabled != 0
	return f, nil
}

// SetFailover upsert 一个实例的替换设置。监控状态（fail_count、last_*、
// replaced_by 等）在编辑时会被保留；只有全新的一行才会使用建表时的默认值。
func (s *Store) SetFailover(f Failover) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, _, err := s.cacheIDForInstanceLocked(f.InstanceID, true)
	if err != nil {
		return err
	}
	enabled := 0
	if f.Enabled {
		enabled = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO instance_failover
		   (cache_id, user_id, group_id, token_id, enabled, threshold, interval_sec,
		    updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(cache_id) DO UPDATE SET
		   user_id=excluded.user_id, group_id=excluded.group_id,
		   token_id=excluded.token_id, enabled=excluded.enabled,
		   threshold=excluded.threshold, interval_sec=excluded.interval_sec,
		   updated_at=excluded.updated_at`,
		cacheID, f.UserID, f.GroupID, f.TokenID, enabled, f.Threshold,
		f.IntervalSec, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// Failover 返回某实例的替换配置（该实例从未配置过时 ok=false）。
func (s *Store) Failover(instanceID int) (Failover, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return Failover{}, false
	}
	row := s.db.QueryRow(`SELECT `+failoverCols+failoverFrom+` WHERE f.cache_id=?`, cacheID)
	f, err := scanFailover(row)
	if err != nil {
		return Failover{}, false
	}
	return f, true
}

// Failovers 返回所有已配置的实例，按当前 Linode 实例 id 升序。
func (s *Store) Failovers() []Failover {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + failoverCols + failoverFrom + ` ORDER BY c.instance_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Failover
	for rows.Next() {
		f, err := scanFailover(rows)
		if err != nil {
			break
		}
		out = append(out, f)
	}
	return out
}

// FailoversOfGroup 返回某账号组下的所有替换配置，按当前 Linode 实例 id 升序。
func (s *Store) FailoversOfGroup(groupID int) []Failover {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT `+failoverCols+failoverFrom+` WHERE f.group_id=? ORDER BY c.instance_id`, groupID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Failover
	for rows.Next() {
		f, err := scanFailover(rows)
		if err != nil {
			break
		}
		out = append(out, f)
	}
	return out
}

// DeleteFailover 删除某实例的替换配置。
func (s *Store) DeleteFailover(instanceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM instance_failover WHERE cache_id=?`, cacheID)
	return err
}

// UpdateFailoverState 记录某实例最近一次的监控结果。空字符串表示对应列保持
// 不变（例如普通检查时没有 taskID）。
func (s *Store) UpdateFailoverState(instanceID, failCount int, status, detail, checkAt, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return err
	}
	return s.updateFailoverStateLocked(cacheID, failCount, status, detail, checkAt, taskID)
}

// UpdateFailoverStateByCache 使用稳定缓存 id 记录监控结果（重建换 id 后仍有效）。
func (s *Store) UpdateFailoverStateByCache(cacheID, failCount int, status, detail, checkAt, taskID string) error {
	if cacheID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateFailoverStateLocked(cacheID, failCount, status, detail, checkAt, taskID)
}

func (s *Store) updateFailoverStateLocked(cacheID, failCount int, status, detail, checkAt, taskID string) error {
	_, err := s.db.Exec(
		`UPDATE instance_failover SET
		   fail_count=?,
		   last_status=CASE WHEN ?='' THEN last_status ELSE ? END,
		   last_detail=CASE WHEN ?='' THEN last_detail ELSE ? END,
		   last_check_at=CASE WHEN ?='' THEN last_check_at ELSE ? END,
		   last_task_id=CASE WHEN ?='' THEN last_task_id ELSE ? END,
		   updated_at=?
		 WHERE cache_id=?`,
		failCount,
		status, status,
		detail, detail,
		checkAt, checkAt,
		taskID, taskID,
		time.Now().UTC().Format(time.RFC3339),
		cacheID,
	)
	return err
}

// MoveFailover 把一个实例的替换配置迁移到新的实例 id（账号替换会删除旧实例
// 并创建一个全新 id 的实例）。监控状态会被重置——新实例从干净状态开始；
// replaced_by 也不迁移（新行从默认值 0 开始）。
func (s *Store) MoveFailover(fromID, toID int) error {
	if fromID == toID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	from, ok, err := s.cacheIDForInstanceLocked(fromID, false)
	if err != nil || !ok {
		return err
	}
	to, _, err := s.cacheIDForInstanceLocked(toID, true)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row := tx.QueryRow(`SELECT `+failoverCols+failoverFrom+` WHERE f.cache_id=?`, from)
	f, err := scanFailover(row)
	if err != nil {
		// 没有可迁移的配置（已被删除）。
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM instance_failover WHERE cache_id=?`, from); err != nil {
		return err
	}
	enabled := 0
	if f.Enabled {
		enabled = 1
	}
	if _, err := tx.Exec(
		`INSERT INTO instance_failover
		   (cache_id, user_id, group_id, token_id, enabled, threshold, interval_sec,
		    updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(cache_id) DO UPDATE SET
		   user_id=excluded.user_id, group_id=excluded.group_id,
		   token_id=excluded.token_id, enabled=excluded.enabled,
		   threshold=excluded.threshold, interval_sec=excluded.interval_sec,
		   updated_at=excluded.updated_at`,
		to, f.UserID, f.GroupID, f.TokenID, enabled, f.Threshold, f.IntervalSec,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	return tx.Commit()
}
