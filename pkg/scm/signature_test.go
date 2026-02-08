package scm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyHmacSha256Hex_GitHubStyle(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	secret := "s3cr3t"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if err := VerifyHmacSha256Hex(body, secret, sig, "sha256="); err != nil {
		t.Fatalf("expected ok, got error: %v", err)
	}
}

func TestVerifyHmacSha256Hex_NoPrefix(t *testing.T) {
	body := []byte("abc")
	secret := "k"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if err := VerifyHmacSha256Hex(body, secret, sig, ""); err != nil {
		t.Fatalf("expected ok, got error: %v", err)
	}
}

func TestVerifyTokenHeader(t *testing.T) {
	if err := VerifyTokenHeader("token", "token"); err != nil {
		t.Fatalf("expected ok, got error: %v", err)
	}
	if err := VerifyTokenHeader("token", "bad"); err == nil {
		t.Fatalf("expected error")
	}
}
