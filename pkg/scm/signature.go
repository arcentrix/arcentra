package scm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// VerifyHmacSha256Hex verifies an HMAC-SHA256 signature in hex encoding.
// If headerPrefix is not empty and headerValue starts with it, the prefix is stripped before decoding.
func VerifyHmacSha256Hex(body []byte, secret, headerValue, headerPrefix string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return fmt.Errorf("webhook secret is required")
	}
	if strings.TrimSpace(headerValue) == "" {
		return fmt.Errorf("signature header is missing")
	}

	got := strings.TrimSpace(headerValue)
	if headerPrefix != "" && strings.HasPrefix(got, headerPrefix) {
		got = strings.TrimPrefix(got, headerPrefix)
	}
	got = strings.TrimSpace(got)

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)

	gotBytes, err := hex.DecodeString(got)
	if err != nil {
		return fmt.Errorf("invalid signature encoding")
	}
	if !hmac.Equal(expected, gotBytes) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// VerifyTokenHeader verifies a shared-secret token carried in a header.
// This is commonly used by providers that send the secret token directly.
func VerifyTokenHeader(secret, got string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return fmt.Errorf("webhook secret is required")
	}
	got = strings.TrimSpace(got)
	if got == "" {
		return fmt.Errorf("token header is missing")
	}
	if subtleStringEqual(secret, got) {
		return nil
	}
	return fmt.Errorf("token mismatch")
}

func subtleStringEqual(a, b string) bool {
	ab := []byte(a)
	bb := []byte(b)
	if len(ab) != len(bb) {
		return false
	}
	return hmac.Equal(ab, bb)
}
