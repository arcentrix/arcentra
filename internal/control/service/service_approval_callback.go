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

package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/bytedance/sonic"
)

// ApprovalCallbackURLBuilder generates HMAC-signed callback URLs for approval actions.
type ApprovalCallbackURLBuilder struct {
	settingRepo repo.ISettingRepository
}

// NewApprovalCallbackURLBuilder creates a new builder.
func NewApprovalCallbackURLBuilder(settingRepo repo.ISettingRepository) *ApprovalCallbackURLBuilder {
	return &ApprovalCallbackURLBuilder{
		settingRepo: settingRepo,
	}
}

// BuildURL generates a signed callback URL for the given approval action.
// Format: {externalURL}/api/v1/approvals/{approvalID}/{action}?token={hmac}&expires={ts}
func (b *ApprovalCallbackURLBuilder) BuildURL(ctx context.Context, approvalID, action string) (string, error) {
	baseURL, err := b.getExternalURL(ctx)
	if err != nil {
		return "", fmt.Errorf("get external URL: %w", err)
	}

	secret, err := b.getSecretKey(ctx)
	if err != nil {
		return "", fmt.Errorf("get secret key: %w", err)
	}

	expires := time.Now().Add(72 * time.Hour).Unix()
	token := signApprovalToken(approvalID, action, expires, secret)

	return fmt.Sprintf("%sapi/v1/approvals/%s/%s?token=%s&expires=%d",
		ensureTrailingSlash(baseURL), approvalID, action, token, expires), nil
}

// VerifyToken validates an approval callback token.
func (b *ApprovalCallbackURLBuilder) VerifyToken(ctx context.Context, approvalID, action, token string, expires int64) (bool, error) {
	if time.Now().Unix() > expires {
		return false, fmt.Errorf("token expired")
	}
	secret, err := b.getSecretKey(ctx)
	if err != nil {
		return false, err
	}
	expected := signApprovalToken(approvalID, action, expires, secret)
	return hmac.Equal([]byte(token), []byte(expected)), nil
}

func (b *ApprovalCallbackURLBuilder) getExternalURL(ctx context.Context) (string, error) {
	setting, err := b.settingRepo.Get(ctx, consts.SettingNameExternalURL)
	if err != nil {
		return "", err
	}
	var values map[string]string
	if err := sonic.Unmarshal(setting.Value, &values); err != nil {
		return "", fmt.Errorf("unmarshal EXTERNAL_URL value: %w", err)
	}
	url, ok := values[consts.SettingKeyExternalURL]
	if !ok || url == "" {
		return "", fmt.Errorf("EXTERNAL_URL not set in setting value")
	}
	return url, nil
}

func (b *ApprovalCallbackURLBuilder) getSecretKey(ctx context.Context) (string, error) {
	setting, err := b.settingRepo.Get(ctx, consts.SettingNameAgentSecretKey)
	if err != nil {
		return "", err
	}
	var values map[string]string
	if err := sonic.Unmarshal(setting.Value, &values); err != nil {
		return "", fmt.Errorf("unmarshal AGENT_SECRET_KEY value: %w", err)
	}
	key, ok := values[consts.SettingKeySecretKey]
	if !ok || key == "" {
		return "", fmt.Errorf("secret_key not set in AGENT_SECRET_KEY value")
	}
	return key, nil
}

func signApprovalToken(approvalID, action string, expires int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(approvalID + ":" + action + ":" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func ensureTrailingSlash(url string) string {
	if len(url) > 0 && url[len(url)-1] != '/' {
		return url + "/"
	}
	return url
}
