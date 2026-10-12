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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const nameShell = "shell"
const maxCapturedOutput = 4 * 1024 * 1024

// ShellExecutor 执行 run、script、command 或 commands 步骤
type ShellExecutor struct{}

// NewShellExecutor 创建 Shell 执行器
func NewShellExecutor() *ShellExecutor { return &ShellExecutor{} }

// Name 返回执行器名称
func (e *ShellExecutor) Name() string { return nameShell }

// CanExecute 判断步骤是否包含本地 Shell 命令
func (e *ShellExecutor) CanExecute(req *ExecutionRequest) bool {
	return req != nil && req.Step != nil && !req.Step.RunRemotely && strings.TrimSpace(BuildCommandFromStepArgs(req.Step)) != ""
}

// Execute 执行 Shell 命令，继承环境并在取消时结束子进程
func (e *ShellExecutor) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	result := NewExecutionResult(e.Name())
	if req == nil || req.Step == nil {
		err := fmt.Errorf("nil request or step")
		result.Complete(false, 1, err)
		return result, err
	}
	cmdText := BuildCommandFromStepArgs(req.Step)
	if strings.TrimSpace(cmdText) == "" {
		err := fmt.Errorf("empty command")
		result.Complete(false, 1, err)
		return result, err
	}
	runCtx := ctx
	if req.Options != nil && req.Options.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Options.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "sh", "-ec", cmdText)
	cmd.Dir = req.Workspace
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureProcessCancellation(cmd)
	cmd.WaitDelay = 2 * time.Second
	stdout, stderr := &boundedOutput{}, &boundedOutput{}
	if req.OutputHandler != nil {
		stdout.onWrite = func(p []byte) { req.OutputHandler("stdout", string(p)) }
		stderr.onWrite = func(p []byte) { req.OutputHandler("stderr", string(p)) }
		result.WithMetadata("logsStreamed", true)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	result.Output, result.ErrorOutput = stdout.String(), stderr.String()
	result.WithMetadata("outputBytes", strconv.FormatInt(stdout.total+stderr.total, 10))
	if stdout.total > maxCapturedOutput || stderr.total > maxCapturedOutput {
		result.WithMetadata("outputTruncated", true)
	}
	if runCtx.Err() != nil {
		err = runCtx.Err()
	}
	if err != nil {
		exitCode := int32(1)
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		}
		result.Complete(false, exitCode, err)
		return result, err
	}
	result.Complete(true, 0, nil)
	return result, nil
}

type boundedOutput struct {
	buffer  bytes.Buffer
	total   int64
	onWrite func([]byte)
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.onWrite != nil {
		b.onWrite(p)
	}
	b.total += int64(n)
	if remaining := maxCapturedOutput - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}

func (b *boundedOutput) Len() int       { return b.buffer.Len() }
func (b *boundedOutput) String() string { return b.buffer.String() }
