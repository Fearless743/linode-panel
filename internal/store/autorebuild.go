package store

import "time"

// AutoRebuild is the per-instance "被墙后自动删机重建换 IP" view.
// Settings (Enabled/Port/Threshold/IntervalSec/UserID/TokenID) come from the
// account-level config; runtime state (FailCount/LastStatus/...) is per instance
// (keyed by the stable cache id) so each instance in the same account keeps its
// own probe counters.
type AutoRebuild struct {
	CacheID int `json:"-"`
	// InstanceID is the current Linode instance id, resolved from the cache
	// row. It is what the Web UI and the monitor use.
	InstanceID int  `json:"instance_id"`
	UserID     int  `json:"user_id"`
	TokenID    int  `json:"token_id"`
	Enabled    bool `json:"enabled"`
	// Port is the TCP port probed from inside China to decide "被墙".
	Port int `json:"port"`
	// Threshold is how many consecutive blocked checks trigger a rebuild.
	Threshold int `json:"threshold"`
	// IntervalSec is the minimum gap between two checks of this instance.
	IntervalSec int `json:"interval_seconds"`

	FailCount     int    `json:"fail_count"`
	LastStatus    string `json:"last_status"` // ok | blocked | rebuilding | error | idle
	LastDetail    string `json:"last_detail"`
	LastCheckAt   string `json:"last_check_at"`
	LastBlockedAt string `json:"last_blocked_at"`
	LastTaskID    string `json:"last_task_id"`
	UpdatedAt     string `json:"updated_at"`
}

func scanState(dc interface{ Scan(...any) error }, a *AutoRebuild) {
	_ = dc.Scan(&a.FailCount, &a.LastStatus, &a.LastDetail, &a.LastCheckAt, &a.LastBlockedAt, &a.LastTaskID, &a.UpdatedAt)
}

// ensureStateRow guarantees the per-instance runtime row exists.
func (s *Store) ensureStateRowLocked(cacheID int) {
	_, _ = s.db.Exec(`INSERT INTO instance_config(cache_id, updated_at) VALUES(?, '')
		ON CONFLICT(cache_id) DO NOTHING`, cacheID)
}

func (s *Store) readStateLocked(cacheID int, a *AutoRebuild) {
	row := s.db.QueryRow(`SELECT fail_count, last_status, last_detail, last_check_at,
		last_blocked_at, last_task_id, updated_at FROM instance_config WHERE cache_id=?`, cacheID)
	_ = row.Scan(&a.FailCount, &a.LastStatus, &a.LastDetail, &a.LastCheckAt, &a.LastBlockedAt, &a.LastTaskID, &a.UpdatedAt)
}

// SetAutoRebuild upserts the account-level auto-rebuild settings for the
// instance's token. Runtime state is preserved.
func (s *Store) SetAutoRebuild(a AutoRebuild) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokenID := a.TokenID
	if tokenID <= 0 {
		tok, ok, err := s.tokenForInstanceLocked(a.InstanceID, false)
		if err != nil {
			return err
		}
		if ok {
			tokenID = tok
		}
	}
	if tokenID > 0 {
		if err := s.ensureAccountRowLocked(tokenID, a.UserID); err != nil {
			return err
		}
		en := 0
		if a.Enabled {
			en = 1
		}
		if _, err := s.db.Exec(`UPDATE account_config
			SET auto_enabled=?, auto_port=?, auto_threshold=?, auto_interval=?, has_auto=1, user_id=?, updated_at=?
			WHERE token_id=?`,
			en, a.Port, a.Threshold, a.IntervalSec, a.UserID,
			time.Now().UTC().Format(time.RFC3339), tokenID); err != nil {
			return err
		}
	}
	// Ensure the per-instance state row exists so UpdateState works.
	cacheID, _, err := s.cacheIDForInstanceLocked(a.InstanceID, true)
	if err != nil {
		return err
	}
	// anchor 占位行带 token 0 时，修正为声明的账号，使 AutoRebuild 能找回账号配置。
	if tokenID > 0 {
		_, _ = s.db.Exec(`UPDATE instance_cache SET token_id=? WHERE id=? AND token_id=0`, tokenID, cacheID)
	}
	s.ensureStateRowLocked(cacheID)
	return nil
}

// AutoRebuild returns one instance's auto-rebuild view (account settings + own state).
func (s *Store) AutoRebuild(instanceID int) (AutoRebuild, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return AutoRebuild{}, false
	}
	var tokenID int
	if err := s.db.QueryRow(`SELECT token_id FROM instance_cache WHERE id=?`, cacheID).Scan(&tokenID); err != nil {
		return AutoRebuild{}, false
	}
	var hasAuto int
	if err := s.db.QueryRow(`SELECT has_auto FROM account_config WHERE token_id=?`, tokenID).Scan(&hasAuto); err != nil {
		return AutoRebuild{}, false
	}
	if hasAuto == 0 {
		return AutoRebuild{}, false
	}
	c, _, ok := s.accountConfigLocked(tokenID)
	if !ok {
		return AutoRebuild{}, false
	}
	a := AutoRebuild{
		CacheID: cacheID, InstanceID: instanceID, UserID: c.UserID, TokenID: tokenID,
		Enabled: c.Enabled, Port: c.Port, Threshold: c.Threshold, IntervalSec: c.IntervalSec,
		UpdatedAt: c.UpdatedAt,
	}
	s.readStateLocked(cacheID, &a)
	// readStateLocked would overwrite UpdatedAt with state row's stale value; restore account time.
	a.UpdatedAt = c.UpdatedAt
	return a, true
}

// AutoRebuilds returns every instance whose account has auto-rebuild config,
// ordered by current instance id. Runtime state is per instance.
func (s *Store) AutoRebuilds() []AutoRebuild {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT c.id, c.instance_id, c.token_id, a.user_id,
		a.auto_enabled, a.auto_port, a.auto_threshold, a.auto_interval, a.updated_at
		FROM instance_cache c JOIN account_config a ON a.token_id=c.token_id
		WHERE a.has_auto=1
		ORDER BY c.instance_id, c.id`)
	if err != nil {
		return nil
	}
	var out []AutoRebuild
	for rows.Next() {
		var a AutoRebuild
		var en int
		if err := rows.Scan(&a.CacheID, &a.InstanceID, &a.TokenID, &a.UserID, &en, &a.Port, &a.Threshold, &a.IntervalSec, &a.UpdatedAt); err != nil {
			continue
		}
		a.Enabled = en != 0
		out = append(out, a)
	}
	_ = rows.Close()
	for i := range out {
		acctUpdated := out[i].UpdatedAt
		s.readStateLocked(out[i].CacheID, &out[i])
		out[i].UpdatedAt = acctUpdated
	}
	return out
}

// DeleteAutoRebuild disables the account-level auto-rebuild (affects all
// instances of the account).
func (s *Store) DeleteAutoRebuild(instanceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokenID, ok, err := s.tokenForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return err
	}
	_, err = s.db.Exec(`UPDATE account_config SET auto_enabled=0, has_auto=0, updated_at=? WHERE token_id=?`,
		time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// UpdateAutoRebuildState records the latest monitor result for an instance.
func (s *Store) UpdateAutoRebuildState(instanceID, failCount int, status, detail, checkAt, blockedAt, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return err
	}
	return s.updateAutoRebuildStateLocked(cacheID, failCount, status, detail, checkAt, blockedAt, taskID)
}

// UpdateAutoRebuildStateByCache records the latest monitor result using the
// stable cache id.
func (s *Store) UpdateAutoRebuildStateByCache(cacheID, failCount int, status, detail, checkAt, blockedAt, taskID string) error {
	if cacheID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateAutoRebuildStateLocked(cacheID, failCount, status, detail, checkAt, blockedAt, taskID)
}

func (s *Store) updateAutoRebuildStateLocked(cacheID, failCount int, status, detail, checkAt, blockedAt, taskID string) error {
	s.ensureStateRowLocked(cacheID)
	_, err := s.db.Exec(
		`UPDATE instance_config SET
		   fail_count=?,
		   last_status=CASE WHEN ?='' THEN last_status ELSE ? END,
		   last_detail=CASE WHEN ?='' THEN last_detail ELSE ? END,
		   last_check_at=CASE WHEN ?='' THEN last_check_at ELSE ? END,
		   last_blocked_at=CASE WHEN ?='' THEN last_blocked_at ELSE ? END,
		   last_task_id=CASE WHEN ?='' THEN last_task_id ELSE ? END,
		   updated_at=?
		   WHERE cache_id=?`,
		failCount,
		status, status,
		detail, detail,
		checkAt, checkAt,
		blockedAt, blockedAt,
		taskID, taskID,
		time.Now().UTC().Format(time.RFC3339),
		cacheID,
	)
	return err
}

// MoveAutoRebuild moves the per-instance state to a new id and copies the
// account-level settings to the target token (if different). After the move
// the old id no longer resolves as configured.
func (s *Store) MoveAutoRebuild(fromID, toID int) error {
	if fromID == toID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// ensure target cache row exists for nid
	dstCacheID, _, err := s.cacheIDForInstanceLocked(toID, true)
	if err != nil {
		return err
	}
	srcTok, _, _ := s.tokenForInstanceLocked(fromID, false)
	dstTok, _, _ := s.tokenForInstanceLocked(toID, false)
	// anchor 占位行带 token 0：跟随源账号，使新实例能读到同一套账号配置
	if dstTok == 0 && srcTok > 0 {
		_, _ = s.db.Exec(`UPDATE instance_cache SET token_id=? WHERE id=? AND token_id=0`, srcTok, dstCacheID)
		dstTok = srcTok
	}
	// copy account settings across tokens
	if srcTok > 0 && srcTok != dstTok {
		c, _, ok := s.accountConfigLocked(srcTok)
		if ok {
			if err := s.ensureAccountRowLocked(dstTok, c.UserID); err != nil {
				return err
			}
			en := 0
			if c.Enabled {
				en = 1
			}
			_, _ = s.db.Exec(`UPDATE account_config
				SET auto_enabled=?, auto_port=?, auto_threshold=?, auto_interval=?, has_auto=1, updated_at=?
				WHERE token_id=?`, en, c.Port, c.Threshold, c.IntervalSec,
				time.Now().UTC().Format(time.RFC3339), dstTok)
		}
	}
	// reset destination state, drop source state + cache rows so old id reads as unconfigured
	_, _ = s.db.Exec(`DELETE FROM instance_config WHERE cache_id=?`, dstCacheID)
	s.ensureStateRowLocked(dstCacheID)
	// Remove source instance's cache + state
	fromCacheID, _, _ := s.cacheIDForInstanceLocked(fromID, false)
	if fromCacheID > 0 {
		_, _ = s.db.Exec(`DELETE FROM instance_config WHERE cache_id=?`, fromCacheID)
		_, _ = s.db.Exec(`DELETE FROM instance_cache WHERE id=?`, fromCacheID)
	}
	return nil
}
