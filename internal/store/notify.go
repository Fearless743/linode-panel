package store

// NotifyConfig 是全局 TG 通知配置（单例，存 meta 表）。
type NotifyConfig struct {
	Enabled  bool   `json:"enabled"`
	ChatID   string `json:"chat_id"`
	HasToken bool   `json:"has_token"`
}

// GetNotifyConfig 读取全局通知配置（token 不回显，只返回是否已配置）。
func (s *Store) GetNotifyConfig() NotifyConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	var enabled string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='tg_enabled'`).Scan(&enabled)
	var chat string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='tg_chat_id'`).Scan(&chat)
	var enc string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='tg_bot_token'`).Scan(&enc)
	return NotifyConfig{
		Enabled:  enabled == "1",
		ChatID:   chat,
		HasToken: enc != "",
	}
}

// SetNotifyConfig 写入全局通知配置。token 为空表示保持不变。
func (s *Store) SetNotifyConfig(enabled bool, chatID, botToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := "0"
	if enabled {
		ev = "1"
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES('tg_enabled', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, ev); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES('tg_chat_id', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, chatID); err != nil {
		return err
	}
	if botToken != "" {
		enc, err := s.encryptSecret(botToken)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`INSERT INTO meta(key, value) VALUES('tg_bot_token', ?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, enc); err != nil {
			return err
		}
	}
	return nil
}

// notifySecrets 返回明文 bot token 与 chat id（发送时使用）。
func (s *Store) notifySecrets() (token, chatID string) {
	var enc, chat string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='tg_bot_token'`).Scan(&enc)
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='tg_chat_id'`).Scan(&chat)
	if enc == "" || chat == "" {
		return "", ""
	}
	plain, err := s.decryptSecret(enc)
	if err != nil || plain == "" {
		return "", ""
	}
	return plain, chat
}

// NotifyTarget 返回明文 bot token、chat id 与是否开启（发送时使用）。
func (s *Store) NotifyTarget() (token, chatID string, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ev string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='tg_enabled'`).Scan(&ev)
	if ev != "1" {
		return "", "", false
	}
	token, chatID = s.notifySecrets()
	if token == "" || chatID == "" {
		return "", "", false
	}
	return token, chatID, true
}

// DualLastBlockedAt 读取双机账号上次被墙时间（RFC3339，无则 ""）。
func (s *Store) DualLastBlockedAt(tokenID int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v string
	_ = s.db.QueryRow(`SELECT dual_last_blocked_at FROM account_config WHERE token_id=?`, tokenID).Scan(&v)
	return v
}

// SetDualLastBlockedAt 写入双机账号本次被墙时间。
func (s *Store) SetDualLastBlockedAt(tokenID int, at string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE account_config SET dual_last_blocked_at=?, updated_at=? WHERE token_id=?`,
		at, at, tokenID)
	return err
}
