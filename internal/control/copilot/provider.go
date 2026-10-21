// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

// Package copilot 提供有界、只读且默认关闭的模型适配器
package copilot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/wire"
)

// ProviderSet 保留独立适配器包的依赖注入入口
var ProviderSet = wire.NewSet(NewRegistry)

// Config 只允许管理员在服务端配置模型和明确允许发送数据的项目
// API 密钥仅引用环境变量，禁止由浏览器请求传入
type Config struct {
	Enabled           bool                `mapstructure:"enabled" json:"enabled"`
	AllowedProjectIDs []string            `mapstructure:"allowedProjectIds" json:"allowedProjectIds"`
	Providers         []ProviderConfig    `mapstructure:"providers" json:"-"`
	Execution         ExecutionConfig     `mapstructure:"execution" json:"-"`
	History           HistoryConfig       `mapstructure:"history" json:"-"`
	SourceContext     SourceContextConfig `mapstructure:"sourceContext" json:"-"`
}

// ExecutionConfig independently enables explicit, bound execution capabilities.
type ExecutionConfig struct {
	TerminalRerunEnabled bool `mapstructure:"terminalRerunEnabled" json:"terminalRerunEnabled"`
	RetryApprovalEnabled bool `mapstructure:"retryApprovalEnabled" json:"retryApprovalEnabled"`
}

// HistoryConfig enables private encrypted history only with the managed AES key.
type HistoryConfig struct {
	Enabled bool `mapstructure:"enabled" json:"enabled"`
}

// SourceContextConfig 独立控制允许读取固定 GitHub 来源的项目，默认关闭
type SourceContextConfig struct {
	Enabled           bool     `mapstructure:"enabled"`
	AllowedProjectIDs []string `mapstructure:"allowedProjectIds"`
}

// ProviderConfig 描述经过管理员批准的固定端点和模型白名单
type ProviderConfig struct {
	ID        string   `mapstructure:"id"`
	Kind      string   `mapstructure:"kind"`
	BaseURL   string   `mapstructure:"baseUrl"`
	APIKeyEnv string   `mapstructure:"apiKeyEnv"`
	Models    []string `mapstructure:"models"`
	// TokenLimitField 仅兼容适配器可选择旧版 max_tokens，默认 max_completion_tokens
	TokenLimitField string `mapstructure:"tokenLimitField"`
}

// ModelOption 是浏览器可见的已配置模型，不表示已探测连接成功
type ModelOption struct {
	ProviderID string `json:"providerId"`
	Kind       string `json:"kind"`
	Model      string `json:"model"`
}

// Input 是内部模型输入，不包含可执行工具定义
type Input struct {
	Model, System, User string
	MaxOutputTokens     int
}

// Output 只接受文本结果和结束原因
type Output struct {
	Text      string
	Truncated bool
}

// Provider 表示支持上下文取消的只读文本分析器
type Provider interface {
	Analyze(context.Context, Input) (Output, error)
}

// 公共错误不包含上游响应体、端点 URL 或凭据
var (
	ErrUnconfigured    = errors.New("Copilot is not configured for this project")
	ErrProvider        = errors.New("model provider request failed")
	ErrRateLimited     = errors.New("model provider rate limit reached; try again later")
	ErrInvalidResponse = errors.New("model provider returned an invalid or unsupported response")
)

// Registry 保存固定端点的适配器和模型白名单
type Registry struct {
	generation string
	options    []ModelOption
	providers  map[string]Provider
	allowed    map[string]bool
	secrets    []string
}

// NewRegistry 创建默认关闭的适配器目录，不发起网络探测
func NewRegistry(config Config) *Registry {
	generation := make([]byte, 32)
	if _, err := rand.Read(generation); err != nil {
		return &Registry{}
	}
	r := &Registry{generation: hex.EncodeToString(generation), providers: map[string]Provider{}, allowed: map[string]bool{}}
	if !config.Enabled {
		return r
	}
	for _, id := range config.AllowedProjectIDs {
		if ValidID(id) {
			r.allowed[id] = true
		}
	}
	seen := map[string]bool{}
	for _, c := range config.Providers {
		if seen[c.ID] {
			return &Registry{}
		}
		seen[c.ID] = true
		provider, _, key, err := newHTTPProvider(c)
		if err != nil {
			continue
		}
		r.providers[c.ID] = provider
		r.secrets = append(r.secrets, key)
		for _, m := range c.Models {
			r.options = append(r.options, ModelOption{ProviderID: c.ID, Kind: c.Kind, Model: m})
		}
	}
	return r
}

// Options 返回项目被明确允许使用的模型白名单
func (r *Registry) Options(projectID string) []ModelOption {
	options := []ModelOption{}
	if r == nil || !r.allowed[projectID] {
		return options
	}
	return append(options, r.options...)
}

// Resolve 按项目、适配器和模型三重白名单选择固定模型
func (r *Registry) Resolve(projectID, providerID, model string) (Provider, error) {
	for _, o := range r.Options(projectID) {
		if o.ProviderID == providerID && o.Model == model {
			return r.providers[providerID], nil
		}
	}
	return nil, ErrUnconfigured
}

// Binding is an opaque per-registry generation. A registry/configuration replacement
// invalidates prior transmission consent without exposing credential fingerprints.
func (r *Registry) Binding(projectID, providerID, model string) (string, error) {
	if _, err := r.Resolve(projectID, providerID, model); err != nil {
		return "", err
	}
	if r.generation == "" {
		return "", ErrUnconfigured
	}
	return r.generation, nil
}

// ConfigurationBinding seals the effective fixed destination/model/credential
// tuple with a service-owned secret key. It is stable across equal-config
// instances and exposes neither the tuple nor a standalone credential hash.
func (r *Registry) ConfigurationBinding(projectID, providerID, model string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrUnconfigured
	}
	provider, err := r.Resolve(projectID, providerID, model)
	if err != nil {
		return "", err
	}
	p, ok := provider.(*httpProvider)
	if !ok {
		return "", ErrUnconfigured
	}
	raw, err := sonic.Marshal([]string{"copilot-provider-binding-v1", projectID, providerID, model, p.kind, p.endpoint, p.apiKey, p.tokenField})
	if err != nil {
		return "", ErrUnconfigured
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Secrets 返回必须从输入和输出中去除的服务端模型凭据副本
func (r *Registry) Secrets() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.secrets...)
}

type httpProvider struct {
	kind, endpoint, apiKey, tokenField string
	client                             *http.Client
}

func newHTTPProvider(c ProviderConfig) (*httpProvider, string, string, error) {
	if !ValidID(c.ID) || (c.Kind != "openai-compatible" && c.Kind != "anthropic") || len(c.Models) == 0 || len(c.Models) > 20 {
		return nil, "", "", ErrUnconfigured
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, "", "", ErrUnconfigured
	}
	for _, m := range c.Models {
		if strings.TrimSpace(m) != m || m == "" || len(m) > 160 || strings.ContainsAny(m, "\r\n\x00") {
			return nil, "", "", ErrUnconfigured
		}
	}
	if !envName.MatchString(c.APIKeyEnv) {
		return nil, "", "", ErrUnconfigured
	}
	key := os.Getenv(c.APIKeyEnv)
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, "", "", ErrUnconfigured
	}
	field := c.TokenLimitField
	if field == "" {
		field = "max_completion_tokens"
	}
	if field != "max_completion_tokens" && field != "max_tokens" {
		return nil, "", "", ErrUnconfigured
	}
	suffix := "/chat/completions"
	if c.Kind == "anthropic" {
		suffix = "/messages"
	}
	u.Path = strings.TrimRight(u.Path, "/") + suffix
	u.RawPath = ""
	return &httpProvider{kind: c.Kind, endpoint: u.String(), apiKey: key, tokenField: field, client: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, u.Host, "" + key, nil
}

func (p *httpProvider) Analyze(ctx context.Context, input Input) (Output, error) {
	if len(input.User)+len(input.System) > 32768 || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 4096 {
		return Output{}, ErrInvalidResponse
	}
	var body any
	if p.kind == "anthropic" {
		body = map[string]any{"model": input.Model, "system": input.System, "messages": []map[string]string{{"role": "user", "content": input.User}}, "max_tokens": input.MaxOutputTokens, "stream": false}
	} else {
		body = map[string]any{"model": input.Model, "messages": []map[string]string{{"role": "system", "content": input.System}, {"role": "user", "content": input.User}}, p.tokenField: input.MaxOutputTokens, "stream": false, "store": false}
	}
	raw, err := sonic.Marshal(body)
	if err != nil {
		return Output{}, ErrProvider
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(raw))
	if err != nil {
		return Output{}, ErrProvider
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.kind == "anthropic" {
		req.Header.Set("x-api-key", p.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Output{}, ctx.Err()
		}
		return Output{}, ErrProvider
	}
	defer response.Body.Close()
	if response.StatusCode == 429 {
		return Output{}, ErrRateLimited
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Output{}, ErrProvider
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 512*1024+1))
	if err != nil {
		if ctx.Err() != nil {
			return Output{}, ctx.Err()
		}
		return Output{}, ErrProvider
	}
	if len(raw) > 512*1024 {
		return Output{}, ErrInvalidResponse
	}
	var out Output
	if p.kind == "anthropic" {
		var result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
		}
		if sonic.Unmarshal(raw, &result) != nil {
			return out, ErrInvalidResponse
		}
		if result.StopReason != "end_turn" && result.StopReason != "max_tokens" && result.StopReason != "stop_sequence" {
			return out, ErrInvalidResponse
		}
		for _, part := range result.Content {
			if part.Type != "text" {
				return Output{}, ErrInvalidResponse
			}
			out.Text += part.Text
		}
		out.Truncated = result.StopReason == "max_tokens"
	} else {
		var result struct {
			Choices []struct {
				Message struct {
					Content      string `json:"content"`
					ToolCalls    []any  `json:"tool_calls"`
					FunctionCall any    `json:"function_call"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if sonic.Unmarshal(raw, &result) != nil || len(result.Choices) != 1 {
			return out, ErrInvalidResponse
		}
		choice := result.Choices[0]
		if len(choice.Message.ToolCalls) > 0 || choice.Message.FunctionCall != nil || (choice.FinishReason != "stop" && choice.FinishReason != "length") {
			return out, ErrInvalidResponse
		}
		out.Text = choice.Message.Content
		out.Truncated = choice.FinishReason == "length"
	}
	if strings.TrimSpace(out.Text) == "" || len(out.Text) > 65536 {
		return Output{}, ErrInvalidResponse
	}
	return out, nil
}
