// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package configsecret

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/bytedance/sonic"
)

// IsSecretField 判断配置字段是否需要作为只写凭据处理
func IsSecretField(name string) bool {
	n := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(name))
	if n == "key" {
		return true
	}
	if n == "tokenurl" {
		return false
	}
	for _, part := range []string{"secret", "password", "token", "credential", "privatekey", "accesskey", "apikey", "salt", "authorization", "authheader", "bearer", "signingkey", "encryptionkey"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

func secretURLField(key string, value any) bool {
	text, ok := value.(string)
	return ok && text != "" && (strings.HasSuffix(strings.ToLower(key), "url") || strings.EqualFold(key, "endpoint")) && CredentialBearingURL(text)
}

func secretPlaceholder(value string) bool {
	s := strings.ToLower(strings.TrimSpace(value))
	return s == "[redacted]" || s == "redacted" || s == "<redacted>" || s == "configured" || s == "[configured]" || s == "********" || s == "******" || s == "••••••••"
}

// Updates merge into existing objects. Missing or empty secret fields keep the
// existing value; removal requires explicit deletion of the configuration.
// The returned bool indicates whether the request supplied a secret field.
func containsConfigSecrets(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if IsSecretField(key) || secretURLField(key, child) || containsConfigSecrets(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if containsConfigSecrets(child) {
				return true
			}
		}
	}
	return false
}

// Merge 合并配置，保留被省略、空字符串或 null 的凭据
func Merge(previous, input []byte, all bool) ([]byte, bool, error) {
	old, next := map[string]any{}, map[string]any{}
	if len(previous) != 0 && (sonic.Unmarshal(previous, &old) != nil || old == nil) {
		return nil, false, fmt.Errorf("stored configuration is invalid")
	}
	if len(input) == 0 {
		input = []byte(`{}`)
	}
	if sonic.Unmarshal(input, &next) != nil || next == nil {
		return nil, false, fmt.Errorf("configuration must be a JSON object")
	}
	secretInput := false
	var merge func(any, any) (any, error)
	merge = func(previous, input any) (any, error) {
		switch next := input.(type) {
		case map[string]any:
			prior, ok := previous.(map[string]any)
			if !ok {
				if containsConfigSecrets(previous) {
					return nil, fmt.Errorf("cannot replace a secret-bearing configuration container")
				}
				prior = map[string]any{}
			}
			for key, val := range next {
				if secretURLField(key, val) {
					return nil, fmt.Errorf("configuration URLs must not contain credentials, query parameters or fragments")
				}
				if all || IsSecretField(key) || secretURLField(key, prior[key]) {
					secretInput = true
					if val == nil || val == "" {
						continue
					}
					text, ok := val.(string)
					if !ok || secretPlaceholder(text) {
						return nil, fmt.Errorf("secret fields require a new value or must be omitted to preserve the existing value")
					}
					prior[key] = text
				} else {
					merged, err := merge(prior[key], val)
					if err != nil {
						return nil, err
					}
					prior[key] = merged
				}
			}
			return prior, nil
		case []any:
			prior, ok := previous.([]any)
			if !ok && containsConfigSecrets(previous) {
				return nil, fmt.Errorf("cannot replace a secret-bearing configuration container")
			}
			for index := len(next); index < len(prior); index++ {
				if containsConfigSecrets(prior[index]) {
					return nil, fmt.Errorf("cannot remove secret-bearing configuration entries")
				}
			}
			merged := make([]any, len(next))
			for index, item := range next {
				var oldItem any
				if index < len(prior) {
					oldItem = prior[index]
				}
				value, err := merge(oldItem, item)
				if err != nil {
					return nil, err
				}
				merged[index] = value
			}
			return merged, nil
		default:
			if containsConfigSecrets(previous) {
				return nil, fmt.Errorf("cannot replace a secret-bearing configuration container")
			}
			return input, nil
		}
	}
	merged, err := merge(old, next)
	if err != nil {
		return nil, secretInput, err
	}
	encoded, err := sonic.Marshal(merged)
	return encoded, secretInput, err
}

// CredentialBearingURL 判断 URL 是否含认证信息、查询参数或片段
func CredentialBearingURL(value string) bool {
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "//" + candidate
	}
	parsed, err := url.Parse(candidate)
	return err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != ""
}

// ValidateStorageEndpoint 拒绝包含凭据或查询信息的存储端点
func ValidateStorageEndpoint(raw []byte) error {
	var config map[string]any
	if sonic.Unmarshal(raw, &config) != nil {
		return fmt.Errorf("invalid storage configuration")
	}
	for key, value := range config {
		if strings.EqualFold(key, "endpoint") {
			endpoint, ok := value.(string)
			if !ok || CredentialBearingURL(endpoint) {
				return fmt.Errorf("storage endpoint must not contain credentials, query parameters or fragments")
			}
		}
	}
	return nil
}
