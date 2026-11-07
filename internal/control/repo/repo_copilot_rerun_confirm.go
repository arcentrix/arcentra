// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Rerun confirmation creates inert records only. There is deliberately no
// enqueue, Agent selection, first claim, input pin or independent approval here.
func (r *Repositories) CopilotRerunConfirm(ctx context.Context, in CopilotRerunConfirmInput, authorize CopilotRerunAuthorize, review CopilotRerunReviewBuilder) (*model.CopilotRerunIntent, error) {
	if review == nil || in.ExpectedVersion == 0 || in.ExpectedVersion >= math.MaxInt64 {
		return nil, ErrCopilotRerunInvalid
	}
	return r.copilotRerunIntentOperation(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, "confirm", authorize, func(tx *gorm.DB, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, saved *model.CopilotRerunIntent, ids *CopilotRerunExecutionIdentities, evidence *CopilotRerunOriginalEvidence) error {
		if saved.IntentDigest != in.ExpectedIntentDigest || saved.ReviewedRunContextDigest != in.ExpectedReviewedRunContextDigest {
			return ErrCopilotRerunConflict
		}
		if saved.State == CopilotRerunConfirmed {
			if saved.Version != in.ExpectedVersion+1 {
				return ErrCopilotRerunConflict
			}
			return inspectCopilotRerunTuple(ctx, tx, saved, ids, snapshot, evidence)
		}
		if saved.State != CopilotRerunPrepared || saved.Version != in.ExpectedVersion {
			return ErrCopilotRerunConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		if !now.Before(saved.ExpiresAt) || !now.Before(snapshot.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		view, current := NewScopedAuthorizationView(tx)
		fixed, _ := json.Marshal(ids)
		reviewed, err := review(ctx, view, scope, saved.OwnerUserID, snapshot, evidence, ids)
		if err != nil {
			return err
		}
		after, _ := json.Marshal(ids)
		if string(fixed) != string(after) {
			return ErrCopilotRerunConflict
		}
		if err := validateCopilotRerunReviewedTarget(reviewed, snapshot, evidence, ids, saved.OwnerUserID); err != nil {
			return err
		}
		if reviewed.RunContext != saved.ReviewedRunContext || reviewed.RunContextDigest != saved.ReviewedRunContextDigest || reviewed.AdmissionPolicyDigest != saved.ReviewedAdmissionPolicyDigest || reviewed.ExpectedSourceCommit != saved.ExpectedSourceCommit {
			return ErrCopilotRerunConflict
		}
		if err := insertCopilotRerunTuple(ctx, tx, saved, ids, snapshot, evidence, reviewed.Layout, now); err != nil {
			return err
		}
		result := tx.Model(&model.CopilotRerunIntent{}).Where("id = ? AND intent_id = ? AND version = ? AND state = ? AND intent_digest = ?", saved.ID, saved.IntentID, saved.Version, CopilotRerunPrepared, saved.IntentDigest).Updates(map[string]any{"state": CopilotRerunConfirmed, "version": saved.Version + 1, "confirmed_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCopilotRerunConflict
		}
		if !time.Now().Before(saved.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		if err := current(ctx); err != nil {
			return err
		}
		if !time.Now().Before(saved.ExpiresAt) || !time.Now().Before(snapshot.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		return nil
	})
}

// CancelPrepared cannot withdraw a confirmed submission. Target cancellation
// needs its own gate-first adapter and is intentionally unavailable here.
func (r *Repositories) CopilotRerunCancelPrepared(ctx context.Context, in CopilotRerunConfirmInput, authorize CopilotRerunAuthorize) (*model.CopilotRerunIntent, error) {
	if in.ExpectedVersion == 0 || in.ExpectedVersion >= math.MaxInt64 {
		return nil, ErrCopilotRerunInvalid
	}
	return r.copilotRerunIntentOperation(ctx, in.OwnerUserID, in.ProjectID, in.IntentID, "cancel", authorize, func(tx *gorm.DB, _ *CopilotRerunScope, _ *model.CopilotDefinitionSnapshot, saved *model.CopilotRerunIntent, _ *CopilotRerunExecutionIdentities, _ *CopilotRerunOriginalEvidence) error {
		if saved.IntentDigest != in.ExpectedIntentDigest || saved.ReviewedRunContextDigest != in.ExpectedReviewedRunContextDigest {
			return ErrCopilotRerunConflict
		}
		if saved.State == CopilotRerunCancelled && saved.Version == in.ExpectedVersion+1 {
			return nil
		}
		if saved.State != CopilotRerunPrepared || saved.Version != in.ExpectedVersion {
			return ErrCopilotRerunConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		result := tx.Model(&model.CopilotRerunIntent{}).Where("id = ? AND intent_id = ? AND version = ? AND state = ? AND intent_digest = ?", saved.ID, saved.IntentID, saved.Version, CopilotRerunPrepared, saved.IntentDigest).Updates(map[string]any{"state": CopilotRerunCancelled, "version": saved.Version + 1, "cancelled_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCopilotRerunConflict
		}
		return nil
	})
}

// Inspection uses the primary, retains current authorization, and verifies the
// fixed durable tuple. It grants no execution authority and never retries a
// mutation after an uncertain commit or constructs replacement identities.
func (r *Repositories) CopilotRerunInspect(ctx context.Context, owner, project, intent string, authorize CopilotRerunAuthorize) (*model.CopilotRerunIntent, error) {
	return r.copilotRerunIntentOperation(ctx, owner, project, intent, "inspect", authorize, func(tx *gorm.DB, _ *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, saved *model.CopilotRerunIntent, ids *CopilotRerunExecutionIdentities, evidence *CopilotRerunOriginalEvidence) error {
		if saved.State == CopilotRerunConfirmed {
			return inspectCopilotRerunTuple(ctx, tx, saved, ids, snapshot, evidence)
		}
		if saved.State != CopilotRerunPrepared && saved.State != CopilotRerunCancelled {
			return ErrCopilotRerunConflict
		}
		return inspectAbsentCopilotRerunTuple(ctx, tx, ids)
	})
}

type rerunIntentOperation func(*gorm.DB, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *model.CopilotRerunIntent, *CopilotRerunExecutionIdentities, *CopilotRerunOriginalEvidence) error

func (r *Repositories) copilotRerunIntentOperation(ctx context.Context, owner, project, intent, action string, authorize CopilotRerunAuthorize, operation rerunIntentOperation) (*model.CopilotRerunIntent, error) {
	if r == nil || r.database == nil || !historyID(owner) || !historyID(project) || !rerunUUID(intent) || authorize == nil {
		return nil, ErrCopilotRerunInvalid
	}
	db := r.database.Database().Clauses(database.Write())
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrCopilotRerunInvalid
	}
	var hints []model.CopilotRerunIntent
	if err := db.WithContext(ctx).Where("intent_id = ? AND owner_user_id = ? AND project_id = ?", intent, owner, project).Limit(2).Find(&hints).Error; err != nil {
		return nil, rerunError(err)
	}
	if len(hints) != 1 || hints[0].IntentID != intent || hints[0].OwnerUserID != owner || hints[0].ProjectID != project {
		return nil, ErrCopilotRerunForbidden
	}
	hint := hints[0]
	var result *model.CopilotRerunIntent
	err := executionAttachmentTransaction(ctx, db, hint.OriginalJobRunID, func(tx *gorm.DB) error {
		scope, current, err := lockCopilotRerunSource(ctx, tx, hint.OriginalJobRunID, project, owner, action, authorize)
		if err != nil {
			return err
		}
		var snapshots []model.CopilotDefinitionSnapshot
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id = ?", scope.Run.RunID).Limit(2).Find(&snapshots).Error; err != nil {
			return err
		}
		if len(snapshots) != 1 {
			return ErrCopilotRerunUnsupported
		}
		snapshot := &snapshots[0]
		var rows []model.CopilotRerunIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("intent_id = ?", intent).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 {
			return ErrCopilotRerunConflict
		}
		saved := &rows[0]
		if saved.IntentID != intent || saved.OwnerUserID != owner || saved.ProjectID != project || saved.OriginalJobRunID != hint.OriginalJobRunID || saved.OriginalRunID != scope.Run.RunID || saved.PipelineID != scope.Pipeline.PipelineID || saved.SnapshotID != snapshot.SnapshotID || saved.SnapshotDigest != snapshot.BindingDigest || saved.IntentDigest != rerunIntentDigest(saved) || saved.Version == 0 {
			return ErrCopilotRerunConflict
		}
		evidence, err := BuildCopilotRerunOriginalEvidence(snapshot, scope.Run, scope.Job, scope.JobCount, saved.CreatedAt)
		if err != nil {
			return ErrCopilotRerunUnsupported
		}
		digest, err := evidence.Digest()
		if err != nil {
			return err
		}
		body, _ := json.Marshal(evidence)
		if digest != saved.OriginalEvidenceDigest || string(body) != saved.OriginalEvidenceJSON {
			return ErrCopilotRerunConflict
		}
		layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
		if err != nil {
			return err
		}
		var ids CopilotRerunExecutionIdentities
		if json.Unmarshal([]byte(saved.StepIdentitiesJSON), &ids) != nil || ids.Validate(layout) != nil || ids.RunID != saved.NewRunID || ids.JobRunID != saved.NewJobRunID || ids.ScheduleID != saved.ScheduleID || ids.GateID != saved.GateID {
			return ErrCopilotRerunConflict
		}
		if err := validateCopilotRerunReviewedTarget(&CopilotRerunReviewedTarget{RunContext: saved.ReviewedRunContext, RunContextDigest: saved.ReviewedRunContextDigest, AdmissionPolicyDigest: saved.ReviewedAdmissionPolicyDigest, ExpectedSourceCommit: saved.ExpectedSourceCommit, Layout: layout}, snapshot, evidence, &ids, owner); err != nil {
			return err
		}
		if err := operation(tx, scope, snapshot, saved, &ids, evidence); err != nil {
			return err
		}
		// Reload the stored timestamp precision, including on the first successful call.
		var receipt model.CopilotRerunIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", saved.ID).First(&receipt).Error; err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		if action == "confirm" && saved.State == CopilotRerunPrepared && (!time.Now().Before(saved.ExpiresAt) || !time.Now().Before(snapshot.ExpiresAt)) {
			return ErrCopilotRerunExpired
		}
		result = &receipt
		return nil
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return result, nil
}

func insertCopilotRerunTuple(ctx context.Context, tx *gorm.DB, intent *model.CopilotRerunIntent, ids *CopilotRerunExecutionIdentities, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, layout *CopilotRerunLayout, now time.Time) error {
	if err := requireRerunAttachmentTransaction(tx); err != nil {
		return rerunInsertError(err)
	}
	source, err := sourceprovenance.NewBinding(intent.PipelineID, ids.RunID, ids.JobRunID, evidence.Source.Type, evidence.Source.Repository, evidence.Source.Ref)
	if err != nil {
		return rerunInsertError(err)
	}
	run := &model.PipelineRun{CopilotRerunIntentID: intent.IntentID, CopilotRerunGateID: ids.GateID, RunID: ids.RunID, PipelineID: intent.PipelineID, ProjectID: intent.ProjectID, RequestID: "copilot-rerun:" + intent.IntentID, Status: model.PipelineStatusPending, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: intent.OwnerUserID, InboxActorID: intent.OwnerUserID, InboxOrigin: model.RunInboxOriginVerifiedManual, RetryApprovalContext: intent.ReviewedRunContext, RetryApprovalContextDigest: intent.ReviewedRunContextDigest, DefinitionCommitSha: snapshot.DefinitionRevision, DefinitionPath: snapshot.DefinitionPath, TotalJobs: 1, Env: "{}"}
	job := &model.JobRun{CopilotRerunIntentID: intent.IntentID, CopilotRerunGateID: ids.GateID, ExpectedSourceCommit: intent.ExpectedSourceCommit, JobRunID: ids.JobRunID, PipelineID: intent.PipelineID, PipelineRunID: ids.RunID, JobName: layout.JobName, Status: model.JobRunStatusPending, RetryApprovalRequired: true, TotalSteps: len(layout.Steps), RuntimeType: layout.Runtime.Type, RuntimeImage: layout.Runtime.Image, RuntimeCPU: layout.Runtime.Resources.CPU, RuntimeMemory: layout.Runtime.Resources.Memory, SourceExpectationCaptured: true, SourceType: source.Type, SourceRepository: source.Repository, SourceRef: source.Ref, SourceBindingDigest: source.Digest, SourceProvenanceState: "unknown", InputManifestState: "unknown", Env: "{}", ArtifactReferences: "{}", ArtifactURIs: "{}"}
	policy, err := json.Marshal(layout.Retry)
	if err != nil {
		return rerunInsertError(err)
	}
	if err := tx.WithContext(ctx).Create(run).Error; err != nil {
		return rerunInsertError(err)
	}
	// Parent key is exclusively held before current child-range reads. Generic
	// attachment uses the same parent-first scoped RR boundary.
	if err := requireEmptyCopilotRerunRunJobs(ctx, tx, ids.RunID); err != nil {
		return rerunInsertError(err)
	}
	if err := tx.WithContext(ctx).Create(job).Error; err != nil {
		return rerunInsertError(err)
	}
	if err := requireEmptyCopilotRerunRunSteps(ctx, tx, ids.RunID); err != nil {
		return err
	}
	// The Job key also serializes legacy steps with a missing/different Run link.
	if err := requireEmptyCopilotRerunJobSteps(ctx, tx, ids.JobRunID, true); err != nil {
		return err
	}
	for i, logical := range layout.Steps {
		previous := ""
		for attempt, id := range ids.Steps[i].Attempts {
			step := &model.StepRun{StepRunID: id, Name: logical.Name, PipelineID: intent.PipelineID, PipelineRunID: ids.RunID, JobRunID: ids.JobRunID, Attempt: attempt + 1, RetryPolicy: string(policy), RetryMode: "whole_job", PreviousStepRunID: previous, StepIndex: int(logical.Index), Uses: logical.Uses, Action: logical.Action, Status: model.JobRunStatusPending, Args: "{}", Env: "{}", Secrets: "{}", LabelSelector: "{}", CreatedBy: intent.OwnerUserID}
			if err := tx.WithContext(ctx).Create(step).Error; err != nil {
				return rerunInsertError(err)
			}
			previous = id
		}
	}
	gate := &model.CopilotRerunAdmissionGate{GateID: ids.GateID, ScheduleID: ids.ScheduleID, ProjectID: intent.ProjectID, PipelineID: intent.PipelineID, RunID: ids.RunID, JobRunID: ids.JobRunID, State: "unclaimed", Version: 1, CreatedAt: now}
	if err := tx.WithContext(ctx).Create(gate).Error; err != nil {
		return rerunInsertError(err)
	}
	schedule := &model.CopilotRerunSchedule{ScheduleID: ids.ScheduleID, IntentID: intent.IntentID, OwnerUserID: intent.OwnerUserID, ProjectID: intent.ProjectID, PipelineID: intent.PipelineID, RunID: ids.RunID, JobRunID: ids.JobRunID, State: CopilotRerunSchedulePending, Version: 1, CreatedAt: now, ExpiresAt: intent.ExpiresAt}
	if err := tx.WithContext(ctx).Create(schedule).Error; err != nil {
		return rerunInsertError(err)
	}
	return nil
}

func inspectCopilotRerunTuple(ctx context.Context, tx *gorm.DB, intent *model.CopilotRerunIntent, ids *CopilotRerunExecutionIdentities, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence) error {
	if intent.ConfirmedAt == nil || intent.CancelledAt != nil {
		return ErrCopilotRerunConflict
	}
	// Consistent primary reads only: never lock an existing target Gate beneath
	// source locks. Inspection cannot advance target state or acquire authority.
	var run model.PipelineRun
	var job model.JobRun
	var gate model.CopilotRerunAdmissionGate
	var schedule model.CopilotRerunSchedule
	for _, query := range []struct {
		field, id string
		row       any
	}{{"run_id", ids.RunID, &run}, {"job_run_id", ids.JobRunID, &job}, {"gate_id", ids.GateID, &gate}, {"schedule_id", ids.ScheduleID, &schedule}} {
		if err := tx.WithContext(ctx).Where(query.field+" = ?", query.id).First(query.row).Error; err != nil {
			return ErrCopilotRerunConflict
		}
	}
	if run.RunID != ids.RunID || run.PipelineID != intent.PipelineID || run.ProjectID != intent.ProjectID || run.CopilotRerunIntentID != intent.IntentID || run.CopilotRerunGateID != ids.GateID || run.TriggeredBy != intent.OwnerUserID || run.RetryApprovalContext != intent.ReviewedRunContext || run.RetryApprovalContextDigest != intent.ReviewedRunContextDigest || job.JobRunID != ids.JobRunID || job.PipelineRunID != ids.RunID || job.PipelineID != intent.PipelineID || job.CopilotRerunIntentID != intent.IntentID || job.CopilotRerunGateID != ids.GateID || job.ExpectedSourceCommit != intent.ExpectedSourceCommit || gate.GateID != ids.GateID || gate.RunID != ids.RunID || gate.JobRunID != ids.JobRunID || gate.ScheduleID != ids.ScheduleID || gate.ProjectID != intent.ProjectID || gate.PipelineID != intent.PipelineID || schedule.ScheduleID != ids.ScheduleID || schedule.IntentID != intent.IntentID || schedule.RunID != ids.RunID || schedule.JobRunID != ids.JobRunID || schedule.PipelineID != intent.PipelineID || schedule.ProjectID != intent.ProjectID || schedule.OwnerUserID != intent.OwnerUserID || schedule.ExpiresAt.UnixMilli() != intent.ExpiresAt.UnixMilli() {
		return ErrCopilotRerunConflict
	}
	layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
	if err != nil {
		return err
	}
	source, err := sourceprovenance.NewBinding(intent.PipelineID, ids.RunID, ids.JobRunID, evidence.Source.Type, evidence.Source.Repository, evidence.Source.Ref)
	if err != nil {
		return err
	}
	if run.TriggerType != int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL) || run.AllowDeployments || run.RequestID != "copilot-rerun:"+intent.IntentID || run.InboxActorID != intent.OwnerUserID || run.InboxOrigin != model.RunInboxOriginVerifiedManual || run.DefinitionCommitSha != snapshot.DefinitionRevision || run.DefinitionPath != snapshot.DefinitionPath || run.TotalJobs != 1 || job.JobName != layout.JobName || !job.RetryApprovalRequired || job.TotalSteps != len(layout.Steps) || job.RuntimeType != layout.Runtime.Type || job.RuntimeImage != layout.Runtime.Image || job.RuntimeCPU != layout.Runtime.Resources.CPU || job.RuntimeMemory != layout.Runtime.Resources.Memory || !job.SourceExpectationCaptured || job.SourceType != source.Type || job.SourceRepository != source.Repository || job.SourceRef != source.Ref || job.SourceBindingDigest != source.Digest {
		return ErrCopilotRerunConflict
	}
	var jobs []model.JobRun
	var steps []model.StepRun
	wantSteps := 0
	for _, step := range ids.Steps {
		wantSteps += len(step.Attempts)
	}
	if err := tx.Select("id").Where("pipeline_run_id = ?", ids.RunID).Limit(2).Find(&jobs).Error; err != nil {
		return err
	}
	if err := tx.Select("id").Where("pipeline_run_id = ? OR job_run_id = ?", ids.RunID, ids.JobRunID).Limit(wantSteps + 1).Find(&steps).Error; err != nil {
		return err
	}
	if len(jobs) != 1 || len(steps) != wantSteps {
		return ErrCopilotRerunConflict
	}
	policy, err := json.Marshal(layout.Retry)
	if err != nil {
		return err
	}
	for index, step := range ids.Steps {
		for attempt, id := range step.Attempts {
			var row model.StepRun
			if err := tx.WithContext(ctx).Where("step_run_id = ?", id).First(&row).Error; err != nil {
				return ErrCopilotRerunConflict
			}
			previous := ""
			if attempt > 0 {
				previous = step.Attempts[attempt-1]
			}
			if row.ContinueOnError != 0 || row.CreatedBy != intent.OwnerUserID || row.StepRunID != id || row.JobRunID != ids.JobRunID || row.PipelineRunID != ids.RunID || row.PipelineID != intent.PipelineID || row.Name != step.Name || row.StepIndex != int(step.Index) || row.Attempt != attempt+1 || row.PreviousStepRunID != previous || row.Uses != layout.Steps[index].Uses || row.Action != layout.Steps[index].Action || row.RetryMode != "whole_job" || !rerunJSONEqual(row.RetryPolicy, string(policy)) {
				return ErrCopilotRerunConflict
			}
		}
	}
	return nil
}

// Bounded existence probes use the indexed run link. Under RR each current
// FOR UPDATE range remains protected until the parent and children commit.
func requireEmptyCopilotRerunChildren(ctx context.Context, tx *gorm.DB, run string, lock bool) error {
	for _, row := range []any{&[]model.JobRun{}, &[]model.StepRun{}} {
		q := tx.WithContext(ctx).Where("pipeline_run_id = ?", run).Limit(1)
		if lock {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		result := q.Find(row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 0 {
			return ErrCopilotRerunConflict
		}
	}
	return nil
}
func requireEmptyCopilotRerunRunJobs(ctx context.Context, tx *gorm.DB, run string) error {
	var rows []model.JobRun
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("pipeline_run_id = ?", run).Limit(1).Find(&rows)
	if result.Error != nil {
		return result.Error
	}
	if len(rows) != 0 {
		return ErrCopilotRerunConflict
	}
	return nil
}
func requireEmptyCopilotRerunRunSteps(ctx context.Context, tx *gorm.DB, run string) error {
	var rows []model.StepRun
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("pipeline_run_id = ?", run).Limit(1).Find(&rows)
	if result.Error != nil {
		return result.Error
	}
	if len(rows) != 0 {
		return ErrCopilotRerunConflict
	}
	return nil
}

func inspectAbsentCopilotRerunTuple(ctx context.Context, tx *gorm.DB, ids *CopilotRerunExecutionIdentities) error {
	for _, probe := range []struct {
		row       any
		field, id string
	}{
		{&model.PipelineRun{}, "run_id", ids.RunID}, {&model.JobRun{}, "job_run_id", ids.JobRunID},
		{&model.CopilotRerunAdmissionGate{}, "gate_id", ids.GateID}, {&model.CopilotRerunSchedule{}, "schedule_id", ids.ScheduleID},
	} {
		var n int64
		if err := tx.WithContext(ctx).Model(probe.row).Where(probe.field+" = ?", probe.id).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return ErrCopilotRerunConflict
		}
	}
	for _, step := range ids.Steps {
		var n int64
		if err := tx.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id IN ?", step.Attempts).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return ErrCopilotRerunConflict
		}
	}
	if err := requireEmptyCopilotRerunChildren(ctx, tx, ids.RunID, false); err != nil {
		return err
	}
	return requireEmptyCopilotRerunJobSteps(ctx, tx, ids.JobRunID, false)
}

func rerunInsertError(err error) error {
	var mysqlError *drivermysql.MySQLError
	if errors.Is(err, gorm.ErrDuplicatedKey) || errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return ErrCopilotRerunConflict
	}
	return err
}

func rerunJSONEqual(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	left, err := json.Marshal(x)
	if err != nil {
		return false
	}
	right, err := json.Marshal(y)
	return err == nil && string(left) == string(right)
}

func requireEmptyCopilotRerunJobSteps(ctx context.Context, tx *gorm.DB, job string, lock bool) error {
	var rows []model.StepRun
	q := tx.WithContext(ctx).Where("job_run_id = ?", job).Limit(1)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 0 {
		return ErrCopilotRerunConflict
	}
	return nil
}
