// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package copilot

import (
	"encoding/base64"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var sensitiveKey = regexp.MustCompile(`(?i)(secret|token|password|passwd|credential|private.?key|api.?key|access.?key|authorization|cookie)`)
var assignments = regexp.MustCompile(`(?im)^.*(?:secret|token|password|passwd|credential|private.?key|api.?key|access.?key|authorization|cookie)[\w ."'_-]*\s*[:=].*$`)
var privateKeys = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)
var tokens = regexp.MustCompile(`\b(?:sk-(?:ant-)?[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|AKIA[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
var bearer = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9+/_.=:-]+`)
var urlCredentials = regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+@`)

// ValidID 拒绝路径、分隔符和非限定长度的标识
func ValidID(id string) bool { return identifier.MatchString(id) }

// SensitiveKey 判断环境变量是否应作为已知秘密脱敏
func SensitiveKey(key string) bool { return sensitiveKey.MatchString(key) }

// CodeArtifactName 只允许注册制品的常用源码文本名称，拒绝秘密文件和任意路径
func CodeArtifactName(name string) bool {
	if artifactutil.Name(name) != nil || strings.ContainsAny(name, "\\\x00") || strings.Contains(name, "%") || path.Clean(name) != name {
		return false
	}
	for _, segment := range strings.Split(strings.ToLower(name), "/") {
		if strings.HasPrefix(segment, ".") || sensitiveKey.MatchString(segment) || segment == "id_rsa" || segment == "id_ed25519" || strings.HasPrefix(segment, "kubeconfig") || segment == "credentials" {
			return false
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".py", ".rs", ".java", ".c", ".h", ".cpp", ".cs", ".rb", ".sh", ".sql", ".yaml", ".yml", ".json", ".toml", ".xml", ".md", ".txt", ".css", ".html":
		return true
	}
	switch strings.ToLower(path.Base(name)) {
	case "dockerfile", "makefile":
		return true
	}
	return false
}

// Redactor 在截断之前处理已知秘密、常见令牌和整行凭据赋值
// 不能保证识别任意未登记秘密，因此管理员仍需批准数据出口和项目范围
type Redactor struct{ values []string }

// NewRedactor 同时去除已知秘密的常见编码形式
func NewRedactor(values []string) *Redactor {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = true
		seen[url.QueryEscape(value)] = true
		seen[base64.StdEncoding.EncodeToString([]byte(value))] = true
		seen[base64.RawStdEncoding.EncodeToString([]byte(value))] = true
	}
	r := &Redactor{}
	for value := range seen {
		r.values = append(r.values, value)
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

// Redact 返回脱敏后的 UTF-8 文本
func (r *Redactor) Redact(value string) string {
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	value = privateKeys.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = assignments.ReplaceAllString(value, "[REDACTED CREDENTIAL LINE]")
	value = tokens.ReplaceAllString(value, "[REDACTED TOKEN]")
	value = bearer.ReplaceAllString(value, "[REDACTED AUTH]")
	value = urlCredentials.ReplaceAllString(value, "${1}[REDACTED]@")
	return strings.ToValidUTF8(value, "")
}

// Prefix 截取不超过上限且保持 UTF-8 完整的前缀
func Prefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
