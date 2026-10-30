// Package agentcrypto seals agent-bound payloads (Linode PATs) with
// AES-256-GCM. The key is derived from the registration code, which both the
// panel and the agent know: key = SHA-256(registration_code).
//
// Frozen contract:
//
//	func Seal(key []byte, plaintext []byte) (string, error) // base64(nonce||ct)
//	func Open(key []byte, sealed string) ([]byte, error)
package agentcrypto
