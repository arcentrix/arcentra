// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/sandbox"
)

var _ executor.Executor = (*Session)(nil)

// Name 返回会话执行器名称
func (s *Session) Name() string { return "sandbox-shell" }

// CanExecute 仅接受显式 Shell 步骤，不接受 HTTP 或本地原生插件
func (s *Session) CanExecute(req *executor.ExecutionRequest) bool {
	if req == nil || req.Step == nil || req.Step.RunRemotely {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(req.Step.Uses)) {
	case "shell", "builtin:shell":
	default:
		return false
	}
	if req.Step.Action != "" && req.Step.Action != "run" {
		return false
	}
	return strings.TrimSpace(executor.BuildCommandFromStepArgs(req.Step)) != ""
}

// Execute 把单步映射到容器 /workspace，复用 Manager 的日志与事件链路
// 错误或取消后会话关闭执行入口，后续必须 Close；普通非零退出仍可继续
func (s *Session) Execute(ctx context.Context, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	result := executor.NewExecutionResult(s.Name())
	fail := func(err error) (*executor.ExecutionResult, error) { result.Complete(false, 1, err); return result, err }
	if !s.CanExecute(req) {
		return fail(fmt.Errorf("sandbox session accepts explicit shell/run steps only"))
	}
	if req.Workspace != "" && req.Workspace != s.workspace {
		return fail(fmt.Errorf("sandbox request cannot override its allocated workspace"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := s.acquire(ctx); err != nil {
		return fail(err)
	}
	defer s.release()
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	if s.closing || !s.ready {
		s.mu.Unlock()
		return fail(fmt.Errorf("sandbox session is not executable; Close is required"))
	}
	runCtx, cancel := context.WithCancel(ctx)
	if req.Options != nil && req.Options.Timeout > 0 {
		cancel()
		runCtx, cancel = context.WithTimeout(ctx, req.Options.Timeout)
	}
	s.activeCancel = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.activeCancel = nil; s.mu.Unlock() }()
	if err := s.verifyWorkspace(); err != nil {
		if s.controller != nil {
			err = s.stopOwnedOnFailure(ctx, err)
		}
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		return fail(err)
	}
	if s.controller != nil {
		record, err := s.durableRecord(runCtx)
		if err != nil {
			return fail(s.stopOwnedOnFailure(ctx, err))
		}
		if record.Phase != phaseRunning && record.Phase != phaseExecuting {
			return fail(fmt.Errorf("sandbox journal is not executable"))
		}
		if _, err := s.controller.verifyRecordWorkspace(record, false); err != nil {
			return fail(s.stopOwnedOnFailure(ctx, err))
		}
		if err := s.advance(runCtx, phaseExecuting); err != nil {
			return fail(s.stopOwnedOnFailure(ctx, err))
		}
	}
	opts := &sandbox.ExecuteOptions{WorkingDir: containerWorkspace, Env: maps.Clone(req.Env), User: s.user}
	if req.Options != nil {
		opts.Timeout = req.Options.Timeout
	}
	var outputMu sync.Mutex
	var bytes int64
	write := func(stream string) outputWriter {
		return func(p []byte) (int, error) {
			outputMu.Lock()
			defer outputMu.Unlock()
			bytes += int64(len(p))
			if req.OutputHandler != nil {
				req.OutputHandler(stream, string(p))
			}
			return len(p), nil
		}
	}
	opts.Stdout, opts.Stderr = write("stdout"), write("stderr")
	out, err := s.backend.Execute(runCtx, s.containerID, []string{"sh", "-ec", executor.BuildCommandFromStepArgs(req.Step)}, opts)
	if err == nil && runCtx.Err() != nil {
		err = runCtx.Err()
	}
	if err == nil && out == nil {
		err = fmt.Errorf("sandbox backend returned no execution result")
	}
	if err != nil {
		if s.controller != nil {
			err = s.stopOwnedOnFailure(ctx, err)
		}
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
	}
	if out != nil {
		result.Output, result.ErrorOutput = out.Stdout, out.Stderr
		result.WithMetadata("outputTruncated", out.OutputTruncated)
		result.Complete(err == nil && out.ExitCode == 0, out.ExitCode, err)
		if err == nil && out.ExitCode != 0 {
			err = fmt.Errorf("sandbox command exited with code %d", out.ExitCode)
			result.Error = err.Error()
		}
	} else {
		result.Complete(false, 1, err)
	}
	result.WithMetadata("outputBytes", bytes)
	if req.OutputHandler != nil {
		result.WithMetadata("logsStreamed", true)
	}
	return result, err
}

type outputWriter func([]byte) (int, error)

func (w outputWriter) Write(p []byte) (int, error) { return w(p) }
