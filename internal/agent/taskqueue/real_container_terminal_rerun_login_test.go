//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/bytedance/sonic"
	"golang.org/x/crypto/bcrypt"
)

type terminalLoginTransport func(*http.Request) (*http.Response, error)

// RoundTrip 检查离线登录请求，不访问外部服务
func (f terminalLoginTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// TestContainerTerminalRerunLoginRequestEncoding 验证真实夹具按生产登录协议发送一次标准 Base64 编码
func TestContainerTerminalRerunLoginRequestEncoding(t *testing.T) {
	// This checks the actual fixture helper's wire contract, not hosted login,
	// Redis-backed authentication or terminal runtime acceptance.
	for _, password := range []string{"synthetic-browser-password!", "c3ludGhldGlj"} {
		t.Run(password, func(t *testing.T) {
			const actor, token = "synthetic-terminal-actor", "synthetic-access-token"
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := 0
			h := &terminalRerunHTTP{
				base:      "http://terminal-fixture.invalid",
				passwords: map[string]string{actor: password},
				tokens:    map[string]string{},
				client: &http.Client{Timeout: time.Second, Transport: terminalLoginTransport(func(request *http.Request) (*http.Response, error) {
					calls++
					if request.Method != http.MethodPost || request.URL.String() != "http://terminal-fixture.invalid/api/v1/users/login" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Authorization") != "" {
						t.Fatal("fixture login request changed endpoint, method or authentication boundary")
					}
					if _, bounded := request.Context().Deadline(); !bounded {
						t.Fatal("fixture login request lost its deadline")
					}
					raw, err := io.ReadAll(io.LimitReader(request.Body, 1025))
					if err != nil || len(raw) > 1024 {
						t.Fatal("fixture login request body is unreadable or oversized")
					}
					var login model.Login
					if sonic.Unmarshal(raw, &login) != nil || login.Username != actor || login.Password != base64.StdEncoding.EncodeToString([]byte(password)) {
						t.Fatal("fixture login must send the plaintext password encoded exactly once as standard base64")
					}
					// Production UserService.Login performs this decode before bcrypt.
					decoded, err := base64.StdEncoding.DecodeString(login.Password)
					if err != nil || bcrypt.CompareHashAndPassword(hash, decoded) != nil {
						t.Fatal("fixture request does not satisfy production password verification")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":200,"detail":{"token":{"accessToken":"` + token + `"}}}`)), Request: request}, nil
				})},
			}
			h.login(t, ctx, actor)
			if calls != 1 || h.tokens[actor] != token || h.passwords[actor] != password {
				t.Fatal("fixture login changed request count, token handling or browser plaintext password")
			}
		})
	}
}
