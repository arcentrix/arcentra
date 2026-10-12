// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

// Package sourcefile 从固定 GitHub 来源读取明确指定的提交和普通文件
//
// 使用官方 Git database REST 接口，不使用可能解引用符号链接的 Contents 接口
// https://docs.github.com/en/rest/git/commits#get-a-commit-object
// https://docs.github.com/en/rest/git/trees#get-a-tree
// https://docs.github.com/en/rest/git/blobs#get-a-blob
package sourcefile

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bytedance/sonic"
)

const maxFileBytes = 8192
const maxMetadataBytes = 1024 * 1024
const maxTreeEntries = 2000
const maxPathDepth = 8

// ErrUnavailable 不包含服务端响应、仓库内容、令牌或网络细节
var ErrUnavailable = errors.New("selected repository file is unavailable or unsupported")

var repositoryPart = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var sensitivePath = regexp.MustCompile(`(?i)(secret|token|password|passwd|credential|private.?key|api.?key|access.?key|authorization|cookie)`)
var objectID = regexp.MustCompile(`^[a-f0-9]{40}$`)

// File 标识从指定提交树解析并核对摘要的普通文件
// Commit 为 Agent 报告的初始提交，不代表构建证明
type File struct {
	Repository string
	Commit     string
	Path       string
	BlobSHA    string
	Mode       string
	Content    []byte
}

// Reader 仅访问 api.github.com，不接受自定义目标或下载链接
type Reader struct{ client *http.Client }

// NewReader 创建严格 TLS、无代理、无重定向的固定来源读取器
func NewReader() *Reader {
	transport := &http.Transport{
		Proxy:                  nil,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:            publicGitHubDial,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 32 * 1024,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     true,
	}
	return &Reader{client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// CanonicalRepository 只接受 GitHub.com 的无凭据 HTTPS 仓库地址
func CanonicalRepository(raw string) (string, error) {
	if raw != strings.TrimSpace(raw) || len(raw) > 512 {
		return "", ErrUnavailable
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.Contains(raw, "%") {
		return "", ErrUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", ErrUnavailable
	}
	parts[1] = strings.TrimSuffix(parts[1], ".git")
	for _, p := range parts {
		if !repositoryPart.MatchString(p) || p == "." || p == ".." {
			return "", ErrUnavailable
		}
	}
	return "https://github.com/" + strings.ToLower(parts[0]) + "/" + strings.ToLower(parts[1]), nil
}

// ValidPath 只接受有界且规范化的相对文件路径
func ValidPath(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\%\x00\r\n\t") {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > maxPathDepth {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.HasPrefix(p, ".") || sensitivePath.MatchString(p) || strings.EqualFold(p, "id_rsa") || strings.EqualFold(p, "id_ed25519") || strings.HasPrefix(strings.ToLower(p), "kubeconfig") {
			return false
		}
		for _, c := range p {
			if unicode.IsControl(c) {
				return false
			}
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".py", ".rs", ".java", ".c", ".h", ".cpp", ".cs", ".rb", ".sh", ".sql", ".yaml", ".yml", ".json", ".toml", ".xml", ".md", ".txt", ".css", ".html":
		return true
	}
	return strings.EqualFold(path.Base(name), "Dockerfile") || strings.EqualFold(path.Base(name), "Makefile")
}

// ReadFileAtCommit 按完整提交及显式路径逐级读取树，仅接受普通文本 blob
func (r *Reader) ReadFileAtCommit(ctx context.Context, repository, commit, name, token string) (File, error) {
	out := File{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	canonical, err := CanonicalRepository(repository)
	if err != nil || !objectID.MatchString(commit) || !ValidPath(name) || len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") || r == nil || r.client == nil {
		return out, sourceError(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	base := "https://api.github.com/repos/" + strings.TrimPrefix(canonical, "https://github.com/") + "/git/"
	var revision struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := r.get(ctx, base+"commits/"+commit, token, maxMetadataBytes, &revision); err != nil || revision.SHA != commit || !objectID.MatchString(revision.Tree.SHA) {
		return out, sourceError(ctx)
	}
	treeSHA := revision.Tree.SHA
	parts := strings.Split(name, "/")
	var selected treeEntry
	for index, segment := range parts {
		var tree struct {
			SHA       string      `json:"sha"`
			Truncated bool        `json:"truncated"`
			Entries   []treeEntry `json:"tree"`
		}
		// recursive 参数连 false 都会启用递归，因此完全不传该参数
		if err := r.get(ctx, base+"trees/"+treeSHA, token, maxMetadataBytes, &tree); err != nil || tree.SHA != treeSHA || tree.Truncated || len(tree.Entries) > maxTreeEntries {
			return out, sourceError(ctx)
		}
		found := false
		for _, entry := range tree.Entries {
			if entry.Path != segment {
				continue
			}
			if found || !objectID.MatchString(entry.SHA) {
				return out, sourceError(ctx)
			}
			selected, found = entry, true
		}
		if !found {
			return out, sourceError(ctx)
		}
		if index < len(parts)-1 {
			if selected.Type != "tree" || selected.Mode != "040000" {
				return out, sourceError(ctx)
			}
			treeSHA = selected.SHA
		} else if selected.Type != "blob" || (selected.Mode != "100644" && selected.Mode != "100755") || selected.Size == nil || *selected.Size < 0 || *selected.Size > maxFileBytes {
			return out, sourceError(ctx)
		}
	}
	var blob struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Size     int    `json:"size"`
		Content  string `json:"content"`
	}
	if err := r.get(ctx, base+"blobs/"+selected.SHA, token, 32*1024, &blob); err != nil || blob.SHA != selected.SHA || blob.Encoding != "base64" || blob.Size < 0 || blob.Size > maxFileBytes || blob.Size != *selected.Size {
		return out, sourceError(ctx)
	}
	content, err := base64.StdEncoding.DecodeString(blob.Content)
	if err != nil || len(content) != blob.Size || len(content) > maxFileBytes || !utf8.Valid(content) || strings.ContainsRune(string(content), 0) || strings.HasPrefix(string(content), "version https://git-lfs.github.com/spec/v1") {
		return out, sourceError(ctx)
	}
	hash := sha1.New() // GitHub Git objects use the Git SHA-1 object framing
	_, _ = fmt.Fprintf(hash, "blob %d\x00", len(content))
	_, _ = hash.Write(content)
	if hex.EncodeToString(hash.Sum(nil)) != selected.SHA {
		return out, sourceError(ctx)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return File{Repository: canonical, Commit: commit, Path: name, BlobSHA: selected.SHA, Mode: selected.Mode, Content: content}, nil
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size *int   `json:"size"`
}

func (r *Reader) get(ctx context.Context, address, token string, limit int64, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.RawQuery != "" {
		return sourceError(ctx)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := r.client.Do(req)
	if err != nil {
		return sourceError(ctx)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		return sourceError(ctx)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit || sonic.Unmarshal(body, out) != nil {
		return sourceError(ctx)
	}
	return nil
}

func publicGitHubDial(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return dialGitHub(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func dialGitHub(ctx context.Context, network, address string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "api.github.com" || port != "443" || network != "tcp" {
		return nil, ErrUnavailable
	}
	addresses, err := lookup(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrUnavailable
	}
	for _, ip := range addresses {
		if !publicIP(ip) {
			return nil, ErrUnavailable
		}
	}
	for _, ip := range addresses {
		// 使用已验证 IP 拨号，避免在检查之后再次解析目标
		conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, ErrUnavailable
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100::/64", "fec0::/10", "192.88.99.0/24", "3fff::/20", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/23", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

func sourceError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnavailable
}
