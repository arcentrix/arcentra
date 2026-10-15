// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sync"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type stepAttemptContextKey struct{}

// 每个执行上下文固定单个准入，不共享可变的“当前尝试”指针
// 只有精确终态 ACK 后记录回执；队列或 outbox 重放没有执行入口
// admission 和 session 不得在创建该上下文后替换
// receipt 仅保存当前尝试，完整终态仍由既有有界 outbox 管理
// 此类型不序列化活动暂停能力
type stepAttemptExecution struct {
	admission taskpayload.StepAttemptAdmission
	session   offlineStepRetrySession
	mu        sync.Mutex
	receipt   *generationReceipt
}

func stepAttemptFromContext(ctx context.Context) *stepAttemptExecution {
	value, _ := ctx.Value(stepAttemptContextKey{}).(*stepAttemptExecution)
	return value
}

func observeStepAttemptReceipt(ctx context.Context, record terminalRecord, request *agentv1.ReportStepRunStatusRequest, digest string) error {
	attempt := stepAttemptFromContext(ctx)
	if attempt == nil {
		return nil
	}
	a := attempt.admission
	if record.RunID != a.Scope.StepRunID || request.StepRunId != a.Scope.StepRunID || record.ClaimID != a.Scope.StepClaimID || request.AgentId != a.Scope.Identity.AgentID || request.GenerationId != a.Scope.GenerationID || request.GenerationBindingDigest != a.Scope.GenerationBindingDigest || request.StepAttemptAdmissionDigest != a.BindingDigest {
		return fmt.Errorf("terminal receipt does not match its exact step admission")
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.receipt != nil && attempt.receipt.digest != digest {
		return fmt.Errorf("admitted step terminal receipt changed")
	}
	attempt.receipt = &generationReceipt{request: proto.Clone(request).(*agentv1.ReportStepRunStatusRequest), digest: digest}
	return nil
}

func (a *stepAttemptExecution) acknowledgedFailure() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.receipt == nil || a.receipt.request.Status != steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED || !a.receipt.request.RetryableShellExit {
		return "", fmt.Errorf("failed-step advance requires the exact acknowledged pure failure")
	}
	return a.receipt.digest, nil
}

func (a *stepAttemptExecution) hasAcknowledgedReceipt() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.receipt != nil
}

// stepAttemptExecutor 保留 Manager 的日志链路，在持久消费后才进入绑定执行器
// 两类效果的消费和取消均由会话内部完成；制品不具备暂停或重试能力
// 两条路径都只持有一个不可变准入
type stepAttemptExecutor struct {
	session   offlineStepRetrySession
	admission taskpayload.StepAttemptAdmission
	artifact  executor.Executor
	drain     <-chan struct{}
	invoked   bool
}

func (e *stepAttemptExecutor) Name() string {
	if e.artifact != nil {
		return e.artifact.Name()
	}
	return e.session.Name()
}
func (e *stepAttemptExecutor) CanExecute(req *executor.ExecutionRequest) bool {
	if req == nil || req.StepRunID != e.admission.Scope.StepRunID || req.StepIndex != e.admission.Scope.StepIndex {
		return false
	}
	if e.artifact != nil {
		return e.artifact.CanExecute(req)
	}
	return e.session.CanExecute(req)
}
func (e *stepAttemptExecutor) Execute(ctx context.Context, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	e.invoked = true
	if !e.CanExecute(req) {
		return nil, fmt.Errorf("executor request differs from admitted step")
	}
	if err := checkStepAdmissionTime(ctx, e.admission); err != nil {
		return nil, err
	}
	if err := waitStepRetry(ctx, e.drain, 0); err != nil {
		return nil, err
	}
	if e.artifact != nil {
		return e.session.ExecuteArtifactStepAttempt(ctx, req, e.admission, e.artifact)
	}
	return e.session.ExecuteStepAttempt(ctx, req, e.admission)
}

func checkStepAdmissionTime(ctx context.Context, admission taskpayload.StepAttemptAdmission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if now < admission.NotBeforeUnixMilli {
		return fmt.Errorf("step admission delay has not elapsed")
	}
	if now >= admission.ExpiresUnixMilli {
		return fmt.Errorf("step admission expired; continuation refused")
	}
	return nil
}

func waitStepRetry(ctx context.Context, drain <-chan struct{}, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-drain:
		return fmt.Errorf("Agent draining; failed-step continuation refused")
	default:
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-drain:
		return fmt.Errorf("Agent draining; failed-step continuation refused")
	case <-timer.C:
		return nil
	}
}

// callStepAttemptTransition 对丢失的 ACK 只重发同一规范字节，不创造新意图或能力
func callStepAttemptTransition(ctx context.Context, client *agentgrpc.ClientWrapper, drain <-chan struct{}, transition taskpayload.StepAttemptTransition) (taskpayload.StepAttemptResult, error) {
	var empty taskpayload.StepAttemptResult
	encoded, err := transition.Canonical()
	if err != nil {
		return empty, err
	}
	if client == nil || client.AgentClient == nil || !client.ControllerAuthenticated() {
		return empty, fmt.Errorf("step admission requires an authenticated controller transport")
	}
	delay := 100 * time.Millisecond
	for {
		if err := waitStepRetry(ctx, drain, 0); err != nil {
			return empty, err
		}
		if transition.Boundary != nil && time.Now().UnixMilli() >= transition.Boundary.ExpiresUnixMilli {
			return empty, fmt.Errorf("paused boundary expired while awaiting admission")
		}
		rpcCtx, cancel := client.WithTimeoutAndAuth(ctx)
		response, callErr := client.AgentClient.ReconcileStepAttempt(withExecutionClaim(rpcCtx, "job", transition.Scope.Identity.JobRunID), &agentv1.ReconcileStepAttemptRequest{AgentId: transition.Scope.Identity.AgentID, TransitionJson: encoded})
		cancel()
		if callErr == nil {
			if response == nil {
				return empty, fmt.Errorf("controller returned no step admission")
			}
			admission, err := taskpayload.ParseStepAttemptAdmission(response.AdmissionJson)
			if err != nil {
				return empty, err
			}
			if err := admission.ValidateTransition(transition); err != nil {
				return empty, err
			}
			return taskpayload.StepAttemptResult{Admission: admission, ExecutionAllowed: response.ExecutionAllowed}, nil
		}
		switch grpcstatus.Code(callErr) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		default:
			return empty, callErr
		}
		if err := waitStepRetry(ctx, drain, delay); err != nil {
			return empty, errors.Join(callErr, err)
		}
		delay = min(delay*2, 5*time.Second)
	}
}

// acknowledgeStepAttempt 只交付已经冻结的终态，绝不再次调用执行器
func acknowledgeStepAttempt(ctx context.Context, drain <-chan struct{}, stepID string, executionErr error) error {
	delay := 100 * time.Millisecond
	for {
		pending, exists := pendingStepReports.Load(receiptKey(ctx, "step", stepID))
		if !exists {
			return executionErr
		}
		if terminalPersistenceFailed(executionErr) || terminalQuarantined(executionErr) {
			return executionErr
		}
		var delivery *terminalDeliveryError
		if !errors.As(executionErr, &delivery) {
			return executionErr
		}
		switch grpcstatus.Code(executionErr) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		default:
			return executionErr
		}
		if err := waitStepRetry(ctx, drain, delay); err != nil {
			return errors.Join(executionErr, err)
		}
		executionErr = pending.(*executionReceipt).deliver(ctx, &pendingStepReports, receiptKey(ctx, "step", stepID))
		if terminalPersistenceFailed(executionErr) || terminalQuarantined(executionErr) {
			return executionErr
		}
		if _, exists := pendingStepReports.Load(receiptKey(ctx, "step", stepID)); !exists {
			return executionErr
		}
		delivery = nil
		if !errors.As(executionErr, &delivery) {
			return executionErr
		}
		switch grpcstatus.Code(executionErr) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		default:
			return executionErr
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func stepAttemptScope(identity taskpayload.JobInputIdentity, generation taskpayload.JobGeneration, inputDigest string, session taskpayload.StepAttemptSession, plan taskpayload.RetryStepPlan, attempt int) taskpayload.StepAttemptIdentity {
	stepID := plan.AttemptRunIDs[attempt-1]
	return taskpayload.StepAttemptIdentity{Identity: identity, GenerationID: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest, RuntimeClaimID: generation.RuntimeClaimID, InputManifestDigest: inputDigest, RetryPlanDigest: generation.RetryPlanDigest, Session: session, LogicalStepRunID: plan.AttemptRunIDs[0], StepRunID: stepID, StepName: plan.Name, StepIndex: plan.StepIndex, DefinitionDigest: plan.DefinitionDigest, Attempt: attempt, StepClaimID: agentidentity.StepClaimID(identity.ClaimID, stepID)}
}

// executeContainerFailedSteps 仅在原活动会话内推进失败步骤，永不进入 whole_job 循环
// 成功步骤和失败步骤的文件变更都保留，源和作业输入只准备一次
func executeContainerFailedSteps(ctx, jobCtx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager, payload *taskpayload.JobRunTaskPayload, store storage.IStorage, reference taskpayload.ArtifactReference, start time.Time) (result generationJobResult) {
	result = generationJobResult{ctx: ctx, artifacts: &artifactExecutor{job: payload, storage: store, reference: reference, uploaded: map[string]string{}}}
	visited := make(map[string]bool)
	defer func() {
		if result.session != nil {
			result.err = errors.Join(result.err, result.session.finish(result.ctx))
		}
		if result.uncertain {
			return
		}
		for _, step := range payload.Steps {
			for _, id := range stepAttemptIDs(step) {
				if visited[id] {
					continue
				}
				status, reason := steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED, "attempt not admitted"
				if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
					status, reason = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT, "job deadline exceeded"
				} else if errors.Is(jobCtx.Err(), context.Canceled) {
					status, reason = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED, "job cancelled"
				}
				end := time.Now().Unix()
				result.err = errors.Join(result.err, retainStepReport(result.ctx, id, nil, func(reportCtx context.Context) error {
					return reportStepRunStatus(reportCtx, client, conf, id, status, 0, reason, start, end, nil)
				}))
			}
		}
	}()
	runtime, ok := offlineRuntimeFromContext(ctx).(offlineStepRetryRuntime)
	if !ok {
		result.err = fmt.Errorf("runtime does not support durable live failed-step retry")
		return result
	}
	if client == nil || !client.ControllerAuthenticated() {
		result.err = fmt.Errorf("failed-step continuation requires an authenticated controller transport")
		return result
	}
	if manager == nil {
		result.err = fmt.Errorf("failed-step session requires managed execution")
		return result
	}
	if err := taskpayload.ValidateJobRetryPlan(payload); err != nil {
		result.err = err
		return result
	}
	if payload.Retry == nil || payload.Retry.Mode != "failed_step" {
		result.err = fmt.Errorf("failed-step mode required")
		return result
	}
	for _, step := range payload.Steps {
		if step.ContinueOnError {
			result.err = fmt.Errorf("failed-step initial profile does not support continueOnError")
			return result
		}
	}
	if _, exists := conf.Agent.EnvVars[taskpayload.RuntimeGenerationEnv]; exists {
		result.err = fmt.Errorf("Agent environment overrides reserved generation variable")
		return result
	}
	if err := waitStepRetry(jobCtx, runtime.AdmissionDone(), 0); err != nil {
		result.err = err
		return result
	}
	generationRequest := taskpayload.JobGenerationTransition{Version: 1, Identity: generationInputIdentity(ctx, payload), RetryPlanDigest: payload.RetryPlanDigest, Operation: "admit"}
	var err error
	generationRequest.RequestDigest, err = generationRequest.Digest()
	if err != nil {
		result.err = err
		return result
	}
	if err = runtime.RecordGenerationIntent(jobCtx, generationRequest); err != nil {
		result.err = err
		return result
	}
	result.journalRequired = true
	generation, err := callGenerationTransition(jobCtx, client, generationRequest)
	if err != nil {
		result.err = &terminalDeliveryError{err}
		result.uncertain = true
		return result
	}
	if generation.Number != 1 || generation.PreviousGenerationID != "" {
		result.err = &terminalDeliveryError{fmt.Errorf("failed-step retry cannot advance job generation")}
		result.uncertain = true
		return result
	}
	admissionCtx, admissionCancel := context.WithTimeout(context.WithoutCancel(jobCtx), 30*time.Second)
	err = runtime.RecordGenerationAdmission(admissionCtx, generationRequest, generation)
	admissionCancel()
	if err != nil {
		result.err = &terminalDeliveryError{err}
		result.uncertain = true
		return result
	}
	gen := &generationExecution{generation: generation, payload: payload, receipts: map[string]generationReceipt{}}
	result.ctx = context.WithValue(ctx, generationContextKey{}, gen)
	jobCtx = context.WithValue(jobCtx, generationContextKey{}, gen)
	allocated, err := runtime.AllocateStepRetryGeneration(jobCtx, payload, generationRequest.Identity.ClaimID, generation)
	if allocated != nil {
		result.session = &offlineJobSession{offlineSession: allocated}
	}
	if err != nil {
		result.err = err
		return result
	}
	session, ok := allocated.(offlineStepRetrySession)
	if !ok || allocated.Workspace() == "" || !filepath.IsAbs(allocated.Workspace()) {
		result.err = fmt.Errorf("failed-step allocation returned no live session")
		return result
	}
	result.checkout, err = checkoutJobInputs(jobCtx, client, conf, payload, session.Workspace())
	if err == nil {
		err = validateArtifactReferences(payload.ArtifactURIs, payload.ArtifactReferences, reference, time.Now())
	}
	if err == nil {
		err = handleArtifactDownload(jobCtx, payload.ArtifactURIs, payload.ArtifactReferences, session.Workspace(), store)
	}
	if err != nil {
		result.err = err
		return result
	}
	inputDigest, err := readGenerationInputDigest(jobCtx, client, payload)
	if err != nil {
		result.err = err
		return result
	}
	descriptor, err := session.StepAttemptSession(jobCtx)
	if err != nil {
		result.err = err
		return result
	}
	if err = descriptor.Validate(generation.RuntimeClaimID); err != nil {
		result.err = err
		return result
	}
	for index, step := range payload.Steps {
		plan := payload.RetryPlan.Steps[index]
		transition := taskpayload.StepAttemptTransition{Version: 1, Operation: "admit", Scope: stepAttemptScope(generationRequest.Identity, generation, inputDigest, descriptor, plan, 1)}
		for attempt := 1; attempt <= len(plan.AttemptRunIDs); attempt++ {
			if err = waitStepRetry(jobCtx, runtime.AdmissionDone(), 0); err != nil {
				result.err = err
				return result
			}
			transition.RequestDigest, err = transition.Digest()
			if err != nil {
				result.err = err
				return result
			}
			if err = session.RecordStepAttemptIntent(jobCtx, transition); err != nil {
				result.err = err
				return result
			}
			admissionResult, admissionErr := callStepAttemptTransition(jobCtx, client, runtime.AdmissionDone(), transition)
			if admissionErr != nil {
				result.err = &terminalDeliveryError{admissionErr}
				result.uncertain = true
				return result
			}
			admission := *admissionResult.Admission
			localPermissionErr := waitStepRetry(jobCtx, runtime.AdmissionDone(), 0)
			if localPermissionErr != nil {
				admissionResult.ExecutionAllowed = false
			}
			persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(jobCtx), 30*time.Second)
			if admissionResult.ExecutionAllowed {
				err = session.RecordStepAttemptAdmission(persistCtx, transition, admission)
				if err != nil {
					if denied := waitStepRetry(jobCtx, runtime.AdmissionDone(), 0); denied != nil {
						localPermissionErr = denied
						admissionResult.ExecutionAllowed = false
						// 取消与响应落盘竞争时仍保留同一描述；中毒日志会拒绝此审计写入
						err = session.RecordStepAttemptObservation(persistCtx, transition, admission)
					}
				}
			} else {
				err = session.RecordStepAttemptObservation(persistCtx, transition, admission)
			}
			persistCancel()
			if err != nil {
				result.err = &terminalDeliveryError{err}
				result.uncertain = true
				return result
			}
			execution := &stepAttemptExecution{admission: admission, session: session}
			stepCtx := context.WithValue(jobCtx, stepAttemptContextKey{}, execution)
			id := admission.Scope.StepRunID
			visited[id] = true
			env := mergeEnv(payload.Env, step.Env)
			env = maps.Clone(env)
			if env == nil {
				env = map[string]string{}
			}
			env[taskpayload.RuntimeGenerationEnv] = "1"
			// 条件只在该逻辑步骤的首次准入后求值，重试保持原决定
			allowed, conditionErr := true, error(nil)
			if attempt == 1 && admissionResult.ExecutionAllowed {
				allowed, conditionErr = evaluateStepCondition(step.When, payload.JobName, step.Name, env, payload.Variables)
			}
			stepStart := time.Now()
			reportUnstarted := func(cause error, conditionSkip bool) error {
				status, message := steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, "failed-step continuation refused"
				if conditionSkip {
					status, message = steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED, "condition evaluated to false"
				}
				if cause != nil {
					cause = redactJobError(payload, cause)
					message = cause.Error()
				}
				if errors.Is(cause, context.DeadlineExceeded) {
					status = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
				} else if errors.Is(cause, context.Canceled) {
					status = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
				}
				end := time.Now().Unix()
				return retainStepReport(stepCtx, id, cause, func(reportCtx context.Context) error {
					return reportStepRunStatus(reportCtx, client, conf, id, status, 0, message, stepStart, end, nil)
				})
			}
			if !admissionResult.ExecutionAllowed {
				refused := errors.Join(fmt.Errorf("current failed-step continuation refused"), localPermissionErr)
				result.err = acknowledgeStepAttempt(stepCtx, runtime.AdmissionDone(), id, reportUnstarted(refused, false))
				if _, pending := pendingStepReports.Load(receiptKey(stepCtx, "step", id)); pending || terminalPersistenceFailed(result.err) || terminalQuarantined(result.err) {
					result.err = &terminalDeliveryError{result.err}
					result.uncertain = true
				}
				return result
			}
			if conditionErr != nil || !allowed {
				err = acknowledgeStepAttempt(stepCtx, runtime.AdmissionDone(), id, reportUnstarted(conditionErr, conditionErr == nil))
				if err != nil {
					result.err = err
					return result
				}
				break
			}
			err = waitStepRetry(jobCtx, runtime.AdmissionDone(), time.Until(time.UnixMilli(admission.NotBeforeUnixMilli)))
			if err == nil {
				err = checkStepAdmissionTime(jobCtx, admission)
			}
			if err == nil {
				err = result.session.beforeStep(jobCtx, step)
			}
			if err != nil {
				result.err = reportUnstarted(err, false)
				return result
			}
			request := &taskpayload.StepRunTaskPayload{PipelineID: payload.PipelineID, PipelineRunID: payload.PipelineRunID, JobID: payload.JobRunID, JobName: payload.JobName, StepName: step.Name, StepIndex: step.StepIndex, StepRunID: id, Uses: step.Uses, Action: step.Action, Args: step.Args, Env: env, Workspace: session.Workspace(), Timeout: step.Timeout, AgentID: payload.AgentID}
			bound := &stepAttemptExecutor{session: session, admission: admission, drain: runtime.AdmissionDone()}
			if step.Uses == "builtin:artifacts" {
				bound.artifact = result.artifacts
			}
			executionErr := executeStepRunViaExecutor(stepCtx, conf, client, manager, request, bound)
			if terminalPersistenceFailed(executionErr) || terminalQuarantined(executionErr) {
				result.err = &terminalDeliveryError{executionErr}
				result.uncertain = true
				return result
			}
			if _, pending := pendingStepReports.Load(receiptKey(stepCtx, "step", id)); !pending && !execution.hasAcknowledgedReceipt() {
				if executionErr == nil || bound.invoked {
					result.err = &terminalDeliveryError{fmt.Errorf("admitted attempt has no exact acknowledged outcome")}
					result.uncertain = true
					return result
				}
				// RUNNING 的明确拒绝可能没有生成终态，只提交同一准入的未启动失败
				// 控制面的当前指针事务仍必须拒绝已被取消、取代或终结的身份
				executionErr = reportUnstarted(executionErr, false)
			}
			executionErr = acknowledgeStepAttempt(stepCtx, runtime.AdmissionDone(), id, executionErr)
			if _, pending := pendingStepReports.Load(receiptKey(stepCtx, "step", id)); pending || terminalPersistenceFailed(executionErr) || terminalQuarantined(executionErr) {
				result.err = &terminalDeliveryError{executionErr}
				result.uncertain = true
				return result
			}
			if executionErr == nil {
				break
			}
			if _, eligible := sandboxexec.RetryCommandExitCode(executionErr, payload.Retry.AllowedExitCodes); !eligible || attempt == len(plan.AttemptRunIDs) {
				result.err = executionErr
				return result
			}
			digest, err := execution.acknowledgedFailure()
			if err != nil {
				result.err = errors.Join(executionErr, err)
				return result
			}
			boundary, err := session.PausedStepBoundary(jobCtx, admission, digest)
			if err != nil {
				result.err = errors.Join(executionErr, fmt.Errorf("failed-step continuation refused: %w", err))
				return result
			}
			transition = taskpayload.StepAttemptTransition{Version: 1, Operation: "advance", Scope: stepAttemptScope(generationRequest.Identity, generation, inputDigest, descriptor, plan, attempt+1), PreviousStepRunID: id, PreviousStepClaimID: admission.Scope.StepClaimID, PreviousAdmissionID: admission.AdmissionID, PreviousAdmissionBindingDigest: admission.BindingDigest, FailedStepReceiptDigest: digest, Boundary: &boundary}
		}
	}
	return result
}
