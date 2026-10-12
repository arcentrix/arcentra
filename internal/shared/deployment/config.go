// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package deployment

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config 定义仅由操作员提供的部署目标，默认禁用且无文件或网络访问
type Config struct {
	Enabled bool           `mapstructure:"enabled" json:"enabled"`
	Targets []TargetConfig `mapstructure:"targets" json:"-"`
}

// TargetConfig 精确限定项目和工作负载，不接受 kubeconfig 执行插件
type TargetConfig struct {
	// Tier 为操作员设置的发布权限等级，省略时按生产环境处理
	AllowPipelineDeployments bool   `mapstructure:"allowPipelineDeployments" json:"allowPipelineDeployments"`
	Tier                     string `mapstructure:"tier" json:"tier"`
	Ref                      string `mapstructure:"ref" json:"ref"`
	ProjectID                string `mapstructure:"projectId" json:"projectId"`
	Server                   string `mapstructure:"server" json:"-"`
	CAFile                   string `mapstructure:"caFile" json:"-"`
	BearerTokenFile          string `mapstructure:"bearerTokenFile" json:"-"`
	Namespace                string `mapstructure:"namespace" json:"namespace"`
	Deployment               string `mapstructure:"deployment" json:"deployment"`
	Container                string `mapstructure:"container" json:"container"`
}

var errConfiguration = errors.New("invalid deployment target configuration; check HTTPS server, exact target, and mounted CA/token files")

func boundedFile(ctx context.Context, path string, maximum int64) ([]byte, error) {
	if ctx.Err() != nil || !filepath.IsAbs(path) {
		return nil, errConfiguration
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errConfiguration
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errConfiguration
	}
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(value)) > maximum || ctx.Err() != nil {
		return nil, errConfiguration
	}
	return value, nil
}

type tokenTransport struct {
	transport http.RoundTripper
	token     string
}

func (t tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.transport.RoundTrip(copy)
}

// NewConfiguredAdapter 校验 TLS 并读取已有挂载凭据，不创建凭据或访问集群
func NewConfiguredAdapter(ctx context.Context, config TargetConfig) (*KubernetesAdapter, error) {
	if !ValidTarget(Target{Namespace: config.Namespace, Deployment: config.Deployment, Container: config.Container}) {
		return nil, errConfiguration
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if config.CAFile != "" {
		pem, err := boundedFile(ctx, config.CAFile, 1024*1024)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, errConfiguration
		}
	}
	raw, err := boundedFile(ctx, config.BearerTokenFile, 64*1024)
	if err != nil {
		return nil, errConfiguration
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errConfiguration
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 60 * time.Second}
	adapter, err := NewKubernetesAdapter(config.Server, &http.Client{Transport: tokenTransport{transport, token}})
	if err != nil {
		return nil, errConfiguration
	}
	return adapter, nil
}
