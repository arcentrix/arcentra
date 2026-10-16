package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

type offlineSession interface {
	executor.Executor
	Workspace() string
	Start(context.Context) error
	Seal(context.Context) error
	Close(context.Context) error
}

type offlineJobRuntime interface {
	Allocate(context.Context, *taskpayload.JobRunTaskPayload, string) (offlineSession, error)
	Acknowledge(context.Context, string, string) error
	HasJob(string) bool
	Ready() bool
}

// offlineGenerationRuntime 为显式整作业重试提供持久代次交接，旧会话不会隐式升级
type offlineGenerationRuntime interface {
	offlineJobRuntime
	AdmissionDone() <-chan struct{}
	AllocateGeneration(context.Context, *taskpayload.JobRunTaskPayload, string, taskpayload.JobGeneration) (offlineSession, error)
	RecordGenerationIntent(context.Context, taskpayload.JobGenerationTransition) error
	RecordGenerationAdmission(context.Context, taskpayload.JobGenerationTransition, taskpayload.JobGeneration) error
}

// offlineGenerationSession 仅由精确清理后的持久代次会话提供回执
type offlineGenerationSession interface {
	offlineSession
	CleanupReceipt(context.Context) (taskpayload.JobGenerationCleanupReceipt, error)
}

// offlineStepRetryRuntime 仅为同一活动代次分配不可恢复的步骤重试会话
type offlineStepRetryRuntime interface {
	offlineGenerationRuntime
	AllocateStepRetryGeneration(context.Context, *taskpayload.JobRunTaskPayload, string, taskpayload.JobGeneration) (offlineSession, error)
}

// offlineStepRetrySession 把持久步骤准入与当前进程独占的暂停能力分离
type offlineStepRetrySession interface {
	offlineSession
	StepAttemptSession(context.Context) (taskpayload.StepAttemptSession, error)
	RecordStepAttemptIntent(context.Context, taskpayload.StepAttemptTransition) error
	RecordStepAttemptAdmission(context.Context, taskpayload.StepAttemptTransition, taskpayload.StepAttemptAdmission) error
	RecordStepAttemptObservation(context.Context, taskpayload.StepAttemptTransition, taskpayload.StepAttemptAdmission) error
	RecordStepAttemptConsumed(context.Context, taskpayload.StepAttemptAdmission) error
	ExecuteStepAttempt(context.Context, *executor.ExecutionRequest, taskpayload.StepAttemptAdmission) (*executor.ExecutionResult, error)
	ExecuteArtifactStepAttempt(context.Context, *executor.ExecutionRequest, taskpayload.StepAttemptAdmission, executor.Executor) (*executor.ExecutionResult, error)
	HasPausedStepBoundary(context.Context, taskpayload.StepAttemptAdmission) error
	PausedStepBoundary(context.Context, taskpayload.StepAttemptAdmission, string) (taskpayload.PausedStepBoundary, error)
}

type offlineRuntimeContextKey struct{}
type runtimeBindingContextKey struct{}

func withOfflineJobRuntime(ctx context.Context, runtime offlineJobRuntime) context.Context {
	return context.WithValue(ctx, offlineRuntimeContextKey{}, runtime)
}
func offlineRuntimeFromContext(ctx context.Context) offlineJobRuntime {
	runtime, _ := ctx.Value(offlineRuntimeContextKey{}).(offlineJobRuntime)
	return runtime
}
func runtimeBindingFromContext(ctx context.Context) string {
	digest, _ := ctx.Value(runtimeBindingContextKey{}).(string)
	return digest
}

// validateOfflineJob 拒绝缺少整作业会话的任务，任何失败都不能降级至宿主 Shell
func validateOfflineJob(ctx context.Context, conf *config.AgentConfig, payload *taskpayload.JobRunTaskPayload) error {
	local, structural := offlineJobValidation(ctx, conf, payload)
	return errors.Join(local, structural)
}

// offlineJobValidation 区分不可认领的畸形任务与可认领但当前策略拒绝的合法任务
// 后者必须报告失败，不能确认队列后让控制面无限等待
func offlineJobValidation(ctx context.Context, conf *config.AgentConfig, payload *taskpayload.JobRunTaskPayload) (error, error) {
	sandboxMode := conf != nil && strings.EqualFold(conf.Agent.Mode, "sandbox")
	if payload.Runtime == nil {
		if payload.RetryApprovalRequired {
			return nil, fmt.Errorf("retry approval requires offline runtime")
		}
		if sandboxMode {
			return fmt.Errorf("sandbox agent requires an explicit offline runtime job"), nil
		}
		return nil, nil
	}
	request := payload.Runtime
	structuralPolicy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowFailedStepRetry: true, AllowFailedStepRetryApproval: true, AllowedImages: []string{request.Image}, MaxCPU: request.Resources.CPU, MaxMemory: request.Resources.Memory}
	if err := taskpayload.ValidateJobRuntime(payload, structuralPolicy); err != nil {
		return nil, err
	}
	if !sandboxMode || !conf.Agent.Sandbox.Enable {
		return fmt.Errorf("runtime job cannot execute on a baremetal agent"), nil
	}
	runtime := offlineRuntimeFromContext(ctx)
	if runtime == nil || !runtime.Ready() {
		return nil, &terminalDeliveryError{fmt.Errorf("offline runtime has not passed startup readiness")}
	}
	if err := taskpayload.ValidateRuntimeRequest(payload.Runtime, conf.Agent.Sandbox.Containerd.Policy); err != nil {
		return fmt.Errorf("offline runtime denied by current Agent policy"), nil
	}
	if err := taskpayload.ValidateRuntimeRetry(payload.Retry, conf.Agent.Sandbox.Containerd.Policy); err != nil {
		return fmt.Errorf("offline runtime retry denied by current Agent policy"), nil
	}
	if payload.RetryApprovalRequired && !taskpayload.AllowsRetryApproval(payload.Retry, conf.Agent.Sandbox.Containerd.Policy) {
		return fmt.Errorf("retry approval denied by current Agent policy"), nil
	}
	if payload.RetryApprovalRequired && payload.Retry != nil && payload.Retry.Mode == "failed_step" && !failedStepApprovalExecutionReady(ctx, conf, payload) {
		return fmt.Errorf("failed-step retry approval has not passed acceptance"), nil
	}
	return nil, nil
}

type offlineJobSession struct {
	offlineSession
	started, sealed bool
}

func (s *offlineJobSession) beforeStep(ctx context.Context, step taskpayload.StepPayload) error {
	if step.Uses == "builtin:artifacts" {
		if step.Action == "download" {
			if s.started || s.sealed {
				return fmt.Errorf("artifact input staging must precede container commands")
			}
			return nil
		}
		if step.Action == "upload" {
			return s.seal(ctx)
		}
		return fmt.Errorf("unsupported sandbox artifact action")
	}
	if s.sealed {
		return fmt.Errorf("sealed container cannot execute another shell step")
	}
	if !s.started {
		if err := s.Start(ctx); err != nil {
			return err
		}
		s.started = true
	}
	return nil
}
func (s *offlineJobSession) seal(ctx context.Context) error {
	if s.sealed {
		return nil
	}
	if err := s.Seal(ctx); err != nil {
		return err
	}
	s.sealed = true
	return nil
}
func (s *offlineJobSession) finish(ctx context.Context) error {
	// Close still attempts exact cleanup when sealing failed; neither failure is hidden.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	return errors.Join(s.seal(cleanup), s.Close(cleanup))
}

func deliverJobReceipt(ctx context.Context, payload *taskpayload.JobRunTaskPayload, receipt *executionReceipt) error {
	claim := receipt.jobClaim
	if claim == "" {
		claim = executionClaimID(ctx, "job", payload.JobRunID)
	}
	ctx = context.WithValue(ctx, jobExecutionClaimContextKey{}, claim)
	ctx = context.WithValue(ctx, receiptJobClaimContextKey{}, receiptJobClaim{runID: payload.JobRunID, claimID: claim})
	ctx = context.WithValue(ctx, runtimeBindingContextKey{}, receipt.runtimeBinding)
	deliveryErr := receipt.deliver(ctx, &pendingJobReports, receiptKey(ctx, "job", payload.JobRunID))
	var completed *completedExecutionError
	if receipt.runtimeCompletionRequired && (deliveryErr == nil || errors.As(deliveryErr, &completed)) {
		runtime := offlineRuntimeFromContext(ctx)
		if runtime == nil {
			pendingJobReports.Store(receiptKey(ctx, "job", payload.JobRunID), receipt)
			return &terminalDeliveryError{fmt.Errorf("runtime completion evidence unavailable")}
		}
		if err := runtime.Acknowledge(context.WithoutCancel(ctx), payload.JobRunID, claim); err != nil {
			// RPC may already be acknowledged. Retain the immutable original receipt and
			// claim until the owned-resource journal also confirms completion.
			pendingJobReports.Store(receiptKey(ctx, "job", payload.JobRunID), receipt)
			return &terminalDeliveryError{err}
		}
	}
	return deliveryErr
}
