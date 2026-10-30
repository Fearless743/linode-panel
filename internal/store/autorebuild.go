package store

import "time"

// AutoRebuild is the per-instance "被墙后自动删机重建换 IP" setting plus the
// latest monitor state for that instance.
//
// Settings (Enabled/Port/Threshold/IntervalSec/UserID/TokenID) are written by
// the Web UI; the state fields (FailCount/LastStatus/...) are maintained by the
// background monitor so the UI can show what happened.
type AutoRebuild struct {
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

const autoRebuildCols = `instance_id, user_id, token_id, enabled, port, threshold,
	interval_sec, fail_count, last_status, last_detail,
	last_check_at, last_blocked_at, last_task_id, updated_at`

// scanAutoRebuild reads one row in autoRebuildCols order.
func scanAutoRebuild(sc interface{ Scan(...any) error }) (AutoRebuild, error) {
	var (
		a       AutoRebuild
		enabled int
	)
	if err := sc.Scan(
		&a.InstanceID, &a.UserID, &a.TokenID, &enabled, &a.Port, &a.Threshold,
		&a.IntervalSec, &a.FailCount, &a.LastStatus, &a.LastDetail,
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
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO instance_auto_rebuild
		   (instance_id, user_id, token_id, enabled, port, threshold, interval_sec,
		    updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(instance_id) DO UPDATE SET
		   user_id=excluded.user_id, token_id=excluded.token_id,
		   enabled=excluded.enabled, port=excluded.port,
		   threshold=excluded.threshold, interval_sec=excluded.interval_sec,
		   updated_at=excluded.updated_at`,
		a.InstanceID, a.UserID, a.TokenID, enabled, a.Port, a.Threshold,
		a.IntervalSec, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// AutoRebuild returns one instance's auto-rebuild config (ok=false when the
// instance has never been configured).
func (s *Store) AutoRebuild(instanceID int) (AutoRebuild, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT `+autoRebuildCols+` FROM instance_auto_rebuild WHERE instance_id=?`, instanceID)
	a, err := scanAutoRebuild(row)
	if err != nil {
		return AutoRebuild{}, false
	}
	return a, true
}

// AutoRebuilds returns every configured instance, ordered by id.
func (s *Store) AutoRebuilds() []AutoRebuild {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + autoRebuildCols + ` FROM instance_auto_rebuild ORDER BY instance_id`)
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
	_, err := s.db.Exec(`DELETE FROM instance_auto_rebuild WHERE instance_id=?`, instanceID)
	return err
}

// UpdateAutoRebuildState records the latest monitor result for an instance.
// Empty strings leave the corresponding column unchanged (e.g. taskID on a
// plain check).
func (s *Store) UpdateAutoRebuildState(instanceID, failCount int, status, detail, checkAt, blockedAt, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE instance_auto_rebuild SET
		   fail_count=?,
		   last_status=CASE WHEN ?='' THEN last_status ELSE ? END,
		   last_detail=CASE WHEN ?='' THEN last_detail ELSE ? END,
		   last_check_at=CASE WHEN ?='' THEN last_check_at ELSE ? END,
		   last_blocked_at=CASE WHEN ?='' THEN last_blocked_at ELSE ? END,
		   last_task_id=CASE WHEN ?='' THEN last_task_id ELSE ? END,
		   updated_at=?
		 WHERE instance_id=?`,
		failCount,
		status, status,
		detail, detail,
		checkAt, checkAt,
		blockedAt, blockedAt,
		taskID, taskID,
		time.Now().UTC().Format(time.RFC3339),
		instanceID,
	)
	return err
}

// MoveAutoRebuild re-points an instance's auto-rebuild config to a new
// instance id (a rebuild deletes the old instance and creates a new one with a
// fresh id). The monitor state is reset — the new instance starts clean.
func (s *Store) MoveAutoRebuild(fromID, toID int) error {
	if fromID == toID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row := tx.QueryRow(`SELECT `+autoRebuildCols+` FROM instance_auto_rebuild WHERE instance_id=?`, fromID)
	a, err := scanAutoRebuild(row)
	if err != nil {
		// Nothing to move (config already removed).
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM instance_auto_rebuild WHERE instance_id=?`, fromID); err != nil {
		return err
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	if _, err := tx.Exec(
		`INSERT INTO instance_auto_rebuild
		   (instance_id, user_id, token_id, enabled, port, threshold, interval_sec,
		    updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		toID, a.UserID, a.TokenID, enabled, a.Port, a.Threshold, a.IntervalSec,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	return tx.Commit()
}
