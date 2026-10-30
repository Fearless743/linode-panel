package sshrun

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GenerateKey creates a fresh ed25519 keypair for provisioning access.
// privatePEM is OpenSSH-format PEM ("-----BEGIN OPENSSH PRIVATE KEY-----");
// authorizedKey is the matching "ssh-ed25519 AAAA..." line (no comment).
func GenerateKey() (privatePEM []byte, authorizedKey string, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("sshrun: generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "linode-panel")
	if err != nil {
		return nil, "", fmt.Errorf("sshrun: marshal private key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(block)
	pubKey, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return nil, "", fmt.Errorf("sshrun: marshal public key: %w", err)
	}
	auth := string(ssh.MarshalAuthorizedKey(pubKey))
	return pemBytes, auth, nil
}
