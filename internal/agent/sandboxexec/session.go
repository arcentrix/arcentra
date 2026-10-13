// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

// Package sandboxexec 提供显式启用的离线作业持久会话与 Shell 适配器
package sandboxexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/google/uuid"
)

const containerWorkspace = "/workspace"
const cleanupTimeout = 30 * time.Second

// Backend 定义单作业会话所需的运行时操作，后端连接仍由调用方管理
// Remove 必须停止所有作业进程并释放挂载后才返回成功
// Create 在资源状态不确定时必须返回非空 ID，以便调用方继续清理
// Execute 必须遵守上下文取消，并在返回前完成输出写入
type Backend interface {
	Create(context.Context, *sandbox.CreateOptions) (string, error)
	Start(context.Context, string) error
	Execute(context.Context, string, []string, *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error)
	Remove(context.Context, string) error
}

// Options 定义可信宿主配置，不得从流水线参数直接填充根目录或镜像策略
// WorkspaceRoot 必须已存在且禁止非所有者写入，作业仅获得新建子目录的挂载
// 持久会话要求显式非零 UID:GID，旧版 NewSession 才允许空值继承 Agent UID:GID
// 不改变宿主文件所有权，运行模式及实际 UID 绑定由可信生产配置验证
type Options struct {
	WorkspaceRoot        string
	JobRunID             string
	PipelineRunID        string
	ExecutionClaimID     string
	RuntimeBindingDigest string
	Image                string
	Resources            *sandbox.Resources
	User                 string
}

// Session 持有一个容器和专属工作目录，步骤串行执行且不会回退到宿主执行器
// Close 成功会删除工作目录，调用方必须先完成可信制品上传
// 通过 Controller 分配的会话提供持久化恢复，但不构成不可信多租户代码的安全保证
type Session struct {
	backend                                           Backend
	root                                              *os.Root
	rootPath, directory, workspace, containerID, user string
	workspaceInfo                                     os.FileInfo
	gate                                              chan struct{}
	mu                                                sync.Mutex
	activeCancel                                      context.CancelFunc
	closing, ready, removed, cleaned                  bool
	sealed                                            bool
	controller                                        *Controller
	recordID                                          string
	createOptions                                     *sandbox.CreateOptions
	resourceRef                                       sandbox.ResourceRef
}

// NewSession 兼容旧版离线测试并立即启动容器，不提供崩溃恢复
// 生产候选路径必须使用 OpenController、Allocate、Start 的持久化生命周期
// 若失败时返回非空会话，清理尚未成功，调用方必须保留会话并重试 Close
func NewSession(ctx context.Context, backend Backend, opts Options) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, fmt.Errorf("sandbox backend is required")
	}
	if !jobIDPattern.MatchString(opts.JobRunID) {
		return nil, fmt.Errorf("invalid job run ID")
	}
	if strings.TrimSpace(opts.Image) == "" {
		return nil, fmt.Errorf("an operator-approved image is required")
	}
	if opts.Resources == nil || opts.Resources.CPU == "" || opts.Resources.Memory == "" {
		return nil, fmt.Errorf("explicit CPU and memory limits are required")
	}
	user := opts.User
	if user == "" {
		user = fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	}
	if err := validateUser(user); err != nil {
		return nil, err
	}
	rootPath := filepath.Clean(opts.WorkspaceRoot)
	if !filepath.IsAbs(opts.WorkspaceRoot) {
		return nil, fmt.Errorf("workspace root must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	if canonical != rootPath {
		return nil, fmt.Errorf("workspace root must not contain symbolic links")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("inspect workspace root: %w", err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		_ = root.Close()
		return nil, fmt.Errorf("workspace root must not be group/other-writable")
	}
	s := &Session{backend: backend, root: root, rootPath: rootPath, directory: "job-" + opts.JobRunID + "-" + uuid.NewString(), user: user, gate: make(chan struct{}, 1)}
	s.workspace = filepath.Join(rootPath, s.directory)
	if err := root.Mkdir(s.directory, 0o700); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create job workspace: %w", err)
	}
	s.workspaceInfo, err = root.Lstat(s.directory)
	if err == nil {
		err = s.verifyWorkspace()
	}
	if err != nil {
		return s.failed(ctx, err)
	}
	resources := *opts.Resources
	s.containerID, err = backend.Create(ctx, &sandbox.CreateOptions{
		Image:      opts.Image,
		Command:    []string{"sh", "-ec", "trap 'exit 0' TERM INT; while :; do sleep 1; done"},
		WorkingDir: "/", NetworkMode: "none", Resources: &resources,
		Mounts: []sandbox.Mount{{Source: s.workspace, Target: containerWorkspace, Type: "bind"}},
		Labels: map[string]string{"io.arcentra.job-run": opts.JobRunID},
	})
	if err != nil {
		return s.failed(ctx, fmt.Errorf("create sandbox: %w", err))
	}
	if s.containerID == "" {
		return s.failed(ctx, fmt.Errorf("sandbox backend returned an empty container ID"))
	}
	if err := backend.Start(ctx, s.containerID); err != nil {
		return s.failed(ctx, fmt.Errorf("start sandbox: %w", err))
	}
	s.ready = true
	return s, nil
}

var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func validateUser(user string) error {
	parts := strings.Split(user, ":")
	if len(parts) > 2 {
		return fmt.Errorf("sandbox user must be numeric UID or UID:GID")
	}
	for _, part := range parts {
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return fmt.Errorf("sandbox user must be numeric UID or UID:GID")
		}
	}
	return nil
}

func (s *Session) failed(ctx context.Context, cause error) (*Session, error) {
	if err := s.Close(ctx); err != nil {
		return s, errors.Join(cause, err)
	}
	return nil, cause
}

// Workspace 返回会话分配的宿主目录，仅供可信源码和制品准备使用
func (s *Session) Workspace() string { return s.workspace }

func (s *Session) verifyWorkspace() error {
	rootInfo, err := s.root.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect workspace root: %w", err)
	}
	pathInfo, err := os.Lstat(s.rootPath)
	if err != nil || !os.SameFile(rootInfo, pathInfo) {
		return fmt.Errorf("workspace root path changed")
	}
	info, err := s.root.Lstat(s.directory)
	if err != nil || !info.IsDir() || !os.SameFile(s.workspaceInfo, info) {
		return fmt.Errorf("job workspace path changed")
	}
	return nil
}

func (s *Session) acquire(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Session) release() { <-s.gate }

// Close 取消当前步骤，先移除容器再删除工作目录，失败后可重复调用
// 清理使用保留上下文值但独立于调用方取消的 30 秒期限
func (s *Session) Close(ctx context.Context) error {
	if s.controller != nil {
		return s.closeDurable(ctx)
	}
	s.mu.Lock()
	s.closing = true
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.mu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := s.acquire(cleanupCtx); err != nil {
		return fmt.Errorf("wait for sandbox cleanup: %w", err)
	}
	defer s.release()
	if s.cleaned {
		return nil
	}
	if !s.removed && s.containerID != "" {
		if err := s.backend.Remove(cleanupCtx, s.containerID); err != nil {
			return fmt.Errorf("remove sandbox: %w", err)
		}
	}
	s.removed = true
	// Root.RemoveAll 不会跟随逃逸符号链接；宿主根目录必须持续由可信 Agent 控制
	if err := s.root.RemoveAll(s.directory); err != nil {
		return fmt.Errorf("remove workspace: %w", err)
	}
	s.cleaned = true
	return s.root.Close()
}
