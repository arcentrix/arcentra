// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
)

// AllocateStepRetryGeneration 仅为原始第一代分配具有活动证明入口的持久会话
// 配额在任何运行时效果前预留，恢复日志永不重新创建此能力
func (c *Controller) AllocateStepRetryGeneration(ctx context.Context, opts Options, generation taskpayload.JobGeneration) (*Session, error) {
	if generation.Number != 1 || generation.PreviousGenerationID != "" {
		return nil, fmt.Errorf("failed-step retry requires the original generation one")
	}
	if c.runtimeIOIdentity == "" {
		return nil, fmt.Errorf("live failed-step retry requires an identity-bound private runtime IO root")
	}
	if _, ok := c.backend.(sandbox.LiveSessionBackend); !ok {
		return nil, fmt.Errorf("runtime has no live failed-step session capability")
	}
	if opts.StepRetryPlan == nil {
		return nil, fmt.Errorf("failed-step allocation requires its immutable complete plan")
	}
	encoded, err := opts.StepRetryPlan.Canonical()
	if err != nil {
		return nil, err
	}
	plan, err := taskpayload.ParseJobRetryPlan(encoded)
	if err != nil {
		return nil, err
	}
	digest, err := plan.Digest(generation.PipelineID, generation.PipelineRunID, generation.JobRunID, generation.AgentID, generation.RuntimeBindingDigest)
	if err != nil || plan.Version != 2 || plan.Policy.Mode != "failed_step" || digest != generation.RetryPlanDigest {
		return nil, fmt.Errorf("failed-step allocation plan binding mismatch")
	}
	slots := 0
	for _, step := range plan.Steps {
		slots += len(step.AttemptRunIDs)
	}
	if opts.StepAttemptSlots != 0 && opts.StepAttemptSlots != slots {
		return nil, fmt.Errorf("step attempt reservation does not match immutable plan")
	}
	opts.StepAttemptSlots = slots
	session, err := c.allocate(ctx, opts, &generation, true)
	if session != nil {
		session.stepPlan = plan
		session.jobDeadline, _ = ctx.Deadline()
	}
	return session, err
}
func (s *Session) createRuntime(ctx context.Context, r journalRecord) (string, error) {
	if !s.stepRetry {
		return s.backend.Create(ctx, s.createOptions)
	}
	backend, ok := s.backend.(sandbox.LiveSessionBackend)
	if !ok {
		return "", fmt.Errorf("runtime live session capability disappeared")
	}
	live, err := backend.CreateLiveSession(ctx, s.createOptions, sandbox.LiveSessionIdentity{SessionID: r.StepSessionNonce, ExecutionClaimID: r.ExecutionClaimID})
	if live != nil {
		// 保留句柄仅供精确清理；身份不匹配不能安装到可执行会话
		if live.Resource() != r.Resource || live.Identity() != (sandbox.LiveSessionIdentity{SessionID: r.StepSessionNonce, ExecutionClaimID: r.ExecutionClaimID}) {
			return live.Resource().ID, fmt.Errorf("runtime live session changed its immutable identity")
		}
		s.live = live
	}
	if err != nil {
		return r.Resource.ID, err
	}
	if live == nil {
		return r.Resource.ID, fmt.Errorf("runtime returned no live session")
	}
	return r.Resource.ID, nil
}
func (s *Session) startRuntime(ctx context.Context, id string) error {
	if !s.stepRetry {
		return s.backend.Start(ctx, id)
	}
	if s.live == nil {
		return fmt.Errorf("live session was not created in this process")
	}
	return s.live.Start(ctx)
}
func (s *Session) executeLiveStep(ctx context.Context, a taskpayload.StepAttemptAdmission, command []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
	if s.live == nil {
		return nil, fmt.Errorf("original live session is unavailable")
	}
	if err := s.verifyConsumedEffect(ctx, a, false); err != nil {
		return nil, err
	}
	nowMillis := time.Now().UnixMilli()
	if nowMillis < a.NotBeforeUnixMilli || nowMillis >= a.ExpiresUnixMilli || !s.jobDeadline.IsZero() && !time.Now().Before(s.jobDeadline) {
		return nil, fmt.Errorf("admission expired before runtime effect")
	}
	step := sandbox.StepIdentity{NotBeforeUnixMilli: a.NotBeforeUnixMilli, ExpiresUnixMilli: a.ExpiresUnixMilli, StepRunID: a.Scope.StepRunID, StepIndex: a.Scope.StepIndex, Attempt: int32(a.Scope.Attempt), AdmissionDigest: a.BindingDigest}
	var out *sandbox.ExecuteResult
	var boundary *sandbox.FailedStepBoundary
	var err error
	if a.Scope.Attempt > 1 {
		if s.failedBoundary == nil || s.failedAdmission == nil {
			return nil, fmt.Errorf("retry has no original opaque failed boundary")
		}
		step.PreviousStepRunID = s.failedAdmission.Scope.StepRunID
		original := s.failedBoundary
		// 即使 Resume 的响应丢失，也不能重用任何旧句柄
		s.failedBoundary = nil
		s.failedAdmission = nil
		s.boundaryAudit = nil
		out, boundary, err = s.live.ResumeStep(ctx, original, step, command, opts)
	} else {
		if s.failedBoundary != nil {
			return nil, fmt.Errorf("new logical command cannot bypass a paused failed boundary")
		}
		out, boundary, err = s.live.ExecuteStep(ctx, step, command, opts)
	}
	if err != nil {
		return out, err
	}
	if out == nil {
		return nil, fmt.Errorf("live runtime returned no execution result")
	}
	if out.ExitCode != 0 {
		if boundary == nil {
			return out, fmt.Errorf("failed command has no fresh paused boundary")
		}
		if err := s.live.CheckBoundary(ctx, boundary); err != nil {
			return out, err
		}
		if s.boundarySequence == ^uint64(0) {
			return out, fmt.Errorf("live boundary sequence exhausted")
		}
		s.boundarySequence++
		now := time.Now()
		expires := now.Add(taskpayload.PausedStepBoundaryLifetime)
		if !s.jobDeadline.IsZero() && s.jobDeadline.Before(expires) {
			expires = s.jobDeadline
		}
		copy := a
		s.failedAdmission = &copy
		s.failedBoundary = boundary
		s.boundaryAudit = &taskpayload.PausedStepBoundary{Version: 1, Scope: a.Scope, BoundaryNonce: uuid.NewString(), BoundarySequence: s.boundarySequence, FailedAdmissionID: a.AdmissionID, FailedAdmissionBindingDigest: a.BindingDigest, CapturedUnixMilli: now.UnixMilli(), ExpiresUnixMilli: expires.UnixMilli(), FailedExecExitObserved: true, ExecReaped: true, TaskPaused: true, ExactLiveInitOnly: true, Childless: true}
	} else if boundary != nil {
		return out, fmt.Errorf("successful execution returned an unexpected failed boundary")
	}
	return out, nil
}

func (s *Session) checkPausedBoundary(ctx context.Context, a taskpayload.StepAttemptAdmission) error {
	if err := s.checkStepLifecycle(ctx, false); err != nil {
		return err
	}
	if s.live == nil || s.failedBoundary == nil || s.failedAdmission == nil || *s.failedAdmission != a || s.boundaryAudit == nil {
		return fmt.Errorf("original failed step live boundary is unavailable")
	}
	if time.Now().UnixMilli() >= s.boundaryAudit.ExpiresUnixMilli {
		return fmt.Errorf("original failed step boundary expired")
	}
	r, err := s.durableRecord(ctx)
	if err != nil {
		return err
	}
	if err := validateStepScope(r, a.Scope); err != nil {
		return err
	}
	if err := s.verifyWorkspace(); err != nil {
		return err
	}
	if _, err := s.controller.verifyRecordWorkspace(r, false); err != nil {
		return err
	}
	if s.live.Resource() != r.Resource || s.live.Identity() != (sandbox.LiveSessionIdentity{SessionID: r.StepSessionNonce, ExecutionClaimID: r.ExecutionClaimID}) {
		return fmt.Errorf("live resource identity changed")
	}
	return s.live.CheckBoundary(ctx, s.failedBoundary)
}

// HasPausedStepBoundary 在失败终态标记可重试前重新验证原活动暂停边界
func (s *Session) HasPausedStepBoundary(ctx context.Context, a taskpayload.StepAttemptAdmission) error {
	if !s.stepRetry || s.controller == nil {
		return fmt.Errorf("session does not support live step attempts")
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := s.checkPausedBoundary(ctx, a); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	return nil
}

// PausedStepBoundary 将准确失败终态回执绑定到原活动观察，不能从审计内容恢复证明
func (s *Session) PausedStepBoundary(ctx context.Context, a taskpayload.StepAttemptAdmission, receiptDigest string) (taskpayload.PausedStepBoundary, error) {
	if !s.stepRetry || s.controller == nil {
		return taskpayload.PausedStepBoundary{}, fmt.Errorf("session does not support live step attempts")
	}
	if err := s.acquire(ctx); err != nil {
		return taskpayload.PausedStepBoundary{}, err
	}
	defer s.release()
	if err := s.checkPausedBoundary(ctx, a); err != nil {
		return taskpayload.PausedStepBoundary{}, s.stopOwnedOnFailure(ctx, err)
	}
	audit := *s.boundaryAudit
	if audit.FailedStepReceiptDigest != "" && audit.FailedStepReceiptDigest != receiptDigest {
		return taskpayload.PausedStepBoundary{}, fmt.Errorf("failed boundary receipt cannot change")
	}
	audit.FailedStepReceiptDigest = receiptDigest
	if err := audit.Validate(); err != nil {
		return taskpayload.PausedStepBoundary{}, err
	}
	s.boundaryAudit = &audit
	return audit, nil
}

func (s *Session) plannedStep(scope taskpayload.StepAttemptIdentity) (taskpayload.RetryStepPlan, error) {
	if s.stepPlan == nil {
		return taskpayload.RetryStepPlan{}, fmt.Errorf("original immutable step plan is unavailable")
	}
	for _, step := range s.stepPlan.Steps {
		if step.StepIndex != scope.StepIndex {
			continue
		}
		if scope.Attempt < 1 || scope.Attempt > len(step.AttemptRunIDs) || step.AttemptRunIDs[scope.Attempt-1] != scope.StepRunID || step.AttemptRunIDs[0] != scope.LogicalStepRunID || step.Name != scope.StepName || step.DefinitionDigest != scope.DefinitionDigest {
			return taskpayload.RetryStepPlan{}, fmt.Errorf("step admission differs from original immutable plan")
		}
		return step, nil
	}
	return taskpayload.RetryStepPlan{}, fmt.Errorf("admitted logical step was not in original plan")
}
func (s *Session) allowSealedStep(scope taskpayload.StepAttemptIdentity) bool {
	step, err := s.plannedStep(scope)
	return err == nil && strings.TrimSpace(strings.ToLower(step.Uses)) == "builtin:artifacts" && step.Action == "upload" && scope.Attempt == 1
}

// BeginStepRetryDrain 原子关闭新准入并取消原活动命令，运行时在每个效果门重新检查同一上下文
func (c *Controller) BeginStepRetryDrain() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.draining = true
	for _, s := range c.sessions {
		if !s.stepRetry {
			continue
		}
		s.mu.Lock()
		s.closing = true
		s.draining = true
		if s.activeCancel != nil {
			s.activeCancel()
		}
		s.mu.Unlock()
	}
}
