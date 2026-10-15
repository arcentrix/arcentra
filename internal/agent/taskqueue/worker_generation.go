package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/retry"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type generationContextKey struct{}
type generationPureExitKey struct{}
type generationReceipt struct {
	request *agentv1.ReportStepRunStatusRequest
	digest  string
}
type generationExecution struct {
	approvalWait *retryApprovalWait
	generation   taskpayload.JobGeneration
	payload      *taskpayload.JobRunTaskPayload
	mu           sync.Mutex
	receipts     map[string]generationReceipt
}

func generationFromContext(ctx context.Context) *generationExecution {
	value, _ := ctx.Value(generationContextKey{}).(*generationExecution)
	return value
}
func (g *generationExecution) retryableStep(stepID string) bool {
	for _, step := range g.payload.Steps {
		if step.ContinueOnError || (step.Uses != "shell" && step.Uses != "builtin:shell") {
			continue
		}
		for _, id := range stepAttemptIDs(step) {
			if id == stepID {
				return true
			}
		}
	}
	return false
}

func (g *generationExecution) failure() (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var stepID, digest string
	for id, receipt := range g.receipts {
		if !receipt.request.RetryableShellExit {
			continue
		}
		if stepID != "" {
			return "", "", fmt.Errorf("ambiguous retry failure receipts")
		}
		stepID, digest = id, receipt.digest
	}
	if stepID == "" {
		return "", "", fmt.Errorf("retry requires an acknowledged pure shell failure")
	}
	return stepID, digest, nil
}
func observeGenerationStepReceipt(ctx context.Context, record terminalRecord) error {
	generation := generationFromContext(ctx)
	if generation == nil || record.Kind != "step" {
		return nil
	}
	var request agentv1.ReportStepRunStatusRequest
	if err := proto.Unmarshal(record.Payload, &request); err != nil {
		return err
	}
	p := generation.payload
	digest, err := agentidentity.TerminalReceiptDigest(&request, record.ClaimID, p.PipelineID, p.PipelineRunID, p.JobRunID)
	if err != nil {
		return err
	}
	if err := observeStepAttemptReceipt(ctx, record, &request, digest); err != nil {
		return err
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	if previous, ok := generation.receipts[record.RunID]; ok && previous.digest != digest {
		return fmt.Errorf("generation receipt changed")
	}
	generation.receipts[record.RunID] = generationReceipt{request: &request, digest: digest}
	return nil
}

type generationJobResult struct {
	ctx             context.Context
	artifacts       *artifactExecutor
	checkout        *agentv1.SourceCheckout
	session         *offlineJobSession
	err             error
	uncertain       bool
	journalRequired bool
}

func generationInputIdentity(ctx context.Context, p *taskpayload.JobRunTaskPayload) taskpayload.JobInputIdentity {
	source := ""
	if p.Source != nil {
		source = p.Source.BindingDigest
	}
	return taskpayload.JobInputIdentity{PipelineID: p.PipelineID, PipelineRunID: p.PipelineRunID, JobRunID: p.JobRunID, AgentID: p.AgentID, ClaimID: executionClaimID(ctx, "job", p.JobRunID), RuntimeBindingDigest: p.Runtime.BindingDigest, SourceBindingDigest: source}
}

func callGenerationTransition(ctx context.Context, client *agentgrpc.ClientWrapper, transition taskpayload.JobGenerationTransition) (taskpayload.JobGeneration, error) {
	var empty taskpayload.JobGeneration
	encoded, err := transition.Canonical()
	if err != nil {
		return empty, err
	}
	if client == nil || client.AgentClient == nil {
		return empty, fmt.Errorf("generation controller unavailable")
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		rpcCtx, cancel := client.WithTimeoutAndAuth(ctx)
		response, callErr := client.AgentClient.ReconcileJobGeneration(withExecutionClaim(rpcCtx, "job", transition.Identity.JobRunID), &agentv1.ReconcileJobGenerationRequest{AgentId: transition.Identity.AgentID, TransitionJson: encoded})
		cancel()
		if callErr == nil {
			if response == nil {
				return empty, fmt.Errorf("controller returned no generation")
			}
			generation, err := taskpayload.ParseJobGeneration(response.GenerationJson)
			if err != nil {
				return empty, err
			}
			identity := transition.Identity
			if !generation.ExecutionAllowed || generation.PipelineID != identity.PipelineID || generation.PipelineRunID != identity.PipelineRunID || generation.JobRunID != identity.JobRunID || generation.AgentID != identity.AgentID || generation.OriginalClaimID != identity.ClaimID || generation.RuntimeBindingDigest != identity.RuntimeBindingDigest || generation.SourceBindingDigest != identity.SourceBindingDigest || generation.RetryPlanDigest != transition.RetryPlanDigest || generation.InputManifestDigest != transition.InputManifestDigest {
				return empty, fmt.Errorf("controller generation does not match exact transition")
			}
			if transition.Operation == "admit" && (generation.Number != 1 || generation.PreviousGenerationID != "") || transition.Operation == "advance" && generation.PreviousGenerationID != transition.PreviousGenerationID {
				return empty, fmt.Errorf("controller generation predecessor mismatch")
			}
			return *generation, nil
		}
		last = callErr
		code := grpcstatus.Code(callErr)
		if code != codes.Unavailable && code != codes.DeadlineExceeded {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return empty, ctx.Err()
		case <-timer.C:
		}
	}
	return empty, last
}

// confirmRejectedGenerationAdvance 只观察并持久化仍为已清理前代的状态，不授予执行或删除转换意图
// 最终失败报告仍由控制面当前代次 CAS 仲裁与延迟 advance 或取消的竞争
func confirmRejectedGenerationAdvance(ctx context.Context, client *agentgrpc.ClientWrapper, runtime offlineGenerationRuntime, transition taskpayload.JobGenerationTransition, previous *generationExecution) error {
	if client == nil || client.AgentClient == nil || !client.ControllerAuthenticated() || previous == nil || previous.payload == nil || !previous.payload.RetryApprovalRequired || transition.Operation != "advance" || transition.CleanupReceipt == nil {
		return fmt.Errorf("authenticated gated predecessor observation required")
	}
	expected := previous.generation
	if transition.PreviousGenerationID != expected.GenerationID || transition.Identity != generationInputIdentity(ctx, previous.payload) || transition.RetryPlanDigest != expected.RetryPlanDigest {
		return fmt.Errorf("rejected transition differs from original predecessor")
	}
	if err := transition.CleanupReceipt.ValidateGeneration(expected); err != nil {
		return err
	}
	inspect := taskpayload.JobGenerationTransition{Version: 1, Operation: "inspect", Identity: transition.Identity, RetryPlanDigest: transition.RetryPlanDigest, InputManifestDigest: transition.InputManifestDigest}
	var err error
	inspect.RequestDigest, err = inspect.Digest()
	if err != nil {
		return err
	}
	encoded, err := inspect.Canonical()
	if err != nil {
		return err
	}
	rpcCtx, cancel := client.WithTimeoutAndAuth(ctx)
	defer cancel()
	response, err := client.AgentClient.ReconcileJobGeneration(withExecutionClaim(rpcCtx, "job", transition.Identity.JobRunID), &agentv1.ReconcileJobGenerationRequest{AgentId: transition.Identity.AgentID, TransitionJson: encoded})
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("missing predecessor observation")
	}
	observed, err := taskpayload.ParseJobGeneration(response.GenerationJson)
	if err != nil {
		return err
	}
	if observed.ExecutionAllowed {
		return fmt.Errorf("observation is not a closed executed predecessor")
	}
	expected.ExecutionAllowed = false
	expected.PreparationOnly = observed.PreparationOnly
	if *observed != expected {
		return fmt.Errorf("generation changed while resolving rejected advance")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return runtime.RecordGenerationAdmission(ctx, transition, *observed)
}

func readGenerationInputDigest(ctx context.Context, client *agentgrpc.ClientWrapper, payload *taskpayload.JobRunTaskPayload) (string, error) {
	identity := generationInputIdentity(ctx, payload)
	expected, err := taskpayload.NewJobInputManifest("", payload.ArtifactURIs, payload.ArtifactReferences)
	if err != nil {
		return "", err
	}
	rpcCtx, cancel := client.WithTimeoutAndAuth(ctx)
	defer cancel()
	response, err := client.AgentClient.GetOrRecordJobInputs(withExecutionClaim(rpcCtx, "job", payload.JobRunID), &agentv1.GetOrRecordJobInputsRequest{AgentId: identity.AgentID, PipelineId: identity.PipelineID, PipelineRunId: identity.PipelineRunID, JobRunId: identity.JobRunID, OriginalClaimId: identity.ClaimID, RuntimeBindingDigest: identity.RuntimeBindingDigest, SourceBindingDigest: identity.SourceBindingDigest})
	if err != nil {
		return "", err
	}
	if _, err = verifyJobInputResponse(response, identity, expected, false); err != nil {
		return "", err
	}
	return response.ManifestDigest, nil
}

func generationRoundPayload(payload *taskpayload.JobRunTaskPayload, number int) (*taskpayload.JobRunTaskPayload, error) {
	if number < 1 || number > int(payload.Retry.MaxAttempts) {
		return nil, fmt.Errorf("invalid generation ordinal")
	}
	round := *payload
	round.Retry = nil
	round.RetryPlan = nil
	round.RetryPlanDigest = ""
	round.Env = maps.Clone(payload.Env)
	if round.Env == nil {
		round.Env = map[string]string{}
	}
	round.Env[taskpayload.RuntimeGenerationEnv] = strconv.Itoa(number)
	round.Steps = make([]taskpayload.StepPayload, len(payload.Steps))
	for i, step := range payload.Steps {
		ids := stepAttemptIDs(step)
		if len(ids) < number {
			return nil, fmt.Errorf("generation has no preallocated step")
		}
		step.StepRunID = ids[number-1]
		step.AttemptRunIDs = []string{step.StepRunID}
		step.Env = maps.Clone(step.Env)
		round.Steps[i] = step
	}
	return &round, nil
}

func finishUnusedGenerationAttempts(ctx, jobCtx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, payload *taskpayload.JobRunTaskPayload, last int, start time.Time) error {
	status := steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED
	reason := "generation not admitted"
	if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
		reason = "job deadline exceeded"
	} else if errors.Is(jobCtx.Err(), context.Canceled) {
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
		reason = "job cancelled"
	}
	var result error
	for _, step := range payload.Steps {
		for _, stepID := range stepAttemptIDs(step)[last:] {
			end := time.Now().Unix()
			result = errors.Join(result, retainStepReport(ctx, stepID, nil, func(reportCtx context.Context) error {
				return reportStepRunStatus(reportCtx, client, conf, stepID, status, 0, reason, start, end, nil)
			}))
		}
	}
	return result
}

func executeContainerGenerations(ctx, jobCtx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager, payload *taskpayload.JobRunTaskPayload, store storage.IStorage, reference taskpayload.ArtifactReference, start time.Time) (result generationJobResult) {
	result = generationJobResult{ctx: ctx, artifacts: &artifactExecutor{job: payload, storage: store, reference: reference, uploaded: map[string]string{}}}
	last := 0
	approvalWait := newRetryApprovalWait(jobCtx)
	if payload.RetryApprovalRequired {
		if payload.RetryApprovalTimeoutSeconds < 1 || payload.RetryApprovalTimeoutSeconds > 3600 {
			result.err = fmt.Errorf("fixed operator retry approval budget required")
			return result
		}
		operatorDeadline := time.Now().Add(time.Duration(payload.RetryApprovalTimeoutSeconds) * time.Second)
		if operatorDeadline.Before(approvalWait.deadline) {
			approvalWait.deadline = operatorDeadline
		}
	}
	if payload.RetryApprovalRequired && (client == nil || !client.ControllerAuthenticated()) {
		result.err = fmt.Errorf("retry approval requires server-authenticated TLS")
		return result
	}
	defer func() {
		if result.uncertain {
			return
		}
		if result.session != nil {
			result.err = errors.Join(result.err, result.session.finish(result.ctx))
		}
		result.err = errors.Join(result.err, finishUnusedGenerationAttempts(result.ctx, jobCtx, conf, client, payload, last, start))
	}()
	runtime, ok := offlineRuntimeFromContext(ctx).(offlineGenerationRuntime)
	if !ok {
		result.err = fmt.Errorf("runtime does not support durable whole-job generations")
		return result
	}
	if _, exists := conf.Agent.EnvVars[taskpayload.RuntimeGenerationEnv]; exists {
		result.err = fmt.Errorf("Agent environment overrides reserved generation variable")
		return result
	}
	transition := taskpayload.JobGenerationTransition{Version: 1, Identity: generationInputIdentity(ctx, payload), RetryPlanDigest: payload.RetryPlanDigest, Operation: "admit"}
	for {
		if err := jobCtx.Err(); err != nil {
			result.err = errors.Join(result.err, err)
			break
		}
		select {
		case <-runtime.AdmissionDone():
			result.err = errors.Join(result.err, fmt.Errorf("Agent is draining; no retry generation admitted"))
			break
		default:
		}
		if result.err != nil {
			break
		}
		var err error
		transition.RequestDigest, err = transition.Digest()
		if err != nil {
			result.err = err
			break
		}
		if err = runtime.RecordGenerationIntent(jobCtx, transition); err != nil {
			result.err = err
			break
		}
		result.journalRequired = true
		generation, err := callGenerationTransition(jobCtx, client, transition)
		if err != nil {
			if payload.RetryApprovalRequired && transition.Operation == "advance" && grpcstatus.Code(err) == codes.FailedPrecondition {
				if observationErr := confirmRejectedGenerationAdvance(jobCtx, client, runtime, transition, generationFromContext(result.ctx)); observationErr == nil {
					result.err = fmt.Errorf("successor admission denied after verified predecessor cleanup: %w", err)
					break
				} else {
					err = errors.Join(err, observationErr)
				}
			}
			result.err = &terminalDeliveryError{fmt.Errorf("generation admission is unresolved; preserve journal and reconcile offline: %w", err)}
			result.uncertain = true
			return result
		}
		if generation.Number != last+1 {
			result.err = &terminalDeliveryError{fmt.Errorf("generation ordinal changed; preserve journal")}
			result.uncertain = true
			return result
		}
		if err = runtime.RecordGenerationAdmission(jobCtx, transition, generation); err != nil {
			result.err = &terminalDeliveryError{err}
			result.uncertain = true
			return result
		}
		generationCtx := &generationExecution{approvalWait: approvalWait, generation: generation, payload: payload, receipts: map[string]generationReceipt{}}
		result.ctx = context.WithValue(ctx, generationContextKey{}, generationCtx)
		delay := time.Until(time.UnixMilli(generation.NotBeforeUnixMilli))
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-jobCtx.Done():
				result.err = jobCtx.Err()
			case <-runtime.AdmissionDone():
				result.err = fmt.Errorf("Agent draining before generation allocation")
			case <-timer.C:
			}
			timer.Stop()
		}
		if result.err != nil {
			break
		}
		if err = jobCtx.Err(); err != nil {
			result.err = err
			break
		}
		round, err := generationRoundPayload(payload, generation.Number)
		if err != nil {
			result.err = err
			break
		}
		allocated, err := runtime.AllocateGeneration(jobCtx, payload, transition.Identity.ClaimID, generation)
		if allocated != nil {
			result.session = &offlineJobSession{offlineSession: allocated}
		}
		if err != nil {
			result.err = err
			break
		}
		if allocated == nil || allocated.Workspace() == "" {
			result.err = fmt.Errorf("generation allocation returned no workspace")
			break
		}
		last = generation.Number
		roundCtx := context.WithValue(jobCtx, generationContextKey{}, generationCtx)
		result.artifacts = &artifactExecutor{job: round, storage: store, reference: reference, uploaded: map[string]string{}}
		checkout, preparationErr := checkoutJobInputs(roundCtx, client, conf, payload, allocated.Workspace())
		if result.checkout == nil {
			result.checkout = checkout
		} else if checkout != nil && checkout.ResolvedCommit != result.checkout.ResolvedCommit {
			preparationErr = errors.Join(preparationErr, fmt.Errorf("generation source commit changed"))
		}
		if preparationErr == nil {
			preparationErr = validateArtifactReferences(payload.ArtifactURIs, payload.ArtifactReferences, reference, time.Now())
		}
		if preparationErr == nil {
			preparationErr = handleArtifactDownload(roundCtx, payload.ArtifactURIs, payload.ArtifactReferences, allocated.Workspace(), store)
		}
		if preparationErr == nil && payload.RetryApprovalRequired {
			preparationErr = waitRetryApproval(roundCtx, client, generationCtx, runtime.AdmissionDone())
		}
		roundErr := executeJobAttempts(result.ctx, roundCtx, conf, client, manager, round, result.artifacts, allocated.Workspace(), preparationErr, start, result.session)
		cleanupErr := result.session.finish(result.ctx)
		result.err = errors.Join(roundErr, cleanupErr)
		if result.err == nil || last >= int(payload.Retry.MaxAttempts) || preparationErr != nil || cleanupErr != nil || retry.IsBlocked(roundErr) || jobCtx.Err() != nil {
			break
		}
		if _, eligible := sandboxexec.RetryCommandExitCode(roundErr, payload.Retry.AllowedExitCodes); !eligible {
			break
		}
		stepID, digest, failureErr := generationCtx.failure()
		if failureErr != nil {
			result.err = errors.Join(result.err, failureErr)
			break
		}
		cleanupSession, ok := allocated.(offlineGenerationSession)
		if !ok {
			result.err = errors.Join(result.err, fmt.Errorf("generation cleanup receipt unavailable"))
			break
		}
		cleanup, cleanupErr := cleanupSession.CleanupReceipt(result.ctx)
		if cleanupErr != nil {
			result.err = errors.Join(result.err, cleanupErr)
			break
		}
		inputDigest, inputErr := readGenerationInputDigest(roundCtx, client, payload)
		if inputErr != nil {
			result.err = errors.Join(result.err, inputErr)
			break
		}
		transition = taskpayload.JobGenerationTransition{Version: 1, Identity: transition.Identity, InputManifestDigest: inputDigest, RetryPlanDigest: payload.RetryPlanDigest, Operation: "advance", PreviousGenerationID: generation.GenerationID, FailedStepRunID: stepID, FailedStepReceiptDigest: digest, CleanupReceipt: &cleanup}
		result.err = nil
	}
	return result
}

func freezeGenerationReport(ctx context.Context, report func(context.Context) error) func(context.Context) error {
	generation := generationFromContext(ctx)
	attempt := stepAttemptFromContext(ctx)
	if generation == nil && attempt == nil {
		return report
	}
	return func(reportCtx context.Context) error {
		if generation != nil {
			reportCtx = context.WithValue(reportCtx, generationContextKey{}, generation)
		}
		if attempt != nil {
			reportCtx = context.WithValue(reportCtx, stepAttemptContextKey{}, attempt)
		}
		return report(reportCtx)
	}
}
