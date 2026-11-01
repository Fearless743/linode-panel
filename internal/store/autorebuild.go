package store

import "time"

// AutoRebuild is the per-instance "被墙后自动删机重建换 IP" setting plus the
// latest monitor state for that instance.
//
// Settings (Enabled/Port/Threshold/IntervalSec/UserID/TokenID) are written by
// the Web UI; the state fields (FailCount/LastStatus/...) are maintained by the
// background monitor so the UI can show what happened.
//
// Configuration is keyed by the instance's stable cache id (CacheID), not by
// the volatile Linode instance id, so a delete+recreate rebuild inherits it
// automatically: only instance_cache.instance_id changes.
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

// autoRebuildCols reads one configuration joined with its cache row so the
// caller sees the current Linode instance id.
const autoRebuildCols = `a.cache_id, c.instance_id, a.user_id, a.token_id, a.enabled,
	a.port, a.threshold, a.interval_sec, a.fail_count, a.last_status,
	a.last_detail, a.last_check_at, a.last_blocked_at, a.last_task_id, a.updated_at`

const autoRebuildFrom = ` FROM instance_auto_rebuild a JOIN instance_cache c ON c.id = a.cache_id`

// scanAutoRebuild reads one row in autoRebuildCols order.
func scanAutoRebuild(sc interface{ Scan(...any) error }) (AutoRebuild, error) {
	var (
		a       AutoRebuild
		enabled int
	)
	if err := sc.Scan(
		&a.CacheID, &a.InstanceID, &a.UserID, &a.TokenID, &enabled, &a.Port,
		&a.Threshold, &a.IntervalSec, &a.FailCount, &a.LastStatus, &a.LastDetail,
		&a.LastCheckAt, &a.LastBlockedAt, &a.LastTaskID, &a.UpdatedAt,
	); err != nil {
		return AutoRebuild{}, err
	}
	a.Enabled = enabled != 0
	return a, nil
}

// SetAutoRebuild upserts an instance's auto-rebuild settings. Monitor state
// (fail count, last check, ...) is preserved across edits; only a brand-new
// row starts with the schema defaults.
func (s *Store) SetAutoRebuild(a AutoRebuild) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, _, err := s.cacheIDForInstanceLocked(a.InstanceID, true)
	if err != nil {
		return err
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO instance_auto_rebuild
		   (cache_id, user_id, token_id, enabled, port, threshold, interval_sec,
		    updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(cache_id) DO UPDATE SET
		   user_id=excluded.user_id, token_id=excluded.token_id,
		   enabled=excluded.enabled, port=excluded.port,
		   threshold=excluded.threshold, interval_sec=excluded.interval_sec,
		   updated_at=excluded.updated_at`,
		cacheID, a.UserID, a.TokenID, enabled, a.Port, a.Threshold,
		a.IntervalSec, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// AutoRebuild returns one instance's auto-rebuild config (ok=false when the
// instance has never been configured).
func (s *Store) AutoRebuild(instanceID int) (AutoRebuild, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return AutoRebuild{}, false
	}
	row := s.db.QueryRow(`SELECT `+autoRebuildCols+autoRebuildFrom+` WHERE a.cache_id=?`, cacheID)
	a, err := scanAutoRebuild(row)
	if err != nil {
		return AutoRebuild{}, false
	}
	return a, true
}

// AutoRebuilds returns every configured instance, ordered by the current
// Linode instance id.
func (s *Store) AutoRebuilds() []AutoRebuild {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + autoRebuildCols + autoRebuildFrom + ` ORDER BY c.instance_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AutoRebuild
	for rows.Next() {
		a, err := scanAutoRebuild(rows)
		if err != nil {
			break
		}
		out = append(out, a)
	}
	return out
}

// DeleteAutoRebuild removes an instance's auto-rebuild config.
func (s *Store) DeleteAutoRebuild(instanceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM instance_auto_rebuild WHERE cache_id=?`, cacheID)
	return err
}

// UpdateAutoRebuildState records the latest monitor result for an instance.
// Empty strings leave the corresponding column unchanged (e.g. taskID on a
// plain check).
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
// stable cache id. Unlike the instance-id variant this keeps working after a
// rebuild replaced the instance id; the monitor uses it to finish a rebuild.
func (s *Store) UpdateAutoRebuildStateByCache(cacheID, failCount int, status, detail, checkAt, blockedAt, taskID string) error {
	if cacheID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateAutoRebuildStateLocked(cacheID, failCount, status, detail, checkAt, blockedAt, taskID)
}

func (s *Store) updateAutoRebuildStateLocked(cacheID, failCount int, status, detail, checkAt, blockedAt, taskID string) error {
	_, err := s.db.Exec(
		`UPDATE instance_auto_rebuild SET
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

// MoveAutoRebuild re-points an instance's auto-rebuild config to a new
// instance id. Delete+recreate rebuilds no longer need this (the stable cache
// id already follows the instance); it is kept for explicit moves and tests.
// The monitor state is reset — the new instance starts clean.
func (s *Store) MoveAutoRebuild(fromID, toID int) error {
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
	row := tx.QueryRow(`SELECT `+autoRebuildCols+autoRebuildFrom+` WHERE a.cache_id=?`, from)
	a, err := scanAutoRebuild(row)
	if err != nil {
		// Nothing to move (config already removed).
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM instance_auto_rebuild WHERE cache_id=?`, from); err != nil {
		return err
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	if _, err := tx.Exec(
		`INSERT INTO instance_auto_rebuild
		   (cache_id, user_id, token_id, enabled, port, threshold, interval_sec,
		    updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(cache_id) DO UPDATE SET
		   user_id=excluded.user_id, token_id=excluded.token_id,
		   enabled=excluded.enabled, port=excluded.port,
		   threshold=excluded.threshold, interval_sec=excluded.interval_sec,
		   updated_at=excluded.updated_at`,
		to, a.UserID, a.TokenID, enabled, a.Port, a.Threshold, a.IntervalSec,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	return tx.Commit()
}
