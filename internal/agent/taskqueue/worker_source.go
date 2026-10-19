// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

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
	git, err := newSourceGit(src, workspace)
	if err != nil {
		return nil, err
	}
	defer git.close()
	run := func(args ...string) error { _, err := git.run(ctx, args...); return err }
	if pinnedCommit != "" {
		format := "sha1"
		if len(pinnedCommit) == 64 {
			format = "sha256"
		}
		for _, args := range [][]string{{"init", "--object-format=" + format, "."}, {"remote", "add", "--", "origin", src.Repo}, {"fetch", "--no-recurse-submodules", "--depth", "1", "origin", pinnedCommit}} {
			if err := run(args...); err != nil {
				return nil, err
			}
		}
	} else {
		args := []string{"clone", "--no-checkout", "--no-local", "--no-recurse-submodules"}
		if git.protocol != "file" {
			args = append(args, "--depth", "1")
		}
		if src.Branch != "" {
			args = append(args, "--branch", src.Branch)
		}
		args = append(args, "--", src.Repo, ".")
		if err := run(args...); err != nil {
			return nil, err
		}
		if src.Commit != "" {
			if err := run("fetch", "--no-recurse-submodules", "--depth", "1", "origin", src.Commit); err != nil {
				return nil, err
			}
		}
	}
	target := pinnedCommit
	if target == "" {
		target = src.Commit
	}
	if target == "" {
		target = "HEAD"
	}
	// Resolve once, inspect objects without checkout, then materialize that exact tree.
	resolved, err := git.run(ctx, "rev-parse", "--verify", target+"^{commit}")
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(resolved))
	if !sourceprovenance.ValidCommit(commit) {
		return nil, fmt.Errorf("initial source commit is not a full Git object ID")
	}
	if err := git.validateTree(ctx, commit); err != nil {
		return nil, err
	}
	if err := run("checkout", "--detach", commit); err != nil {
		return nil, err
	}

	// 旧任务没有控制面绑定，不能凭本地观察合成新的证明
	if src.BindingDigest == "" {
		return nil, nil
	}
	commit, err = git.resolveCommit(ctx)
	if err != nil {
		return nil, err
	}
	if pinnedCommit != "" && commit != pinnedCommit {
		return nil, fmt.Errorf("resolved source commit differs from immutable pin")
	}
	return &agentv1.SourceCheckout{BindingDigest: src.BindingDigest, ResolvedCommit: commit}, nil
}
