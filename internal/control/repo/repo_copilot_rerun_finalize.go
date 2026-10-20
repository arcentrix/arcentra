// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CopilotRerunTerminalReceipt struct {
	IntentID, ScheduleID, RunID, JobRunID, AgentID, ClaimID, TerminalReportDigest string
	RunStatus, JobStatus                                                          int
	CompletedAtUnixMilli                                                          int64
	AgentTerminalAcknowledged                                                     bool
}
type rerunTerminalRows struct {
	job        *model.JobRun
	run        model.PipelineRun
	intent     *model.CopilotRerunIntent
	schedule   *model.CopilotRerunSchedule
	ids        CopilotRerunExecutionIdentities
	layout     *CopilotRerunLayout
	steps      []model.StepRun
	generation *taskqueue.JobGeneration
}

func loadCopilotRerunTerminalRows(ctx context.Context, tx *gorm.DB, identity taskqueue.JobInputIdentity, mode executionLockMode) (*rerunTerminalRows, error) {
	job, intent, schedule, err := lockCopilotRerunOperation(ctx, tx, identity, mode)
	if err != nil {
		return nil, err
	}
	rows := &rerunTerminalRows{job: job, intent: intent, schedule: schedule}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_run_id = ?", job.JobRunID).First(job).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id = ?", job.PipelineRunID).First(&rows.run).Error; err != nil {
		return nil, err
	}
	if rows.run.RunID != intent.NewRunID || rows.run.PipelineID != intent.PipelineID || rows.run.ProjectID != intent.ProjectID || rows.run.CopilotRerunGateID != intent.GateID || rows.run.CopilotRerunIntentID != intent.IntentID || rows.run.TotalJobs != 1 || rows.run.StartTime == nil || job.StartTime == nil || !rows.run.StartTime.Equal(*job.StartTime) {
		return nil, ErrCopilotRerunConflict
	}
	var snapshot model.CopilotDefinitionSnapshot
	if err := tx.WithContext(ctx).Where("snapshot_id = ?", intent.SnapshotID).First(&snapshot).Error; err != nil {
		return nil, err
	}
	if ValidateCopilotDefinitionSnapshot(&snapshot) != nil || snapshot.SnapshotID != intent.SnapshotID || snapshot.BindingDigest != intent.SnapshotDigest {
		return nil, ErrCopilotRerunConflict
	}
	rows.layout, err = ParseCopilotRerunLayout(snapshot.LayoutJSON)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(intent.StepIdentitiesJSON), &rows.ids) != nil || rows.ids.Validate(rows.layout) != nil || rows.ids.RunID != job.PipelineRunID || rows.ids.JobRunID != job.JobRunID || rows.ids.GateID != intent.GateID || rows.ids.ScheduleID != schedule.ScheduleID {
		return nil, ErrCopilotRerunConflict
	}
	if err := validateCopilotRerunClaimDispatch(&CopilotRerunScope{Job: job}, &rows.ids); err != nil {
		return nil, err
	}
	var children []model.JobRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("pipeline_run_id = ?", job.PipelineRunID).Limit(2).Find(&children).Error; err != nil {
		return nil, err
	}
	if len(children) != 1 || children[0].JobRunID != job.JobRunID {
		return nil, ErrCopilotRerunConflict
	}
	if job.CurrentGenerationID != "" {
		_, generation, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
		if err != nil || generation == nil || generation.Number != job.CurrentGeneration || !generationMatchesJob(generation, job) {
			return nil, ErrCopilotRerunConflict
		}
		rows.generation = generation
	} else if job.CurrentGeneration != 0 {
		return nil, ErrCopilotRerunConflict
	}
	if mode != executionLockRerunPostCancel {
		if err := lockCopilotRerunTerminalSteps(ctx, tx, rows); err != nil {
			return nil, err
		}
	}

	return rows, nil
}
func lockCopilotRerunTerminalSteps(ctx context.Context, tx *gorm.DB, rows *rerunTerminalRows) error {
	job, intent := rows.job, rows.intent
	count := 0
	for _, step := range rows.ids.Steps {
		count += len(step.Attempts)
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_run_id = ?", job.JobRunID).Order("id").Limit(count + 1).Find(&rows.steps).Error; err != nil {
		return err
	}
	if len(rows.steps) != count {
		return ErrCopilotRerunConflict
	}
	indexed := map[string]*model.StepRun{}
	for i := range rows.steps {
		indexed[rows.steps[i].StepRunID] = &rows.steps[i]
	}
	policy, _ := json.Marshal(rows.layout.Retry)
	for i, fixed := range rows.ids.Steps {
		for attempt, id := range fixed.Attempts {
			row := indexed[id]
			previous := ""
			if attempt > 0 {
				previous = fixed.Attempts[attempt-1]
			}
			if row == nil || row.JobRunID != job.JobRunID || row.PipelineRunID != job.PipelineRunID || row.PipelineID != job.PipelineID || row.AgentID != job.AgentID || row.Name != fixed.Name || row.StepIndex != int(fixed.Index) || row.Attempt != attempt+1 || row.PreviousStepRunID != previous || row.Uses != rows.layout.Steps[i].Uses || row.Action != rows.layout.Steps[i].Action || row.ContinueOnError != 0 || row.CreatedBy != intent.OwnerUserID || row.RetryMode != "whole_job" || !rerunJSONEqual(row.RetryPolicy, string(policy)) {
				return ErrCopilotRerunConflict
			}
		}
	}
	return nil
}
func rerunTerminalDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}
func validateCopilotRerunTerminalRows(rows *rerunTerminalRows) (int, int, error) {
	job := rows.job
	if !model.IsJobRunTerminal(job.Status) || job.EndTime == nil || job.StartTime == nil || job.EndTime.Before(*job.StartTime) {
		return 0, 0, ErrCopilotRerunConflict
	}
	controllerCancel := job.Status == model.JobRunStatusCancelled && job.CancellationClaimID == job.ClaimID
	if !controllerCancel && !rerunTerminalDigest(job.TerminalReportDigest) {
		return 0, 0, ErrCopilotRerunConflict
	}
	if job.Status == model.JobRunStatusSuccess && rows.generation == nil {
		return 0, 0, ErrCopilotRerunConflict
	}
	failed := 0
	for _, step := range rows.steps {
		if !model.IsJobRunTerminal(step.Status) || step.EndTime == nil {
			return 0, 0, ErrCopilotRerunConflict
		}
		cancelled := controllerCancel && step.Status == model.JobRunStatusCancelled && step.CancellationClaimID == job.ClaimID
		if !cancelled && (!rerunTerminalDigest(step.TerminalReportDigest) || step.ClaimID != agentidentity.StepClaimID(job.ClaimID, step.StepRunID)) {
			return 0, 0, ErrCopilotRerunConflict
		}
		if !cancelled && step.Attempt == job.CurrentGeneration && (rows.generation == nil || step.GenerationID != rows.generation.GenerationID || step.GenerationBindingDigest != rows.generation.BindingDigest) {
			return 0, 0, ErrCopilotRerunConflict
		}
		if step.Attempt == job.CurrentGeneration && (step.Status == model.JobRunStatusFailed || step.Status == model.JobRunStatusTimeout) {
			failed++
		}
		if job.Status == model.JobRunStatusSuccess && step.Attempt == job.CurrentGeneration && step.Status != model.JobRunStatusSuccess && step.Status != model.JobRunStatusSkipped {
			return 0, 0, ErrCopilotRerunConflict
		}
	}
	status := model.PipelineStatusFailed
	if job.Status == model.JobRunStatusSuccess || job.Status == model.JobRunStatusSkipped {
		status = model.PipelineStatusSuccess
	}
	if job.Status == model.JobRunStatusCancelled {
		status = model.PipelineStatusCancelled
	}
	return status, failed, nil
}

// FinalizeCopilotRerun derives the single-job aggregate only from committed
// terminal receipts. It does not claim, dispatch, or create execution authority.
func (r *JobRunRepo) FinalizeCopilotRerun(ctx context.Context, identity taskqueue.JobInputIdentity) (*CopilotRerunTerminalReceipt, error) {
	var result *CopilotRerunTerminalReceipt
	err := executionTransaction(ctx, r.Database(), identity.JobRunID, func(tx *gorm.DB) error {
		rows, err := loadCopilotRerunTerminalRows(ctx, tx, identity, executionLockRerunFinalize)
		if err != nil {
			return err
		}
		status, failed, err := validateCopilotRerunTerminalRows(rows)
		if err != nil {
			return err
		}
		if err := finishCopilotRerunOperation(ctx, tx, rows.job, rows.intent, rows.schedule, executionLockRerunFinalize); err != nil {
			return err
		}
		if err := writeCopilotRerunTerminalAggregate(ctx, tx, rows, status, failed); err != nil {
			return err
		}
		result = rerunTerminalReceipt(rows, status)
		return nil
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return result, nil
}
func rerunTerminalReceipt(rows *rerunTerminalRows, status int) *CopilotRerunTerminalReceipt {
	j := rows.job
	acknowledged := rerunTerminalDigest(j.TerminalReportDigest)
	for _, step := range rows.steps {
		claimMatches := step.ClaimID == agentidentity.StepClaimID(j.ClaimID, step.StepRunID) || (step.ClaimID == "" && step.Status == model.JobRunStatusCancelled && step.CancellationClaimID == j.ClaimID)
		acknowledged = acknowledged && rerunTerminalDigest(step.TerminalReportDigest) && claimMatches
	}
	return &CopilotRerunTerminalReceipt{IntentID: rows.intent.IntentID, ScheduleID: rows.schedule.ScheduleID, RunID: j.PipelineRunID, JobRunID: j.JobRunID, AgentID: j.AgentID, ClaimID: j.ClaimID, TerminalReportDigest: j.TerminalReportDigest, RunStatus: status, JobStatus: j.Status, CompletedAtUnixMilli: j.EndTime.UnixMilli(), AgentTerminalAcknowledged: acknowledged}
}
func writeCopilotRerunTerminalAggregate(ctx context.Context, tx *gorm.DB, rows *rerunTerminalRows, status, failedSteps int) error {
	job, run, schedule := rows.job, &rows.run, rows.schedule
	// The final fence has acquired Schedule. Lease-only progress is permitted,
	// so use its current CAS version rather than the earlier immutable hint.
	var currentSchedule model.CopilotRerunSchedule
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", schedule.ScheduleID).First(&currentSchedule).Error; err != nil {
		return err
	}
	if !rerunClaimScheduleUnchanged(*schedule, currentSchedule) {
		return ErrCopilotRerunConflict
	}
	*schedule = currentSchedule
	end := job.EndTime.UTC().Truncate(time.Millisecond)
	duration := end.Sub(*job.StartTime).Milliseconds()
	if duration < 0 {
		return ErrCopilotRerunConflict
	}
	state := CopilotRerunScheduleCompleted
	if status == model.PipelineStatusCancelled {
		state = CopilotRerunScheduleCancelled
	}
	failedJobs := 0
	if status == model.PipelineStatusFailed {
		failedJobs = 1
	}
	if schedule.State != CopilotRerunScheduleClaimed {
		if schedule.State != state || schedule.CompletedAt == nil || schedule.CompletedAt.UnixMilli() != end.UnixMilli() || run.Status != status || run.EndTime == nil || run.EndTime.UnixMilli() != end.UnixMilli() || run.RunningJobs != 0 || run.CompletedJobs != 1 || run.FailedJobs != failedJobs || job.CompletedSteps != len(rows.layout.Steps) || job.FailedSteps != failedSteps {
			return ErrCopilotRerunConflict
		}
		return nil
	}
	if run.Status != model.PipelineStatusRunning || run.EndTime != nil {
		return ErrCopilotRerunConflict
	}
	if err := reportedStatusResult(tx.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ? AND claim_id = ? AND status = ? AND terminal_report_digest = ? AND copilot_rerun_intent_id = ? AND copilot_rerun_gate_id = ?", job.JobRunID, job.ClaimID, job.Status, job.TerminalReportDigest, job.CopilotRerunIntentID, job.CopilotRerunGateID).Updates(map[string]any{"completed_steps": len(rows.layout.Steps), "failed_steps": failedSteps})); err != nil {
		return err
	}
	if err := reportedStatusResult(tx.WithContext(ctx).Model(&model.PipelineRun{}).Where("run_id = ? AND status = ? AND copilot_rerun_intent_id = ? AND copilot_rerun_gate_id = ?", run.RunID, model.PipelineStatusRunning, job.CopilotRerunIntentID, job.CopilotRerunGateID).Updates(map[string]any{"status": status, "running_jobs": 0, "completed_jobs": 1, "failed_jobs": failedJobs, "end_time": end, "duration": duration})); err != nil {
		return err
	}
	if err := reportedStatusResult(tx.WithContext(ctx).Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ? AND version = ? AND state = ? AND claimed_id = ?", schedule.ScheduleID, schedule.Version, CopilotRerunScheduleClaimed, job.ClaimID).Updates(map[string]any{"state": state, "completed_at": end, "cancel_requested": state == CopilotRerunScheduleCancelled, "version": schedule.Version + 1})); err != nil {
		return err
	}
	// Replace only the suffix snapshot with the exact row just committed in this
	// transaction; the automatic final fence still checks every immutable field.
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", schedule.ScheduleID).First(schedule).Error; err != nil {
		return err
	}
	value, _ := tx.Statement.Settings.Load(rerunOperationProofKey)
	proof := value.(rerunOperationProof)
	proof.scheduleSnapshot = *schedule
	tx.Statement.Settings.Store(rerunOperationProofKey, proof)
	return nil
}
