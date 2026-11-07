// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
)

// CopilotRerunCancellationLocator 仅供内部选择取消事务，不包含执行授权或敏感负载
// Every branch must validate this hint and current permission again under its
// own ordered transaction. It must never be serialized as an HTTP response.
type CopilotRerunCancellationLocator struct {
	Protected                   bool                          `json:"-"`
	Claimed                     bool                          `json:"-"`
	PipelineID, RunID, JobRunID string                        `json:"-"`
	IntentID, ScheduleID        string                        `json:"-"`
	Unclaimed                   CopilotRerunTargetCancelInput `json:"-"`
	Identity                    taskqueue.JobInputIdentity    `json:"-"`
}

// LocateCopilotRerunCancellation 从当前主库读取有界精确身份，不加锁、不写入、不授权
func (r *Repositories) LocateCopilotRerunCancellation(ctx context.Context, pipelineID, runID string) (*CopilotRerunCancellationLocator, error) {
	if r == nil || r.database == nil || !historyID(pipelineID) || !historyID(runID) {
		return nil, ErrCopilotRerunInvalid
	}
	read := func() *gorm.DB { return r.database.Database().WithContext(ctx).Clauses(database.Write()) }
	var runs []model.PipelineRun
	if err := read().Select("run_id", "pipeline_id", "project_id", "copilot_rerun_intent_id", "copilot_rerun_gate_id").Where("run_id = ?", runID).Limit(2).Find(&runs).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(runs) != 1 || runs[0].RunID != runID || runs[0].PipelineID != pipelineID {
		return nil, ErrCopilotRerunForbidden
	}
	run := runs[0]
	result := &CopilotRerunCancellationLocator{PipelineID: pipelineID, RunID: runID}
	if emptyRunRerun(&run) {
		var protected []model.JobRun
		if err := read().Select("job_run_id").Where("pipeline_run_id = ? AND (copilot_rerun_intent_id <> '' OR copilot_rerun_gate_id <> '' OR expected_source_commit <> '')", runID).Limit(1).Find(&protected).Error; err != nil {
			return nil, rerunError(err)
		}
		if len(protected) != 0 {
			return nil, ErrCopilotRerunConflict
		}
		return result, ctx.Err()
	}
	if !rerunUniqueUUIDs(run.RunID, run.CopilotRerunIntentID, run.CopilotRerunGateID) || !historyID(run.ProjectID) {
		return nil, ErrCopilotRerunConflict
	}
	var jobs []model.JobRun
	if err := read().Select("job_run_id", "pipeline_run_id", "pipeline_id", "agent_id", "claim_id", "runtime_binding_digest", "source_binding_digest", "copilot_rerun_intent_id", "copilot_rerun_gate_id", "expected_source_commit").Where("pipeline_run_id = ?", runID).Limit(2).Find(&jobs).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(jobs) != 1 {
		return nil, ErrCopilotRerunConflict
	}
	job := jobs[0]
	if job.PipelineRunID != runID || job.PipelineID != pipelineID || job.CopilotRerunIntentID != run.CopilotRerunIntentID || job.CopilotRerunGateID != run.CopilotRerunGateID || !rerunUniqueUUIDs(job.JobRunID, runID, job.CopilotRerunIntentID, job.CopilotRerunGateID) {
		return nil, ErrCopilotRerunConflict
	}
	var intents []model.CopilotRerunIntent
	if err := read().Select("intent_id", "owner_user_id", "project_id", "pipeline_id", "new_run_id", "new_job_run_id", "gate_id", "schedule_id", "state", "version", "intent_digest").Where("intent_id = ?", job.CopilotRerunIntentID).Limit(2).Find(&intents).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(intents) != 1 {
		return nil, ErrCopilotRerunConflict
	}
	intent := intents[0]
	if intent.IntentID != job.CopilotRerunIntentID || intent.ProjectID != run.ProjectID || intent.PipelineID != pipelineID || intent.NewRunID != runID || intent.NewJobRunID != job.JobRunID || intent.GateID != job.CopilotRerunGateID || intent.State != CopilotRerunConfirmed || intent.Version == 0 || !historyID(intent.OwnerUserID) {
		return nil, ErrCopilotRerunConflict
	}
	var gates []model.CopilotRerunAdmissionGate
	if err := read().Select("gate_id", "schedule_id", "project_id", "pipeline_id", "run_id", "job_run_id", "agent_id", "claim_id", "state", "version").Where("gate_id = ?", intent.GateID).Limit(2).Find(&gates).Error; err != nil {
		return nil, rerunError(err)
	}
	var schedules []model.CopilotRerunSchedule
	if err := read().Select("schedule_id", "intent_id", "project_id", "pipeline_id", "run_id", "job_run_id", "owner_user_id", "agent_id", "claimed_id", "state", "version", "cancel_requested").Where("schedule_id = ?", intent.ScheduleID).Limit(2).Find(&schedules).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(gates) != 1 || len(schedules) != 1 {
		return nil, ErrCopilotRerunConflict
	}
	gate, schedule := gates[0], schedules[0]
	if gate.GateID != intent.GateID || gate.ScheduleID != intent.ScheduleID || gate.ProjectID != intent.ProjectID || gate.PipelineID != pipelineID || gate.RunID != runID || gate.JobRunID != job.JobRunID || gate.Version == 0 || schedule.ScheduleID != intent.ScheduleID || schedule.IntentID != intent.IntentID || schedule.ProjectID != intent.ProjectID || schedule.PipelineID != pipelineID || schedule.RunID != runID || schedule.JobRunID != job.JobRunID || schedule.OwnerUserID != intent.OwnerUserID || schedule.Version == 0 {
		return nil, ErrCopilotRerunConflict
	}
	result.Protected, result.JobRunID = true, job.JobRunID
	result.IntentID, result.ScheduleID = intent.IntentID, intent.ScheduleID
	if job.ClaimID != "" {
		result.Claimed = true
		result.Identity = taskqueue.JobInputIdentity{PipelineID: pipelineID, PipelineRunID: runID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
		if result.Identity.Validate() != nil || gate.State != "claimed" || gate.ClaimID != job.ClaimID || schedule.ClaimedID != job.ClaimID || gate.AgentID != job.AgentID || schedule.AgentID != job.AgentID || (schedule.State != CopilotRerunScheduleClaimed && schedule.State != CopilotRerunScheduleCancelled && schedule.State != CopilotRerunScheduleCompleted) {
			return nil, ErrCopilotRerunConflict
		}
	} else {
		if gate.ClaimID != "" || schedule.ClaimedID != "" || (gate.State != "unclaimed" && gate.State != "closed") {
			return nil, ErrCopilotRerunConflict
		}
		result.Unclaimed = CopilotRerunTargetCancelInput{OwnerUserID: intent.OwnerUserID, ProjectID: intent.ProjectID, IntentID: intent.IntentID, ExpectedIntentDigest: intent.IntentDigest, ExpectedIntentVersion: intent.Version, ExpectedGateVersion: gate.Version, ExpectedScheduleVersion: schedule.Version}
		if gate.State == "closed" {
			if schedule.State != CopilotRerunScheduleCancelled || !schedule.CancelRequested || gate.Version <= 1 || schedule.Version <= 1 {
				return nil, ErrCopilotRerunConflict
			}
			// Historical replay proves the exact prior CAS; it cannot cancel again.
			result.Unclaimed.ExpectedGateVersion--
			result.Unclaimed.ExpectedScheduleVersion--
		}
	}
	return result, ctx.Err()
}
