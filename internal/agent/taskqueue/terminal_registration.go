// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// CheckTerminalOutboxForEnrollment 只读检查新身份注册是否会遗弃已有终态日志
// 已有目录或不完整状态均阻止注册，不把身份摘要用作认证凭据，也不修改日志
func CheckTerminalOutboxForEnrollment(ctx context.Context, workspaceDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := os.Lstat(filepath.Join(workspaceDir, ".terminal-report-outbox"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return terminalIO("inspect existing journal before registration", err)
	}
	return errors.New("dynamic registration blocked by existing terminal report outbox; configure the original agent.id and grpc.token for the same controller before restarting")
}
