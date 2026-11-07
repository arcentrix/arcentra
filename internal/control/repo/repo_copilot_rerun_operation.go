// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"reflect"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The private locator permits only an operation-specific ordered lock attempt.
// It is not a proof: the locked Job must match the complete committed claim.
type rerunOperationLocator struct {
	pool         gorm.ConnPool
	operation    executionLockMode
	identity     taskqueue.JobInputIdentity
	gate, intent string
}

const rerunOperationLocatorKey = "arcentra.rerun.operation.locator"
const rerunOperationProofKey = "arcentra.rerun.operation.proof"

type rerunOperationProof struct {
	locator                                                                    rerunOperationLocator
	schedule, seal, sealDigest, payloadDigest, retryPlanDigest, expectedCommit string
	jobSnapshot                                                                model.JobRun
	intentSnapshot                                                             model.CopilotRerunIntent
	scheduleSnapshot                                                           model.CopilotRerunSchedule
}

func rerunOperationRequest(tx *gorm.DB, mode executionLockMode) (rerunOperationLocator, bool) {
	if tx == nil || tx.Statement == nil || !rerunOperationMode(mode) {
		return rerunOperationLocator{}, false
	}
	v, ok := tx.Statement.Settings.Load(rerunOperationLocatorKey)
	request, valid := v.(rerunOperationLocator)
	return request, ok && valid && request.pool == tx.Statement.ConnPool && request.operation == mode
}
func rerunOperationRunMatches(tx *gorm.DB, mode executionLockMode, run *model.PipelineRun, jobID string) bool {
	request, ok := rerunOperationRequest(tx, mode)
	return ok && run.RunID == request.identity.PipelineRunID && run.PipelineID == request.identity.PipelineID && jobID == request.identity.JobRunID && run.CopilotRerunGateID == request.gate && run.CopilotRerunIntentID == request.intent
}
func rerunOperationLockedJobMatches(tx *gorm.DB, mode executionLockMode, job *model.JobRun) bool {
	request, ok := rerunOperationRequest(tx, mode)
	if !ok || job == nil {
		return false
	}
	if mode == executionLockRerunGenerationReceipt && !model.IsJobRunTerminal(job.Status) {
		return false
	}
	if !rerunOperationHistorical(mode) && mode != executionLockRerunHumanClose && mode != executionLockRerunFinalize && mode != executionLockRerunPostCancel && (job.Status != model.JobRunStatusRunning || job.CancellationClaimID != "" || job.TerminalReportDigest != "") {
		return false
	}
	i := request.identity
	return job.JobRunID == i.JobRunID && job.PipelineRunID == i.PipelineRunID && job.PipelineID == i.PipelineID && job.AgentID == i.AgentID && job.ClaimID == i.ClaimID && job.RuntimeBindingDigest == i.RuntimeBindingDigest && job.SourceBindingDigest == i.SourceBindingDigest && job.CopilotRerunGateID == request.gate && job.CopilotRerunIntentID == request.intent && job.StartTime != nil
}

func lockCopilotRerunInputOperation(ctx context.Context, tx *gorm.DB, identity taskqueue.JobInputIdentity) (*model.JobRun, *model.CopilotRerunIntent, *model.CopilotRerunSchedule, error) {
	return lockCopilotRerunOperation(ctx, tx, identity, executionLockRerunInput)
}
func lockCopilotRerunOperation(ctx context.Context, tx *gorm.DB, identity taskqueue.JobInputIdentity, mode executionLockMode) (*model.JobRun, *model.CopilotRerunIntent, *model.CopilotRerunSchedule, error) {
	if !rerunOperationMode(mode) {
		return nil, nil, nil, ErrCopilotRerunUnsupported
	}
	if identity.Validate() != nil || !rerunUniqueUUIDs(identity.PipelineRunID, identity.JobRunID, identity.ClaimID) {
		return nil, nil, nil, ErrCopilotRerunInvalid
	}
	value, ok := tx.Statement.Settings.Load(executionScopeLocatorKey)
	hint, valid := value.(executionScopeLocator)
	if !ok || !valid || hint.pool != tx.Statement.ConnPool || hint.jobErr != nil || !rerunUniqueUUIDs(hint.job.CopilotRerunGateID, hint.job.CopilotRerunIntentID, identity.PipelineRunID, identity.JobRunID, identity.ClaimID) {
		return nil, nil, nil, ErrCopilotRerunConflict
	}
	request := rerunOperationLocator{tx.Statement.ConnPool, mode, identity, hint.job.CopilotRerunGateID, hint.job.CopilotRerunIntentID}
	tx.Statement.Settings.Store(rerunOperationLocatorKey, request)
	job, err := lockExecutionJob(ctx, tx, identity.JobRunID, mode)
	if err != nil {
		return nil, nil, nil, err
	}
	// These are immutable hints, not locks. Current suffix rows are locked last.
	var intent model.CopilotRerunIntent
	if err := tx.WithContext(ctx).Where("intent_id = ?", job.CopilotRerunIntentID).First(&intent).Error; err != nil {
		return nil, nil, nil, err
	}
	var run model.PipelineRun
	if err := tx.WithContext(ctx).Select("run_id", "pipeline_id", "copilot_rerun_gate_id", "copilot_rerun_intent_id", "retry_approval_context", "retry_approval_context_digest").Where("run_id = ?", job.PipelineRunID).First(&run).Error; err != nil {
		return nil, nil, nil, err
	}
	if run.RunID != intent.NewRunID || run.PipelineID != intent.PipelineID || run.CopilotRerunGateID != intent.GateID || run.CopilotRerunIntentID != intent.IntentID || run.RetryApprovalContext != intent.ReviewedRunContext || run.RetryApprovalContextDigest != intent.ReviewedRunContextDigest {
		return nil, nil, nil, ErrCopilotRerunConflict
	}
	var schedule model.CopilotRerunSchedule
	if err := tx.WithContext(ctx).Where("schedule_id = ?", intent.ScheduleID).First(&schedule).Error; err != nil {
		return nil, nil, nil, err
	}
	if err := validateCopilotRerunOperationTuple(job, &intent, &schedule, mode); err != nil {
		return nil, nil, nil, err
	}
	proof := rerunOperationProof{locator: request, schedule: schedule.ScheduleID, seal: *schedule.SealID, sealDigest: schedule.SealDigest, payloadDigest: schedule.PayloadDigest, retryPlanDigest: job.RetryPlanDigest, expectedCommit: job.ExpectedSourceCommit, jobSnapshot: *job, intentSnapshot: intent, scheduleSnapshot: schedule}
	tx.Statement.Settings.Store(rerunOperationProofKey, proof)
	return job, &intent, &schedule, nil
}
func validateCopilotRerunOperationTuple(job *model.JobRun, intent *model.CopilotRerunIntent, schedule *model.CopilotRerunSchedule, mode executionLockMode) error {
	if rerunOperationHistorical(mode) || mode == executionLockRerunHumanClose || mode == executionLockRerunFinalize || mode == executionLockRerunPostCancel {
		if schedule.State != CopilotRerunScheduleClaimed && schedule.State != CopilotRerunScheduleCompleted && schedule.State != CopilotRerunScheduleCancelled {
			return ErrCopilotRerunConflict
		}
	} else if schedule.State != CopilotRerunScheduleClaimed || schedule.CancelRequested || schedule.CompletedAt != nil {
		return ErrCopilotRerunConflict
	}
	if intent.IntentID != job.CopilotRerunIntentID || intent.GateID != job.CopilotRerunGateID || intent.NewRunID != job.PipelineRunID || intent.NewJobRunID != job.JobRunID || intent.PipelineID != job.PipelineID || intent.State != CopilotRerunConfirmed || intent.IntentDigest != rerunIntentDigest(intent) || intent.ExpectedSourceCommit != job.ExpectedSourceCommit || schedule.IntentID != intent.IntentID || schedule.ScheduleID != intent.ScheduleID || schedule.RunID != job.PipelineRunID || schedule.JobRunID != job.JobRunID || schedule.PipelineID != job.PipelineID || schedule.ProjectID != intent.ProjectID || schedule.OwnerUserID != intent.OwnerUserID || schedule.AgentID != job.AgentID || schedule.ClaimedID != job.ClaimID || schedule.SealID == nil || schedule.ClaimedAt == nil || job.StartTime == nil || !schedule.ClaimedAt.Equal(*job.StartTime) || !schedule.ExpiresAt.Equal(intent.ExpiresAt) {
		return ErrCopilotRerunConflict
	}
	binding := CopilotRerunSealBinding{Version: 1, SealID: *schedule.SealID, ScheduleID: schedule.ScheduleID, RunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, PayloadDigest: schedule.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
	return ValidateCopilotRerunSealedBinding(schedule, &binding)
}
func requireCopilotRerunInputProof(tx *gorm.DB, job *model.JobRun) error {
	return requireCopilotRerunOperationProof(tx, job, executionLockRerunInput)
}
func requireCopilotRerunOperationProof(tx *gorm.DB, job *model.JobRun, mode executionLockMode) error {
	if tx == nil || tx.Statement == nil || job == nil {
		return ErrCopilotRerunUnsupported
	}
	v, ok := tx.Statement.Settings.Load(rerunOperationProofKey)
	p, valid := v.(rerunOperationProof)
	request, requestOK := rerunOperationRequest(tx, mode)
	if !requestOK || !reflect.DeepEqual(p.locator, request) || !ok || !valid || p.locator.pool != tx.Statement.ConnPool || p.locator.operation != mode || !rerunOperationLockedJobMatches(tx, mode, job) || p.retryPlanDigest != job.RetryPlanDigest || p.expectedCommit != job.ExpectedSourceCommit || !rerunUniqueUUIDs(p.schedule, p.seal) {
		return ErrCopilotRerunUnsupported
	}
	return nil
}
func finishCopilotRerunInputOperation(ctx context.Context, tx *gorm.DB, job *model.JobRun, intent *model.CopilotRerunIntent, schedule *model.CopilotRerunSchedule) error {
	return finishCopilotRerunOperation(ctx, tx, job, intent, schedule, executionLockRerunInput)
}
func finishCopilotRerunOperation(ctx context.Context, tx *gorm.DB, job *model.JobRun, intent *model.CopilotRerunIntent, schedule *model.CopilotRerunSchedule, mode executionLockMode) error {
	if err := requireCopilotRerunOperationProof(tx, job, mode); err != nil {
		return err
	}
	value, _ := tx.Statement.Settings.Load(rerunOperationProofKey)
	proof := value.(rerunOperationProof)
	if schedule.SealID == nil || proof.schedule != schedule.ScheduleID || proof.seal != *schedule.SealID || proof.sealDigest != schedule.SealDigest || proof.payloadDigest != schedule.PayloadDigest {
		return ErrCopilotRerunConflict
	}
	var approvalDeadline *time.Time
	if rerunOperationNeedsDeadline(mode) && job.RetryApprovalID != "" {
		var approval model.ApprovalRequest
		if err := tx.WithContext(ctx).Select("approval_id", "expires_at").Where("approval_id = ?", job.RetryApprovalID).First(&approval).Error; err != nil {
			return err
		}
		if approval.ApprovalID != job.RetryApprovalID || approval.ExpiresAt == nil {
			return ErrCopilotRerunConflict
		}
		approvalDeadline = approval.ExpiresAt
	}
	var lockedSchedule model.CopilotRerunSchedule
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", schedule.ScheduleID).First(&lockedSchedule).Error; err != nil {
		return err
	}
	var lockedIntent model.CopilotRerunIntent
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("intent_id = ?", intent.IntentID).First(&lockedIntent).Error; err != nil {
		return err
	}
	if !reflect.DeepEqual(*intent, lockedIntent) || !rerunClaimScheduleUnchanged(*schedule, lockedSchedule) {
		return ErrCopilotRerunConflict
	}
	if err := validateCopilotRerunOperationTuple(job, &lockedIntent, &lockedSchedule, mode); err != nil {
		return err
	}
	if rerunOperationHistorical(mode) {
		return ctx.Err()
	}
	if !rerunOperationNeedsDeadline(mode) {
		return checkRetryApprovalAuthorizationExpiry(ctx, tx)
	}
	if !time.Now().Before(lockedIntent.ExpiresAt) || !time.Now().Before(lockedSchedule.ExpiresAt) {
		return ErrCopilotRerunExpired
	}
	if job.Timeout != "" {
		duration, err := time.ParseDuration(job.Timeout)
		if err != nil || duration <= 0 || job.StartTime == nil || !time.Now().Before(job.StartTime.Add(duration)) {
			return ErrCopilotRerunExpired
		}
	}
	if approvalDeadline != nil && !time.Now().Before(*approvalDeadline) {
		return ErrCopilotRerunExpired
	}
	// This closes only projections actually established by underlying helpers.
	// Input preparation itself does not establish human execution authority;
	// startup must independently perform the full current authorization checks.
	return checkRetryApprovalAuthorizationExpiry(ctx, tx)
}

func rerunOperationMode(mode executionLockMode) bool {
	switch mode {
	case executionLockRerunInput, executionLockRerunGeneration, executionLockRerunGenerationInspect, executionLockRerunGenerationReceipt, executionLockRerunApprovalObserve, executionLockRerunApprovalStartup, executionLockRerunHuman, executionLockRerunHumanReceipt, executionLockRerunHumanClose, executionLockRerunStepActivate, executionLockRerunStepReport, executionLockRerunStepReceipt, executionLockRerunJobReport, executionLockRerunJobSuccess, executionLockRerunJobReceipt, executionLockRerunFinalize, executionLockRerunPostCancel, executionLockRerunJobCancelReceipt, executionLockRerunStepCancelReceipt:
		return true
	}
	return false
}
func rerunOperationHistorical(mode executionLockMode) bool {
	switch mode {
	case executionLockRerunGenerationInspect, executionLockRerunGenerationReceipt, executionLockRerunHumanReceipt, executionLockRerunStepReceipt, executionLockRerunJobReceipt, executionLockRerunJobCancelReceipt, executionLockRerunStepCancelReceipt:
		return true
	}
	return false
}

// Only an operation-specific proof minted in this actual transaction activates
// this completion fence. Ordinary transactions and explicit input fences remain unchanged.
func completeCopilotRerunOperation(ctx context.Context, tx *gorm.DB) error {
	value, ok := tx.Statement.Settings.Load(rerunOperationProofKey)
	if !ok {
		return nil
	}
	proof, valid := value.(rerunOperationProof)
	if !valid || !rerunOperationMode(proof.locator.operation) {
		return ErrCopilotRerunUnsupported
	}
	if proof.locator.operation == executionLockRerunInput {
		return requireCopilotRerunInputProof(tx, &proof.jobSnapshot)
	}
	return finishCopilotRerunOperation(ctx, tx, &proof.jobSnapshot, &proof.intentSnapshot, &proof.scheduleSnapshot, proof.locator.operation)
}
func rerunProtectedLocator(tx *gorm.DB) (model.JobRun, bool) {
	value, ok := tx.Statement.Settings.Load(executionScopeLocatorKey)
	hint, valid := value.(executionScopeLocator)
	return hint.job, ok && valid && hint.pool == tx.Statement.ConnPool && hint.jobErr == nil && !emptyJobRerun(&hint.job)
}

// Evidence observation and pending-approval closure never authorize commands.
// They retain any current permission projection's expiry, but expiration of the
// execution window must not prevent reading or closing the expired approval.
func rerunOperationNeedsDeadline(mode executionLockMode) bool {
	switch mode {
	case executionLockRerunInput, executionLockRerunGeneration, executionLockRerunApprovalStartup, executionLockRerunHuman, executionLockRerunStepActivate, executionLockRerunJobSuccess:
		return true
	}
	return false
}
