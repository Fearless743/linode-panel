package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Secrets in the SQLite database (Linode PATs, instance root passwords and the
// agent registration code) are encrypted at rest with AES-256-GCM. The master
// key is intentionally not stored in the database itself, so leaking only the
// database file does not expose the plaintext secrets.
//
// Key resolution order:
//  1. an explicit key passed to OpenWithKey (the --secret-key flag);
//  2. the LINODE_PANEL_SECRET_KEY environment variable;
//  3. a key file next to the database (<state>.key, mode 0600), created on
//     first use when it does not exist.
//
// Use option 1 or 2 to keep the key away from the database directory; the
// auto-generated key file protects against a leaked database file but not
// against a leak of the whole data directory.
const (
	secretPrefix  = "enc:v1:"
	secretAAD     = "linode-panel/secrets/v1"
	secretKeyLen  = 32
	keyFileSuffix = ".key"
)

// deriveSecretKey turns any non-empty passphrase into an AES-256 key.
func deriveSecretKey(secret string) []byte {
	sum := sha256.Sum256([]byte("linode-panel/secret-key/v1\x00" + secret))
	return sum[:]
}

// resolveSecretKey loads the master key with the precedence documented above.
// dbPath is used to locate the auto-generated key file.
func resolveSecretKey(dbPath, explicit string) ([]byte, error) {
	if secret := strings.TrimSpace(explicit); secret != "" {
		return deriveSecretKey(secret), nil
	}
	if secret := strings.TrimSpace(os.Getenv("LINODE_PANEL_SECRET_KEY")); secret != "" {
		return deriveSecretKey(secret), nil
	}
	return loadOrCreateKeyFile(dbPath + keyFileSuffix)
}

// loadOrCreateKeyFile reads the key file, creating it with a random key on
// first use. The file is always kept owner-only readable.
func loadOrCreateKeyFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		secret := strings.TrimSpace(string(raw))
		if secret == "" {
			return nil, fmt.Errorf("密钥文件 %s 为空", path)
		}
		return deriveSecretKey(secret), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取密钥文件 %s: %w", path, err)
	}

	secret := randomHex(secretKeyLen)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// Another process may have won the race; re-read its key.
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateKeyFile(path)
		}
		return nil, fmt.Errorf("创建密钥文件 %s: %w", path, err)
	}
	if _, err := f.WriteString(secret + "\n"); err != nil {
		f.Close()
		return nil, fmt.Errorf("写入密钥文件 %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("关闭密钥文件 %s: %w", path, err)
	}
	return deriveSecretKey(secret), nil
}

// encryptSecret encrypts plain with AES-256-GCM and returns the stored form
// ("enc:v1:" + base64(nonce|ciphertext)). Empty input stays empty.
func (s *Store) encryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	aead, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成随机数: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plain), []byte(secretAAD))
	return secretPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// decryptSecret reverses encryptSecret. Values without the prefix are treated
// as legacy plaintext so existing databases keep working until migration
// re-encrypts them.
func (s *Store) decryptSecret(stored string) (string, error) {
	if stored == "" || !strings.HasPrefix(stored, secretPrefix) {
		return stored, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("解码密文: %w", err)
	}
	aead, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度无效")
	}
	plain, err := aead.Open(nil, raw[:ns], raw[ns:], []byte(secretAAD))
	if err != nil {
		return "", errors.New("解密失败（密钥不匹配或数据已损坏）")
	}
	return string(plain), nil
}

func (s *Store) secretAEAD() (cipher.AEAD, error) {
	if len(s.key) != secretKeyLen {
		return nil, errors.New("数据库加密密钥未初始化")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES: %w", err)
	}
	return cipher.NewGCM(block)
}

// isEncryptedSecret reports whether v is already stored in encrypted form.
func isEncryptedSecret(v string) bool {
	return strings.HasPrefix(v, secretPrefix)
}

// encryptLegacyColumn re-encrypts plaintext values in one table column. It is
// idempotent: already-encrypted values are left untouched.
func (s *Store) encryptLegacyColumn(table, keyCol, valueCol string) error {
	rows, err := s.db.Query(fmt.Sprintf(`SELECT %s, %s FROM %s`, keyCol, valueCol, table))
	if err != nil {
		return err
	}
	type pending struct {
		key   int64
		value string
	}
	var updates []pending
	for rows.Next() {
		var key int64
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return err
		}
		if value == "" || isEncryptedSecret(value) {
			continue
		}
		enc, err := s.encryptSecret(value)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, pending{key: key, value: enc})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range updates {
		if _, err := s.db.Exec(
			fmt.Sprintf(`UPDATE %s SET %s=? WHERE %s=?`, table, valueCol, keyCol),
			u.value, u.key,
		); err != nil {
			return err
		}
	}
	return nil
}

// encryptLegacySecrets migrates plaintext secrets written by older versions.
// It runs once at startup, after the schema has been created.
func (s *Store) encryptLegacySecrets() error {
	if err := s.encryptLegacyColumn("tokens", "id", "token"); err != nil {
		return fmt.Errorf("加密 PAT: %w", err)
	}
	if err := s.encryptLegacyColumn("instance_config", "cache_id", "password"); err != nil {
		return fmt.Errorf("加密 root 密码: %w", err)
	}
	// The registration code lives in the meta key/value table.
	var code string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key='reg_code'`).Scan(&code); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if code == "" || isEncryptedSecret(code) {
		return nil
	}
	enc, err := s.encryptSecret(code)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE meta SET value=? WHERE key='reg_code'`, enc)
	return err
}

// verifySecretsDecryptable fails fast when the configured key cannot read the
// database's secrets (for example after switching from the auto-generated key
// file to a different --secret-key). Serving undecryptable tokens would look
// like every account lost its PAT, so a clear startup error is better.
func (s *Store) verifySecretsDecryptable() error {
	checks := []struct{ q string }{
		{`SELECT token FROM tokens WHERE token <> '' LIMIT 1`},
		{`SELECT password FROM instance_config WHERE password <> '' LIMIT 1`},
		{`SELECT value FROM meta WHERE key='reg_code' AND value <> '' LIMIT 1`},
	}
	for _, c := range checks {
		var stored string
		if err := s.db.QueryRow(c.q).Scan(&stored); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		if _, err := s.decryptSecret(stored); err != nil {
			return errors.New("数据库加密密钥不匹配：请使用创建数据库时的 --secret-key / LINODE_PANEL_SECRET_KEY，或恢复对应的 .key 密钥文件")
		}
	}
	return nil
}
