package agentcrypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key := DeriveKey("registration-code-123")
	plaintext := []byte("linode_pat_abcdef0123456789")

	sealed, err := Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed == "" {
		t.Fatal("Seal returned empty string")
	}

	got, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestSealProducesDifferentCiphertexts(t *testing.T) {
	key := DeriveKey("code")
	plaintext := []byte("payload")

	a, err := Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if a == b {
		t.Fatal("two seals of the same plaintext produced identical output; nonce is not random")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	key := DeriveKey("code")
	sealed, err := Seal(key, []byte("secret pat"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Flip one byte in the middle of the sealed payload.
	raw := []byte(sealed)
	mid := len(raw) / 2
	if raw[mid] == 'A' {
		raw[mid] = 'B'
	} else {
		raw[mid] = 'A'
	}
	if _, err := Open(key, string(raw)); err == nil {
		t.Fatal("Open accepted tampered ciphertext")
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	key := DeriveKey("code")
	sealed, err := Seal(key, []byte("secret pat"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(DeriveKey("other-code"), sealed); err == nil {
		t.Fatal("Open accepted ciphertext under wrong key")
	}
}

func TestOpenRejectsBadBase64(t *testing.T) {
	key := DeriveKey("code")
	if _, err := Open(key, "!!!not-base64!!!"); err == nil {
		t.Fatal("Open accepted invalid base64")
	} else if !strings.Contains(err.Error(), "解密失败") {
		t.Fatalf("expected Chinese error, got %q", err.Error())
	}
}

func TestOpenRejectsShortCiphertext(t *testing.T) {
	key := DeriveKey("code")
	if _, err := Open(key, "aGVsbG8="); err == nil {
		t.Fatal("Open accepted truncated ciphertext")
	}
}

func TestDeriveKeyLength(t *testing.T) {
	if got := len(DeriveKey("whatever")); got != 32 {
		t.Fatalf("DeriveKey length = %d, want 32", got)
	}
	if !bytes.Equal(DeriveKey("same"), DeriveKey("same")) {
		t.Fatal("DeriveKey not deterministic")
	}
	if bytes.Equal(DeriveKey("a"), DeriveKey("b")) {
		t.Fatal("DeriveKey collision for distinct codes")
	}
}
