package service

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSecretEncryptionRequiresExplicitValidKey(t *testing.T) {
	for _, value := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(make([]byte, 16)), base64.StdEncoding.EncodeToString(make([]byte, 34))} {
		t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", value)
		s := NewSecretService(nil)
		if _, err := s.encrypt("fixture-secret"); err == nil {
			t.Fatal("invalid key encrypted plaintext")
		}
		if _, err := s.decrypt("AAAA"); err == nil {
			t.Fatal("invalid key decrypted ciphertext")
		}
	}
}

func TestSecretEncryptionRoundTripRestartAndTamper(t *testing.T) {
	// 固定测试夹具，不是部署凭据。
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)))
	first := NewSecretService(nil)
	ciphertext, err := first.encrypt("fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	another, err := first.encrypt("fixture-secret")
	if err != nil || another == ciphertext {
		t.Fatal("nonce was reused")
	}
	if strings.Contains(ciphertext, "fixture-secret") {
		t.Fatal("plaintext stored")
	}
	restarted := NewSecretService(nil)
	plain, err := restarted.decrypt(ciphertext)
	if err != nil || plain != "fixture-secret" {
		t.Fatalf("restart decrypt: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(ciphertext)
	raw[len(raw)-1] ^= 1
	if _, err := restarted.decrypt(base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tamper accepted")
	}
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32)))
	if _, err := NewSecretService(nil).decrypt(ciphertext); err == nil {
		t.Fatal("wrong key accepted")
	}
	for _, invalid := range []string{"invalid!", "", "AA=="} {
		if _, err := restarted.decrypt(invalid); err == nil {
			t.Fatal("invalid ciphertext accepted")
		}
	}
}
