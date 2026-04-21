// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package executor

import (
	"context"
	"os/exec"
	"strings"
)

const nameShell = "shell"

// ShellExecutor 执行 run/script/command 类步骤的 shell 执行器。
type ShellExecutor struct{}

// NewShellExecutor 创建 ShellExecutor。
func NewShellExecutor() *ShellExecutor {
	return &ShellExecutor{}
}

// Name 返回执行器名称。
func (e *ShellExecutor) Name() string {
	return nameShell
}

// CanExecute 检查步骤是否包含可执行命令（Args 中含 run/script/command/commands）。
func (e *ShellExecutor) CanExecute(req *ExecutionRequest) bool {
	if req == nil || req.Step == nil || req.Step.Args == nil {
		return false
	}
	return strings.TrimSpace(BuildCommandFromStepArgs(req.Step)) != ""
}

// Execute 使用 sh -lc 执行步骤命令。
func (e *ShellExecutor) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	result := NewExecutionResult(e.Name())
	if req == nil || req.Step == nil {
		result.Complete(false, 1, nil)
		result.Error = "nil request or step"
		return result, nil
	}
	cmdText := BuildCommandFromStepArgs(req.Step)
	if strings.TrimSpace(cmdText) == "" {
		result.Complete(false, 1, nil)
		result.Error = "empty command"
		return result, nil
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Options != nil && req.Options.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Options.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "sh", "-lc", cmdText)
	if strings.TrimSpace(req.Workspace) != "" {
		cmd.Dir = req.Workspace
	}
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	output, err := cmd.CombinedOutput()
	outStr := string(output)
	if err != nil {
		result.Complete(false, 1, err)
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = int32(exitErr.ExitCode())
		}
		result.Output = outStr
		result.ErrorOutput = outStr
		if strings.TrimSpace(outStr) == "" {
			result.Error = err.Error()
		} else {
			result.Error = strings.TrimSpace(outStr)
		}
		return result, err
	}
	result.Complete(true, 0, nil)
	result.Output = outStr
	return result, nil
}
