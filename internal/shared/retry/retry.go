// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

// Package retry 为控制面和 Agent 提供相同的显式、有限重试策略
package retry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
)

// MaxAttempts 限制每个步骤的预分配执行次数（包括首次执行）
const MaxAttempts = 5

// Validate 拒绝隐式重试范围和不安全的退出码
func Validate(policy *pipelinev1.Retry) error {
	if policy == nil {
		return nil
	}
	if policy.Mode != "failed_step" && policy.Mode != "whole_job" {
		return fmt.Errorf("retry mode must explicitly be failed_step or whole_job")
	}
	if policy.MaxAttempts < 2 || policy.MaxAttempts > MaxAttempts {
		return fmt.Errorf("retry maxAttempts must be between 2 and %d (including the first attempt)", MaxAttempts)
	}
	if !policy.Idempotent {
		return fmt.Errorf("retry requires explicit idempotent=true for repeated commands and retained workspace")
	}
	if len(policy.AllowedExitCodes) == 0 {
		return fmt.Errorf("retry requires allowedExitCodes")
	}
	seen := map[int32]bool{}
	for _, code := range policy.AllowedExitCodes {
		if code < 1 || code > 125 {
			return fmt.Errorf("retry exit codes must be between 1 and 125; command/permission errors and signals cannot be retried")
		}
		if seen[code] {
			return fmt.Errorf("duplicate retry exit code %d", code)
		}
		seen[code] = true
	}
	if policy.Delay != "" {
		delay, err := time.ParseDuration(policy.Delay)
		if err != nil || delay < 0 || delay > time.Hour {
			return fmt.Errorf("retry delay must be a duration between 0s and 1h")
		}
	}
	return nil
}

// Attempts 返回包含首次执行的预留次数
func Attempts(policy *pipelinev1.Retry) int {
	if policy == nil {
		return 1
	}
	return int(policy.MaxAttempts)
}

// Eligible 仅允许已知 Shell 进程退出，不重试取消、超时、日志、权限或配置错误
func Eligible(ctx context.Context, policy *pipelinev1.Retry, uses string, err error) bool {
	if policy == nil || Validate(policy) != nil || err == nil || ctx.Err() != nil {
		return false
	}
	if uses != "shell" && uses != "builtin:shell" && uses != "" {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrPermission) {
		return false
	}
	var blocked *blockedError
	if errors.As(err, &blocked) {
		return false
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	for _, code := range policy.AllowedExitCodes {
		if int(code) == exit.ExitCode() {
			return true
		}
	}
	return false
}

// Step 记录执行策略所需的逻辑步骤信息
type Step struct {
	Uses            string
	ContinueOnError bool
}

// Run 执行同一活动作业的显式重试，回调必须先持久化本次终态再返回错误
// attempt 从 1 开始；未调用的预留次数必须由调用者记录为 skipped
func Run(ctx context.Context, policy *pipelinev1.Retry, steps []Step, execute func(index, attempt int) error) error {
	if err := Validate(policy); err != nil {
		return err
	}
	attempts := Attempts(policy)
	if policy != nil && policy.Mode == "whole_job" {
		for attempt := 1; attempt <= attempts; attempt++ {
			retryRound := false
			for index, step := range steps {
				if err := ctx.Err(); err != nil {
					return err
				}
				err := execute(index, attempt)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err == nil || step.ContinueOnError && ctx.Err() == nil && !IsBlocked(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					continue
				}
				if attempt == attempts || !Eligible(ctx, policy, strings.TrimSpace(step.Uses), err) {
					return err
				}
				if err := wait(ctx, policy); err != nil {
					return err
				}
				retryRound = true
				break
			}
			if !retryRound {
				return nil
			}
		}
		return nil
	}
	for index, step := range steps {
		for attempt := 1; attempt <= attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := execute(index, attempt)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil || step.ContinueOnError && ctx.Err() == nil && !IsBlocked(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				break
			}
			if attempt == attempts || !Eligible(ctx, policy, strings.TrimSpace(step.Uses), err) {
				return err
			}
			if err := wait(ctx, policy); err != nil {
				return err
			}
		}
	}
	return nil
}

func wait(ctx context.Context, policy *pipelinev1.Retry) error {
	delay, _ := time.ParseDuration(policy.Delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type blockedError struct{ cause error }

func (e *blockedError) Error() string { return e.cause.Error() }
func (e *blockedError) Unwrap() error { return e.cause }

// Block 标记必须停止的日志、持久化或上报故障，保留原始错误链
func Block(err error) error {
	if err == nil {
		return nil
	}
	return &blockedError{cause: err}
}

// IsBlocked 判断故障是否必须停止而不能由 continueOnError 忽略
func IsBlocked(err error) bool { var blocked *blockedError; return errors.As(err, &blocked) }
