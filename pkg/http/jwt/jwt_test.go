// Copyright 2025 Arcentra Team
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

package jwt

import (
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/http"
)

func TestJwt(t *testing.T) {

	userId := "1"
	secretKey := []byte("1111111111111111")
	accessExpired := time.Hour * 24
	refreshExpired := time.Hour * 24 * 7

	aToken, rToken, err := GenToken(userId, secretKey, accessExpired, refreshExpired)
	if err != nil {
		t.Errorf("GenToken error: %v", err)
	}
	t.Logf("aToken: %s, rToken: %s", aToken, rToken)
}

func TestRefreshToken(t *testing.T) {

	userId := "1"
	secretKey := "bf284d03-ba65-42d4-a9fe-0d2fbfe61060"
	accessExpire := 3600 * time.Second
	refreshExpire := 7200 * time.Second
	aToken, rToken, err := GenToken(userId, []byte(secretKey), accessExpire, refreshExpire)
	if err != nil {
		t.Errorf("GenToken error: %v", err)
	}
	t.Logf("aToken: %s\n rToken: %s", aToken, rToken)

	auth := &http.Auth{
		SecretKey: secretKey,
	}
	newRefreshToken, err := RefreshToken(auth, userId, rToken)
	if err != nil {
		t.Errorf("RefreshToken error: %v", err)
	}
	t.Logf("newRefeshToken: %s", newRefreshToken)
}

func TestParseToken(t *testing.T) {

	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJ1c2VySWQiOiIxYjhiZTgyMDE3YmE0ZDQ5ODJkOWU2ZTQyOTQzOGNmOSIsImlzcyI6ImFyY2FkZSIsImV4cCI6MTcyOTYy" +
		"MjU2MywibmJmIjoxNzI5NDA2NTYzfQ." +
		"RfnTfjtvgy2j7GfYpAwW1nG1FWS-m-aW8z_DEK817TY"
	secretKey := "bf284d03-ba65-42d4-a9fe-0d2fbfe61060"
	claims, err := ParseToken(token, secretKey)
	if err != nil {
		t.Errorf("ParseToken error: %v", err)
	}
	t.Logf("claims: %v", claims)
	t.Logf("userId: %s", claims.UserId)
	t.Logf("Issuer: %s", claims.Issuer)
	t.Logf("ExpiresAt: %v", claims.ExpiresAt)
	t.Logf("NotBefore: %v", claims.NotBefore)
	t.Logf("IssuedAt: %v", claims.IssuedAt)
	t.Logf("Subject: %s", claims.Subject)
	t.Logf("Audience: %s", claims.Audience)
}
