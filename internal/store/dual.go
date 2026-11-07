package store

import "time"

// DualConfig is the dual view of the account-level config (single place).
// Enabled/Active/Standby/State live in account_config.dual_* columns;
// script/root and detection params remain in the same row.
type DualConfig struct {
	TokenID           int    `json:"token_id"`
	UserID            int    `json:"user_id"`
	Enabled           bool   `json:"enabled"`
	Port              int    `json:"port"`
	Threshold         int    `json:"threshold"`
	IntervalSec       int    `json:"interval_seconds"`
	ActiveInstanceID  int    `json:"active_instance_id"`
	StandbyInstanceID int    `json:"standby_instance_id"`
	State             string `json:"state"`
	FailCount         int    `json:"fail_count"`
	LastStatus        string `json:"last_status"`
	LastDetail        string `json:"last_detail"`
	LastCheckAt       string `json:"last_check_at"`
	Script            string `json:"script"`
	HasRootPass       bool   `json:"has_root_password"`
	UpdatedAt         string `json:"updated_at"`
}

func (s *Store) scanDualLocked(tokenID int) (DualConfig, bool) {
	row := s.db.QueryRow(`SELECT token_id, user_id,
		dual_enabled, dual_port, dual_threshold, dual_interval,
		dual_active_instance_id, dual_standby_instance_id, dual_state, dual_fail_count,
		dual_last_status, dual_last_detail, dual_last_check_at,
		script, has_script, has_password, updated_at
		FROM account_config WHERE token_id=?`, tokenID)
	var d DualConfig
	var enabled, hasS, hasP int
	if err := row.Scan(&d.TokenID, &d.UserID, &enabled, &d.Port, &d.Threshold, &d.IntervalSec,
		&d.ActiveInstanceID, &d.StandbyInstanceID, &d.State, &d.FailCount,
		&d.LastStatus, &d.LastDetail, &d.LastCheckAt,
		&d.Script, &hasS, &hasP, &d.UpdatedAt); err != nil {
		return DualConfig{}, false
	}
	d.Enabled = enabled != 0
	d.HasRootPass = hasP != 0
	return d, true
}

// SetDualConfig upserts the dual switch + pointers (+ detection params).
// Script/root with account are the same single place; callers pass the
// account's current values via d.Script/rootPassword.
func (s *Store) SetDualConfig(d DualConfig, rootPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	enabled := 0
	if d.Enabled {
		enabled = 1
	}
	if d.Port <= 0 {
		d.Port = 22
	}
	if d.Threshold <= 0 {
		d.Threshold = 3
	}
	if d.IntervalSec <= 0 {
		d.IntervalSec = 300
	}
	encPass := ""
	if rootPassword != "" {
		enc, err := s.encryptSecret(rootPassword)
		if err != nil {
			return err
		}
		encPass = enc
	} else {
		_ = s.db.QueryRow(`SELECT root_password FROM account_config WHERE token_id=?`, d.TokenID).Scan(&encPass)
	}
	hasScript := 0
	if d.Script != "" {
		hasScript = 1
	}
	hasPass := 0
	if encPass != "" {
		hasPass = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT INTO account_config
		(token_id, user_id, dual_enabled, dual_port, dual_threshold, dual_interval,
		 dual_active_instance_id, dual_standby_instance_id,
		 script, has_script, root_password, has_password, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(token_id) DO UPDATE SET
			user_id=excluded.user_id, dual_enabled=excluded.dual_enabled,
			dual_port=excluded.dual_port, dual_threshold=excluded.dual_threshold,
			dual_interval=excluded.dual_interval,
			dual_active_instance_id=excluded.dual_active_instance_id,
			dual_standby_instance_id=excluded.dual_standby_instance_id,
			script=excluded.script, has_script=excluded.has_script,
			root_password=excluded.root_password, has_password=excluded.has_password,
			updated_at=excluded.updated_at`,
		d.TokenID, d.UserID, enabled, d.Port, d.Threshold, d.IntervalSec,
		d.ActiveInstanceID, d.StandbyInstanceID, d.Script, hasScript, encPass, hasPass, now)
	return err
}

// DualConfigOf returns dual view from the account row.
func (s *Store) DualConfigOf(tokenID int) (DualConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only return dual if the row was ever configured for dual (dual pointers/state set
	// or explicit enabled). To keep old behavior (row exists only after SetDual),
	// we check: dual_enabled or active/standby or state != idle or dual_last_status set.
	// Simplest: if account row exists and (enabled or active/standby assigned) then ok.
	// But tests create dual via SetDual always have row.
	// We need to distinguish "never configured" (no dual row) from "configured but disabled".
	// Use presence of dual_enabled/active/standby/state in the row created by SetDual
	// or migration; for migration-less rows created only for script, dual fields are defaults
	// indistinguishable. Track via active/standby/state: if all default, treat as not configured
	// only when the row was created without dual. Migration sets active/state, so that covers legacy.
	// For new writes, SetDual creates row with state ''? we set state '' default idle.
	// Heuristic: if account row exists, check if dual was ever set by looking at dual_state != 'idle' or active/standby !=0 or enabled.
	// To avoid false positives for script-only rows, return false when all dual fields are untouched.
	d, ok := s.scanDualLocked(tokenID)
	if !ok {
		return DualConfig{}, false
	}
	// Row exists but may be script-only (no dual ever set): treat as no dual.
	if !d.Enabled && d.ActiveInstanceID == 0 && d.StandbyInstanceID == 0 && (d.State == "" || d.State == "idle") && d.LastStatus == "" {
		return DualConfig{}, false
	}
	return d, true
}

// DualConfigs returns all dual rows (account rows that have dual pointers or enabled).
// For compatibility, list every account row that was ever dual-configured.
func (s *Store) DualConfigs() []DualConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT token_id, user_id,
		dual_enabled, dual_port, dual_threshold, dual_interval,
		dual_active_instance_id, dual_standby_instance_id, dual_state, dual_fail_count,
		dual_last_status, dual_last_detail, dual_last_check_at,
		script, has_script, has_password, updated_at
		FROM account_config ORDER BY token_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []DualConfig
	for rows.Next() {
		var d DualConfig
		var en, hasS, hasP int
		if err := rows.Scan(&d.TokenID, &d.UserID, &en, &d.Port, &d.Threshold, &d.IntervalSec,
			&d.ActiveInstanceID, &d.StandbyInstanceID, &d.State, &d.FailCount,
			&d.LastStatus, &d.LastDetail, &d.LastCheckAt,
			&d.Script, &hasS, &hasP, &d.UpdatedAt); err != nil {
			continue
		}
		d.Enabled = en != 0
		d.HasRootPass = hasP != 0
		// Only list rows that ever had dual (to keep UI list clean, filter untouched script-only rows)
		if !d.Enabled && d.ActiveInstanceID == 0 && d.StandbyInstanceID == 0 && (d.State == "" || d.State == "idle") && d.LastStatus == "" {
			// Still include if the row was created via dual path? Without marker, skip script-only.
			continue
		}
		out = append(out, d)
	}
	return out
}

// DualRootPassword returns the account root password (single place).
func (s *Store) DualRootPassword(tokenID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var enc string
	if err := s.db.QueryRow(`SELECT root_password FROM account_config WHERE token_id=?`, tokenID).Scan(&enc); err != nil || enc == "" {
		return "", false
	}
	plain, err := s.decryptSecret(enc)
	if err != nil || plain == "" {
		return "", false
	}
	return plain, true
}

// UpdateDualInstances updates A/B pointers.
func (s *Store) UpdateDualInstances(tokenID, activeID, standbyID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE account_config SET dual_active_instance_id=?, dual_standby_instance_id=?, updated_at=? WHERE token_id=?`,
		activeID, standbyID, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// UpdateDualState records dual state.
func (s *Store) UpdateDualState(tokenID, failCount int, state, status, detail, checkAt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE account_config SET dual_fail_count=?,
			dual_state=CASE WHEN ?='' THEN dual_state ELSE ? END,
			dual_last_status=CASE WHEN ?='' THEN dual_last_status ELSE ? END,
			dual_last_detail=CASE WHEN ?='' THEN dual_last_detail ELSE ? END,
			dual_last_check_at=CASE WHEN ?='' THEN dual_last_check_at ELSE ? END,
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

// RepointDualInstances.
func (s *Store) RepointDualInstances(oldID, newID int) error {
	if oldID <= 0 || newID <= 0 || oldID == newID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`UPDATE account_config SET dual_active_instance_id=? WHERE dual_active_instance_id=?`, newID, oldID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE account_config SET dual_standby_instance_id=? WHERE dual_standby_instance_id=?`, newID, oldID)
	return err
}

// DeleteDualConfig clears the dual switch and pointers in the single account row.
func (s *Store) DeleteDualConfig(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET dual_enabled=0, dual_active_instance_id=0,
		dual_standby_instance_id=0, dual_state='idle', dual_fail_count=0,
		dual_last_status='', dual_last_detail='', dual_last_check_at='', dual_last_blocked_at='',
		updated_at=? WHERE token_id=?`, time.Now().UTC().Format(time.RFC3339), tokenID)
	return err
}

// DualInstanceRole.
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

// ClearDualInstance.
func (s *Store) ClearDualInstance(instanceID int) error {
	if instanceID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`UPDATE account_config SET dual_active_instance_id=0 WHERE dual_active_instance_id=?`, instanceID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE account_config SET dual_standby_instance_id=0 WHERE dual_standby_instance_id=?`, instanceID)
	return err
}

// ReassignDualInstance.
func (s *Store) ReassignDualInstance(sourceTokenID, targetTokenID, oldID, newID int) error {
	if sourceTokenID <= 0 || oldID <= 0 || newID <= 0 || oldID == newID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	read := func(tokenID int) (DualConfig, bool) {
		return s.scanDualLocked(tokenID)
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
		`UPDATE account_config SET dual_active_instance_id=?, dual_standby_instance_id=?, updated_at=? WHERE token_id=?`,
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
			`UPDATE account_config SET dual_active_instance_id=?, dual_standby_instance_id=?, updated_at=? WHERE token_id=?`,
			dst.ActiveInstanceID, dst.StandbyInstanceID,
			time.Now().UTC().Format(time.RFC3339), targetTokenID)
		return err
	}
	return nil
}
