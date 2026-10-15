// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

// ExecuteArtifactStepAttempt 在同一取消门和持久消费边界内执行计划中的可信制品步骤
// 准入期限仅约束启动窗口，已开始传输继承原作业和步骤期限；封存后仅允许原计划尾部上传
func (s *Session) ExecuteArtifactStepAttempt(ctx context.Context, req *executor.ExecutionRequest, a taskpayload.StepAttemptAdmission, artifact executor.Executor) (*executor.ExecutionResult, error) {
	result := executor.NewExecutionResult("sandbox-artifact")
	fail := func(err error) (*executor.ExecutionResult, error) { result.Complete(false, 1, err); return result, err }
	if req == nil || req.Step == nil || artifact == nil {
		return fail(fmt.Errorf("exact planned artifact executor is required"))
	}
	if err := s.acquire(ctx); err != nil {
		return fail(err)
	}
	defer s.release()
	planned, err := s.plannedStep(a.Scope)
	if err != nil {
		return fail(err)
	}
	if planned.Uses != "builtin:artifacts" || (planned.Action != "upload" && planned.Action != "download") || req.Step.Uses != planned.Uses || req.Step.Action != planned.Action || req.Step.Name != planned.Name || req.StepRunID != a.Scope.StepRunID || req.StepIndex != a.Scope.StepIndex || req.PipelineRunID != a.Scope.Identity.PipelineRunID || req.JobRunID != a.Scope.Identity.JobRunID || req.AgentID != a.Scope.Identity.AgentID || req.Workspace != s.workspace || a.Scope.Attempt != 1 {
		return fail(fmt.Errorf("artifact execution differs from immutable admission"))
	}
	if err := s.checkStepLifecycle(ctx, s.allowSealedStep(a.Scope)); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	if planned.Action == "upload" && !s.sealed || planned.Action == "download" && (s.ready || s.sealed) {
		s.mu.Unlock()
		return fail(fmt.Errorf("artifact action is not at its original input or final-output boundary"))
	}
	runCtx, cancel := context.WithCancel(ctx)
	if !s.jobDeadline.IsZero() {
		cancel()
		runCtx, cancel = context.WithDeadline(ctx, s.jobDeadline)
	}
	s.activeCancel = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.activeCancel = nil; s.mu.Unlock() }()
	if err := s.consumeStepAttempt(runCtx, a, true); err != nil {
		return fail(s.stopOwnedOnFailure(ctx, err))
	}
	if err := s.verifyConsumedEffect(runCtx, a, s.allowSealedStep(a.Scope)); err != nil {
		return fail(s.stopOwnedOnFailure(ctx, err))
	}
	if time.Now().UnixMilli() >= a.ExpiresUnixMilli {
		return fail(s.stopOwnedOnFailure(ctx, fmt.Errorf("artifact admission expired after durable consumption")))
	}
	out, err := artifact.Execute(runCtx, req)
	if err == nil {
		err = runCtx.Err()
	}
	if err != nil {
		return out, s.stopOwnedOnFailure(ctx, err)
	}
	return out, nil
}
