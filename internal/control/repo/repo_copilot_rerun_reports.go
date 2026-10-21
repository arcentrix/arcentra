// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"reflect"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const rerunStepProofKey = "arcentra.rerun.step.operation"

type rerunStepProof struct {
	pool                                                        gorm.ConnPool
	operation                                                   executionLockMode
	step, job, run, pipeline, agent, claim, generation, binding string
	attempt, index                                              int
}

func rerunStepBinding(tx *gorm.DB, mode executionLockMode, step *model.StepRun, claim string) rerunStepProof {
	generation := agentidentity.GenerationReportFromContext(tx.Statement.Context)
	return rerunStepProof{tx.Statement.ConnPool, mode, step.StepRunID, step.JobRunID, step.PipelineRunID, step.PipelineID, step.AgentID, claim, generation.ID, generation.BindingDigest, step.Attempt, step.StepIndex}
}
func lockCopilotRerunStepOperation(ctx context.Context, tx *gorm.DB, step *model.StepRun, claim string, next int) (*model.JobRun, error) {
	if step == nil {
		return nil, ErrCopilotRerunInvalid
	}
	mode := executionLockRerunStepReport
	if next == model.JobRunStatusRunning {
		mode = executionLockRerunStepActivate
	} else if model.IsJobRunTerminal(step.Status) {
		mode = executionLockRerunStepReceipt
	}
	expected := rerunStepBinding(tx, mode, step, claim)
	if value, ok := tx.Statement.Settings.Load(rerunOperationProofKey); ok {
		proof, valid := value.(rerunOperationProof)
		bound, boundOK := tx.Statement.Settings.Load(rerunStepProofKey)
		stored, typed := bound.(rerunStepProof)
		if !valid || !boundOK || !typed || stored != expected || requireCopilotRerunOperationProof(tx, &proof.jobSnapshot, mode) != nil {
			return nil, ErrCopilotRerunConflict
		}
		return &proof.jobSnapshot, nil
	}
	hint, protected := rerunProtectedLocator(tx)
	if !protected {
		return nil, ErrCopilotRerunUnsupported
	}
	job, _, _, err := lockCopilotRerunOperation(ctx, tx, generationIdentity(&hint), mode)
	if err != nil {
		return nil, err
	}
	if step.JobRunID != job.JobRunID || step.PipelineRunID != job.PipelineRunID || step.PipelineID != job.PipelineID || step.AgentID != job.AgentID || claim != agentidentity.StepClaimID(job.ClaimID, step.StepRunID) || (step.ClaimID != "" && step.ClaimID != claim) {
		return nil, ErrCopilotRerunConflict
	}
	tx.Statement.Settings.Store(rerunStepProofKey, expected)
	return job, nil
}
func lockCopilotRerunReportedJob(ctx context.Context, tx *gorm.DB, expected *model.JobRun, updates map[string]any) error {
	next, ok := updates["status"].(int)
	if expected == nil || !ok || !model.IsJobRunTerminal(next) || expected.ClaimID == "" {
		return ErrCopilotRerunConflict
	}
	for key := range updates {
		switch key {
		case "status", "error_message", "start_time", "end_time", "duration", "claim_id", "terminal_report_digest", "updated_at", "source_resolved_commit", "source_provenance_state", "artifact_uris", "artifact_references":
		default:
			return ErrCopilotRerunConflict
		}
	}
	mode := executionLockRerunJobReport
	if next == model.JobRunStatusSuccess {
		mode = executionLockRerunJobSuccess
	}
	current, _, _, err := lockCopilotRerunOperation(ctx, tx, generationIdentity(expected), mode)
	if err != nil {
		return err
	}
	if !jobSnapshotEqual(current, expected) || !rerunReportedStorageMatches(ctx, tx, expected) {
		return ErrCopilotRerunConflict
	}
	digest, _ := updates["terminal_report_digest"].(string)
	claim, _ := updates["claim_id"].(string)
	if !rerunTerminalDigest(digest) || claim != current.ClaimID || current.StartTime == nil {
		return ErrCopilotRerunConflict
	}
	// Agent wire timestamps have second precision. Preserve the immutable
	// millisecond claim start and record the first accepted server observation.
	// The canonical terminal digest still binds the original Agent timestamps.
	return normalizeCopilotRerunTerminalClock(updates, current.StartTime, time.Now())
}
func normalizeCopilotRerunTerminalClock(updates map[string]any, started *time.Time, observed time.Time) error {
	now := observed.UTC().Truncate(time.Millisecond)
	if started == nil || now.Before(*started) {
		return ErrCopilotRerunConflict
	}
	delete(updates, "start_time")
	updates["end_time"] = now
	updates["duration"] = now.Sub(*started).Milliseconds()
	return nil
}
func requireCopilotRerunReportedJob(tx *gorm.DB, job *model.JobRun) error {
	value, ok := tx.Statement.Settings.Load(rerunOperationProofKey)
	proof, valid := value.(rerunOperationProof)
	if !ok || !valid {
		return ErrCopilotRerunUnsupported
	}
	switch proof.locator.operation {
	case executionLockRerunJobReport, executionLockRerunJobSuccess, executionLockRerunJobReceipt, executionLockRerunJobCancelReceipt:
		return requireCopilotRerunOperationProof(tx, job, proof.locator.operation)
	}
	return ErrCopilotRerunUnsupported
}

// InspectCopilotRerunJobReceipt validates an exact existing claim snapshot only.
// It cannot disclose dispatch bytes, claim a job, or authorize another command.
func (r *JobRunRepo) InspectCopilotRerunJobReceipt(ctx context.Context, expected *model.JobRun) error {
	if expected == nil || emptyJobRerun(expected) {
		return ErrCopilotRerunInvalid
	}
	return executionTransaction(ctx, r.Database(), expected.JobRunID, func(tx *gorm.DB) error {
		current, _, _, err := lockCopilotRerunOperation(ctx, tx, generationIdentity(expected), executionLockRerunJobReceipt)
		if err != nil {
			return err
		}
		if !jobSnapshotEqual(current, expected) || !rerunReportedStorageMatches(ctx, tx, expected) {
			return ErrCopilotRerunConflict
		}
		return nil
	})
}

// The generic execution snapshot intentionally omits artifact storage. Protected
// receipt/report callers must also preserve the immutable dedicated-claim pin.
func rerunReportedStorageMatches(ctx context.Context, tx *gorm.DB, expected *model.JobRun) bool {
	var current model.JobRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id", "artifact_storage_id", "artifact_storage_fingerprint", "artifact_storage_type").Where("job_run_id = ?", expected.JobRunID).First(&current).Error; err != nil {
		return false
	}
	return current.JobRunID == expected.JobRunID && current.ArtifactStorageID == expected.ArtifactStorageID && current.ArtifactStorageFingerprint == expected.ArtifactStorageFingerprint && current.ArtifactStorageType == expected.ArtifactStorageType
}

// Historical artifact validation never invokes Upsert: the exact committed set
// is a receipt, including after cancellation, expiry, or authority revocation.
func validateCopilotRerunCompletedArtifacts(ctx context.Context, tx *gorm.DB, hint *model.JobRun, artifacts []*model.ProjectArtifact) error {
	job, _, _, err := lockCopilotRerunOperation(ctx, tx, generationIdentity(hint), executionLockRerunJobReceipt)
	if err != nil {
		return err
	}
	if job.Status != model.JobRunStatusSuccess || !rerunTerminalDigest(job.TerminalReportDigest) {
		return ErrArtifactConflict
	}
	var stored []model.ProjectArtifact
	if err := tx.WithContext(ctx).Where("job_run_id = ?", job.JobRunID).Limit(len(artifacts) + 1).Find(&stored).Error; err != nil {
		return err
	}
	if len(stored) != len(artifacts) {
		return ErrArtifactConflict
	}
	seen := map[string]bool{}
	for _, input := range artifacts {
		if input == nil || input.JobRunID != job.JobRunID {
			return ErrArtifactConflict
		}
		value := *input
		id := artifactutil.ID(value.ProjectID, value.PipelineRunID, value.JobRunID, value.StepRunID, value.Name)
		if value.ArtifactID == "" {
			value.ArtifactID = id
		}
		if value.ArtifactID != id || seen[id] {
			return ErrArtifactConflict
		}
		seen[id] = true
		matched := false
		for _, current := range stored {
			if current.ArtifactID != id {
				continue
			}
			value.BaseModel = current.BaseModel
			if value.ExpiresAt != nil {
				expiry := value.ExpiresAt.UTC()
				value.ExpiresAt = &expiry
			}
			if current.ExpiresAt != nil {
				expiry := current.ExpiresAt.UTC()
				current.ExpiresAt = &expiry
			}
			if !reflect.DeepEqual(value, current) {
				return ErrArtifactConflict
			}
			matched = true
		}
		if !matched {
			return ErrArtifactConflict
		}
	}
	return nil
}
