// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

type initialSourceCheckoutContextKey struct{}

type initialSourceCheckoutEvidence struct {
	bindingDigest  string
	resolvedCommit string
}

func withInitialSourceCheckout(ctx context.Context, checkout *agentv1.SourceCheckout) context.Context {
	if checkout == nil {
		return ctx
	}
	// 值拷贝使内存上报重试也不依赖工作区或可变的任务负载
	return context.WithValue(ctx, initialSourceCheckoutContextKey{}, initialSourceCheckoutEvidence{
		bindingDigest: checkout.BindingDigest, resolvedCommit: checkout.ResolvedCommit,
	})
}

func initialSourceCheckout(ctx context.Context) *agentv1.SourceCheckout {
	checkout, ok := ctx.Value(initialSourceCheckoutContextKey{}).(initialSourceCheckoutEvidence)
	if !ok {
		return nil
	}
	return &agentv1.SourceCheckout{BindingDigest: checkout.bindingDigest, ResolvedCommit: checkout.resolvedCommit}
}

func validateJobSourceBinding(payload *taskqueue.JobRunTaskPayload) error {
	src := payload.Source
	if src == nil || src.BindingDigest == "" {
		return nil
	}
	// v1 绑定尚不包含显式提交，不能允许额外的 checkout 参数绕过身份核对
	if src.Commit != "" {
		return fmt.Errorf("bound source does not support an explicit commit")
	}
	binding, err := sourceprovenance.NewBinding(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, src.Type, src.Repo, src.Branch)
	if err != nil {
		return err
	}
	if binding.Digest != src.BindingDigest {
		return fmt.Errorf("source binding does not match the dispatched job")
	}
	return nil
}

// handleSourceClone 在最终 checkout 后立即固定初始提交，后续制品、步骤和整作业重试不能重新取证
func handleSourceClone(ctx context.Context, src *taskqueue.SourcePayload, workspace string) (*agentv1.SourceCheckout, error) {
	return handleSourceCloneAtCommit(ctx, src, workspace, "")
}

// handleSourceCloneAtCommit 仅接受已验证控制面清单的提交，不改变原始源请求
func handleSourceCloneAtCommit(ctx context.Context, src *taskqueue.SourcePayload, workspace, pinnedCommit string) (*agentv1.SourceCheckout, error) {
	if pinnedCommit != "" && !sourceprovenance.ValidCommit(pinnedCommit) {
		return nil, fmt.Errorf("pinned source commit is invalid")
	}
	if src == nil || src.Type == "" {
		return nil, nil
	}
	if !strings.EqualFold(src.Type, "git") {
		return nil, fmt.Errorf("unsupported source type")
	}
	if src.Repo == "" {
		return nil, fmt.Errorf("git source repository is required")
	}
	if repoURL, err := url.Parse(src.Repo); err == nil && repoURL.User != nil {
		return nil, fmt.Errorf("git source URL must not contain credentials; use source.auth instead")
	}
	if src.Commit != "" && !regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`).MatchString(src.Commit) {
		return nil, fmt.Errorf("source commit must be a hexadecimal commit ID")
	}
	if src.BindingDigest != "" && !sourceprovenance.ValidBindingDigest(src.BindingDigest) {
		return nil, fmt.Errorf("invalid source binding digest")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, err
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	username, password := src.Auth["username"], src.Auth["password"]
	if password == "" {
		password = src.Auth["token"]
		if password != "" && username == "" {
			username = "oauth2"
		}
	}
	if password != "" {
		// 凭据不进入命令行、仓库配置、收据或脚本内容
		file, err := os.CreateTemp("", "arcentra-askpass-*")
		if err != nil {
			return nil, err
		}
		name := file.Name()
		defer func() { _ = os.Remove(name) }()
		script := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' \"$ARCENTRA_GIT_USERNAME\" ;; *) printf '%s\\n' \"$ARCENTRA_GIT_PASSWORD\" ;; esac\n"
		if _, err := file.WriteString(script); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		if err := os.Chmod(name, 0o700); err != nil {
			return nil, err
		}
		env = append(env, "GIT_ASKPASS="+name, "ARCENTRA_GIT_USERNAME="+username, "ARCENTRA_GIT_PASSWORD="+password)
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = workspace, env
		cmd.WaitDelay = 2 * time.Second
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Git 输出可能包含源 URL、查询参数或凭据，不进入日志和终态收据
			return fmt.Errorf("git source operation failed: %w", err)
		}
		return nil
	}
	if pinnedCommit != "" {
		// 不解析可变分支，即使分支移动或被删除也只获取原提交
		initArgs := []string{"init", "--object-format=sha1", "."}
		if len(pinnedCommit) == 64 {
			initArgs = []string{"init", "--object-format=sha256", "."}
		}
		for _, args := range [][]string{initArgs, {"remote", "add", "--", "origin", src.Repo}, {"fetch", "--depth", "1", "origin", pinnedCommit}, {"checkout", "--detach", pinnedCommit}} {
			if err := run(args...); err != nil {
				return nil, err
			}
		}
	} else {
		args := []string{"clone", "--depth", "1"}
		if src.Branch != "" {
			args = append(args, "--branch", src.Branch)
		}
		args = append(args, "--", src.Repo, ".")
		if err := run(args...); err != nil {
			return nil, err
		}
		if src.Commit != "" {
			if err := run("fetch", "--depth", "1", "origin", src.Commit); err != nil {
				return nil, err
			}
			if err := run("checkout", "--detach", src.Commit); err != nil {
				return nil, err
			}
		}
	}

	// 旧任务没有控制面绑定，不能凭本地观察合成新的证明
	if src.BindingDigest == "" {
		return nil, nil
	}
	commit, err := resolveInitialSourceCommit(ctx, workspace, env)
	if err != nil {
		return nil, err
	}
	if pinnedCommit != "" && commit != pinnedCommit {
		return nil, fmt.Errorf("resolved source commit differs from immutable pin")
	}
	return &agentv1.SourceCheckout{BindingDigest: src.BindingDigest, ResolvedCommit: commit}, nil
}

func resolveInitialSourceCommit(ctx context.Context, workspace string, env []string) (string, error) {
	// 已成功 checkout 后，即使取消刚到达，也用有界本地只读操作保留已发生的事实
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(readCtx, "git", "rev-parse", "--verify", "HEAD^{commit}")
	cmd.Dir, cmd.Env = workspace, env
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve initial source commit: %w", err)
	}
	commit := strings.TrimSpace(string(output))
	if !sourceprovenance.ValidCommit(commit) {
		return "", fmt.Errorf("initial source commit is not a full Git object ID")
	}
	return commit, nil
}
