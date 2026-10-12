// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

// Package artifact 提供制品身份、保留期限和受限文件访问
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// ID 为同一作业的相对文件名生成稳定业务标识
func ID(projectID, runID, jobRunID, stepRunID, name string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{projectID, runID, jobRunID, stepRunID, name}, "\x00"))).String()
}

// Identity 校验一个不能跨目录的身份字段
func Identity(value string) error {
	if value == "" || len(value) > 64 || value == "." || value == ".." || strings.ContainsAny(value, "/\\:\x00\r\n") {
		return fmt.Errorf("invalid artifact identity")
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return fmt.Errorf("invalid artifact identity")
		}
	}
	return nil
}

// Name 校验规范化的制品相对路径
func Name(value string) error {
	if value == "" || len(value) > 2048 || value == "." || !fs.ValidPath(value) || path.Clean(value) != value || strings.ContainsAny(value, "\\:\x00\r\n") {
		return fmt.Errorf("invalid artifact name")
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return fmt.Errorf("invalid artifact name")
		}
	}
	return nil
}

// Root 返回管理员配置的独立制品目录
func Root() string {
	if root := strings.TrimSpace(os.Getenv("ARCENTRA_ARTIFACT_ROOT")); root != "" {
		return root
	}
	return filepath.Join("data", "artifacts")
}

// LocalPath 返回项目、运行和作业范围内的规范相对位置
func LocalPath(projectID, runID, jobRunID, stepRunID, name string) (string, error) {
	for _, id := range []string{projectID, runID, jobRunID, stepRunID} {
		if err := Identity(id); err != nil {
			return "", err
		}
	}
	if err := Name(name); err != nil {
		return "", err
	}
	return path.Join(projectID, runID, jobRunID, stepRunID, name), nil
}

// Digest 校验规范的小写 SHA256 内容或存储位置摘要
func Digest(value string) error {
	bytes, err := hex.DecodeString(value)
	if err != nil || len(bytes) != sha256.Size || strings.ToLower(value) != value {
		return fmt.Errorf("invalid artifact digest")
	}
	return nil
}

// RemotePath 返回绑定运行、作业、步骤和内容摘要的对象键
func RemotePath(runID, jobRunID, stepRunID, digest, name string) (string, error) {
	for _, id := range []string{runID, jobRunID, stepRunID} {
		if err := Identity(id); err != nil {
			return "", err
		}
	}
	if err := Name(name); err != nil {
		return "", err
	}
	if err := Digest(digest); err != nil {
		return "", err
	}
	return path.Join("artifacts", runID, jobRunID, stepRunID, digest, name), nil
}

// ParseExpiry 解析显式保留期限，空值表示不自动过期
func ParseExpiry(value string, now time.Time) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var duration time.Duration
	var err error
	if strings.HasSuffix(value, "d") {
		days, e := strconv.ParseUint(strings.TrimSuffix(value, "d"), 10, 16)
		if e != nil || days == 0 || days > 3650 {
			return nil, fmt.Errorf("expireIn must be a positive duration no longer than 3650 days")
		}
		duration = time.Duration(days) * 24 * time.Hour
	} else {
		duration, err = time.ParseDuration(value)
	}
	if err != nil || duration <= 0 || duration > 3650*24*time.Hour {
		return nil, fmt.Errorf("expireIn must be a positive duration no longer than 3650 days")
	}
	expires := now.UTC().Add(duration)
	return &expires, nil
}

// StorageFingerprint 绑定对象存储位置，凭据轮换不改变位置身份
func StorageFingerprint(provider, endpoint, bucket, region, basePath string, useTLS bool) string {
	value := struct {
		Provider, Endpoint, Bucket, Region, BasePath string
		TLS                                          bool
	}{provider, endpoint, bucket, region, basePath, useTLS}
	encoded, _ := sonic.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// OpenLocal 逐层固定目录句柄，拒绝符号链接、目录替换和非普通文件
func OpenLocal(ctx context.Context, rootPath, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Name(name); err != nil {
		return nil, err
	}
	root, err := OpenRoot(ctx, rootPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		child, err := openDirectory(ctx, root, part)
		if err != nil {
			return nil, err
		}
		_ = root.Close()
		root = child
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := root.Lstat(parts[len(parts)-1])
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact is not a regular file")
	}
	file, err := root.Open(parts[len(parts)-1])
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		_ = file.Close()
		return nil, fmt.Errorf("artifact changed while opening")
	}
	return file, nil
}

// OpenRoot 从文件系统根逐层打开可信目录，拒绝符号链接
func OpenRoot(ctx context.Context, rootPath string) (*os.Root, error) {
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(absolute, volume+string(filepath.Separator))
	if relative == "" {
		_ = root.Close()
		return nil, fmt.Errorf("artifact root cannot be filesystem root")
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		child, err := openDirectory(ctx, root, part)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root = child
	}
	return root, nil
}

func openDirectory(ctx context.Context, root *os.Root, name string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact directory is not a real directory")
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := child.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		_ = child.Close()
		return nil, fmt.Errorf("artifact directory changed while opening")
	}
	return child, nil
}
