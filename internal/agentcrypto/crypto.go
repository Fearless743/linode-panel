// Package agentcrypto seals agent-bound payloads (Linode PATs) with
// AES-256-GCM. The key is derived from the registration code: the caller
// derives a 32-byte key with DeriveKey (or passes SHA-256(registration_code)
// directly).
package agentcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// DeriveKey derives the 32-byte AES-256 key from a registration code by
// taking its SHA-256 digest. Both the panel and the agent share the
// registration code, so both derive the same key.
func DeriveKey(registrationCode string) []byte {
	sum := sha256.Sum256([]byte(registrationCode))
	return sum[:]
}

// Seal encrypts plaintext with AES-256-GCM under key (32 bytes) and returns
// base64(nonce || ciphertext || tag).
func Seal(key []byte, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("创建加密器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value produced by Seal and returns the plaintext. Any
// parsing or decryption failure yields a Chinese error.
func Open(key []byte, sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, errors.New("解密失败: 密文不是合法的 Base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建解密器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("解密失败: 密文长度不足")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("解密失败: 密钥错误或数据被篡改")
	}
	return plain, nil
}
