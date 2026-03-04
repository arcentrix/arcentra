// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
