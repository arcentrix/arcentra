// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCopilotRerunForbidden   = errors.New("whole-job re-execution access denied")
	ErrCopilotRerunConflict    = errors.New("whole-job re-execution state changed")
	ErrCopilotRerunExpired     = errors.New("whole-job re-execution intent expired")
	ErrCopilotRerunUnsupported = errors.New("original run lacks prospective re-execution evidence")
)

const CopilotRerunPrepared = "prepared"
const CopilotRerunConfirmed = "confirmed"
const CopilotRerunCancelled = "cancelled"
const CopilotRerunIntentTTL = 15 * time.Minute

// This view is used only inside the common locked domain prefix. Authorization
// callbacks must use these transaction-bound repositories; they must not start
// their own secret/authorization transaction. Preparation creates no authority.
type CopilotRerunScope struct {
	Project  *model.Project
	Pipeline *model.Pipeline
	Run      *model.PipelineRun
	Job      *model.JobRun
	JobCount int
}
type CopilotRerunAuthorize func(context.Context, *Repositories, *CopilotRerunScope, string, string) error

type CopilotRerunPrepareInput struct {
	OwnerUserID, ProjectID, OriginalJobRunID, RequestID, SnapshotDigest string
	ExpiresAt                                                           time.Time
}

func rerunError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrCopilotRerunInvalid, ErrCopilotRerunForbidden, ErrCopilotRerunConflict, ErrCopilotRerunExpired, ErrCopilotRerunUnsupported, ErrCopilotRerunUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	if errors.Is(err, ErrManualTriggerForbidden) || errors.Is(err, ErrApprovalForbidden) {
		return ErrCopilotRerunForbidden
	}
	if errors.Is(err, errExecutionParentMissing) || errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrCopilotRerunUnsupported
	}
	return ErrCopilotRerunUnavailable
}

func rerunIntentDigest(intent *model.CopilotRerunIntent) string {
	return rerunDigest("arcentra-copilot-rerun-intent-v2", []any{intent.IntentID, intent.OwnerUserID, intent.ProjectID, intent.RequestID, intent.PipelineID, intent.OriginalRunID, intent.OriginalJobRunID, intent.SnapshotID, intent.SnapshotDigest, intent.OriginalEvidenceJSON, intent.OriginalEvidenceDigest, intent.NewRunID, intent.NewJobRunID, intent.ScheduleID, intent.StepIdentitiesJSON, intent.GateID, intent.ReviewedRunContext, intent.ReviewedRunContextDigest, intent.ReviewedAdmissionPolicyDigest, intent.ExpectedSourceCommit, intent.CreatedAt.UnixMilli(), intent.ExpiresAt.UnixMilli()})
}

func lockCopilotRerunSource(ctx context.Context, tx *gorm.DB, jobID, projectID, owner, action string, authorize CopilotRerunAuthorize) (*CopilotRerunScope, func(context.Context) error, error) {
	if authorize == nil {
		return nil, nil, ErrCopilotRerunForbidden
	}
	job, err := lockExecutionJob(ctx, tx, jobID, executionLockActive)
	if err != nil {
		return nil, nil, err
	}
	if err := requireExecutionScope(tx, job); err != nil {
		return nil, nil, err
	}
	scope := &CopilotRerunScope{Job: job, Run: &model.PipelineRun{}, Pipeline: &model.Pipeline{}, Project: &model.Project{}}
	// All four rows are already held by the common prefix. These current reads
	// recover complete values without adding a reverse lock-order dependency.
	lock := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
	if err := lock().Where("job_run_id = ?", jobID).First(scope.Job).Error; err != nil {
		return nil, nil, err
	}
	if err := lock().Where("run_id = ?", job.PipelineRunID).First(scope.Run).Error; err != nil {
		return nil, nil, err
	}
	if err := lock().Where("pipeline_id = ?", job.PipelineID).First(scope.Pipeline).Error; err != nil {
		return nil, nil, err
	}
	if scope.Pipeline.ProjectID != projectID {
		return nil, nil, ErrCopilotRerunForbidden
	}
	if err := lock().Where("project_id = ?", projectID).First(scope.Project).Error; err != nil {
		return nil, nil, err
	}
	if scope.Job.JobRunID != jobID || scope.Run.RunID != job.PipelineRunID || scope.Run.PipelineID != job.PipelineID || scope.Pipeline.PipelineID != job.PipelineID || scope.Pipeline.ProjectID != projectID || scope.Project.ProjectID != projectID || scope.Run.ProjectID != projectID || scope.Pipeline.IsEnabled != 1 || scope.Pipeline.IsDeleted != 0 || scope.Project.IsEnabled != 1 || scope.Project.Status != model.ProjectStatusActive {
		return nil, nil, ErrCopilotRerunForbidden
	}
	if scope.Run.Status != model.PipelineStatusFailed || scope.Job.Status != model.JobRunStatusFailed || scope.Run.TriggerType != int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL) || scope.Run.InboxOrigin != model.RunInboxOriginVerifiedManual || scope.Run.InboxActorID != scope.Run.TriggeredBy || scope.Run.PollingContext != "" || scope.Run.PollingContextDigest != "" || scope.Run.AllowDeployments {
		return nil, nil, ErrCopilotRerunUnsupported
	}
	var jobs []model.JobRun
	if err := lock().Select("job_run_id").Where("pipeline_run_id = ?", scope.Run.RunID).Limit(2).Find(&jobs).Error; err != nil {
		return nil, nil, err
	}
	if len(jobs) != 1 || jobs[0].JobRunID != jobID {
		return nil, nil, ErrCopilotRerunUnsupported
	}
	scope.JobCount = len(jobs)
	view, current := NewScopedAuthorizationView(tx)
	actor, err := view.User.(IInboxActorReader).GetInboxActor(ctx, owner)
	if err != nil || actor == nil || actor.UserID != owner || actor.IsEnabled != 1 {
		return nil, nil, ErrCopilotRerunForbidden
	}
	if err := authorize(ctx, view, scope, owner, action); err != nil {
		return nil, nil, ErrCopilotRerunForbidden
	}
	return scope, current, nil
}

func (repositories *Repositories) CopilotRerunPrepare(ctx context.Context, in CopilotRerunPrepareInput, authorize CopilotRerunAuthorize, review CopilotRerunReviewBuilder) (*model.CopilotRerunIntent, error) {
	if review == nil || repositories == nil || repositories.database == nil || !historyID(in.OwnerUserID) || !historyID(in.ProjectID) || !historyID(in.OriginalJobRunID) || !historyID(in.RequestID) || !sourceprovenance.ValidBindingDigest(in.SnapshotDigest) || in.ExpiresAt.IsZero() {
		return nil, ErrCopilotRerunInvalid
	}
	in.ExpiresAt = in.ExpiresAt.UTC().Truncate(time.Millisecond)
	var result *model.CopilotRerunIntent
	err := executionTransaction(ctx, repositories.database.Database(), in.OriginalJobRunID, func(tx *gorm.DB) error {
		scope, current, err := lockCopilotRerunSource(ctx, tx, in.OriginalJobRunID, in.ProjectID, in.OwnerUserID, "prepare", authorize)
		if err != nil {
			return err
		}
		var snapshots []model.CopilotDefinitionSnapshot
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id = ?", scope.Run.RunID).Limit(2).Find(&snapshots).Error; err != nil {
			return err
		}
		if len(snapshots) != 1 {
			return ErrCopilotRerunUnsupported
		}
		snapshot := &snapshots[0]
		if snapshot.BindingDigest != in.SnapshotDigest {
			return ErrCopilotRerunConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
		if err != nil {
			return err
		}
		var existing []model.CopilotRerunIntent
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ? AND project_id = ? AND request_id = ?", in.OwnerUserID, in.ProjectID, in.RequestID).Limit(2).Find(&existing).Error; err != nil {
			return err
		}
		// An exact persisted request is a historical receipt, including after its
		// expiry. Current owner/scope authorization above still applies. Validate
		// its terminal evidence at the original preparation time; this neither
		// renews the deadline nor grants confirmation or execution authority.
		evidenceTime := now
		if len(existing) == 1 {
			evidenceTime = existing[0].CreatedAt
		} else if !now.Before(in.ExpiresAt) || in.ExpiresAt.After(now.Add(CopilotRerunIntentTTL)) || in.ExpiresAt.After(snapshot.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		evidence, err := BuildCopilotRerunOriginalEvidence(snapshot, scope.Run, scope.Job, scope.JobCount, evidenceTime)
		if err != nil {
			return ErrCopilotRerunUnsupported
		}
		evidenceDigest, err := evidence.Digest()
		if err != nil {
			return err
		}
		evidenceJSON, err := json.Marshal(evidence)
		if err != nil {
			return ErrCopilotRerunInvalid
		}
		if len(existing) > 0 {
			if len(existing) != 1 {
				return ErrCopilotRerunConflict
			}
			saved := &existing[0]
			if saved.OwnerUserID != in.OwnerUserID || saved.ProjectID != in.ProjectID || saved.RequestID != in.RequestID || saved.OriginalJobRunID != in.OriginalJobRunID || saved.OriginalRunID != scope.Run.RunID || saved.PipelineID != scope.Pipeline.PipelineID || saved.SnapshotID != snapshot.SnapshotID || saved.OriginalEvidenceJSON != string(evidenceJSON) || saved.SnapshotDigest != in.SnapshotDigest || saved.OriginalEvidenceDigest != evidenceDigest || saved.ExpiresAt.UnixMilli() != in.ExpiresAt.UnixMilli() || saved.IntentDigest != rerunIntentDigest(saved) || saved.Version == 0 {
				return ErrCopilotRerunConflict
			}
			var identities CopilotRerunExecutionIdentities
			if !rerunUUID(saved.IntentID) || json.Unmarshal([]byte(saved.StepIdentitiesJSON), &identities) != nil || identities.Validate(layout) != nil || identities.RunID != saved.NewRunID || identities.JobRunID != saved.NewJobRunID || identities.ScheduleID != saved.ScheduleID || identities.GateID != saved.GateID || (saved.State != CopilotRerunPrepared && saved.State != CopilotRerunConfirmed && saved.State != CopilotRerunCancelled) {
				return ErrCopilotRerunConflict
			}
			if err := validateCopilotRerunReviewedTarget(&CopilotRerunReviewedTarget{RunContext: saved.ReviewedRunContext, RunContextDigest: saved.ReviewedRunContextDigest, AdmissionPolicyDigest: saved.ReviewedAdmissionPolicyDigest, ExpectedSourceCommit: saved.ExpectedSourceCommit, Layout: layout}, snapshot, evidence, &identities, in.OwnerUserID); err != nil {
				return ErrCopilotRerunUnsupported
			}
			result = saved
			return current(ctx)
		}
		var count int64
		if err := tx.WithContext(ctx).Model(&model.CopilotRerunIntent{}).Where("owner_user_id = ? AND project_id = ?", in.OwnerUserID, in.ProjectID).Count(&count).Error; err != nil {
			return err
		}
		if count >= 1000 {
			return ErrCopilotRerunConflict
		}
		ids, err := NewCopilotRerunExecutionIdentities(layout)
		if err != nil {
			return err
		}
		intentID := uuid.NewString()
		newIDs := []string{intentID, ids.RunID, ids.JobRunID, ids.ScheduleID, ids.GateID}
		for _, step := range ids.Steps {
			newIDs = append(newIDs, step.Attempts...)
		}
		if !rerunUniqueUUIDs(newIDs...) {
			return ErrCopilotRerunConflict
		}
		originals := []string{scope.Run.RunID, scope.Job.JobRunID, snapshot.SnapshotID, in.OwnerUserID, in.ProjectID, scope.Pipeline.PipelineID}
		originalPlan, err := taskqueue.ParseJobRetryPlan(scope.Job.RetryPlan)
		if err != nil {
			return ErrCopilotRerunUnsupported
		}
		for _, step := range originalPlan.Steps {
			originals = append(originals, step.AttemptRunIDs...)
		}
		for _, fresh := range newIDs {
			for _, original := range originals {
				if strings.EqualFold(fresh, original) {
					return ErrCopilotRerunConflict
				}
			}
		}
		fixedIdentities, err := json.Marshal(ids)
		if err != nil {
			return ErrCopilotRerunInvalid
		}
		view, reviewCurrent := NewScopedAuthorizationView(tx)
		reviewed, err := review(ctx, view, scope, in.OwnerUserID, snapshot, evidence, ids)
		if err != nil {
			return err
		}
		if err := validateCopilotRerunReviewedTarget(reviewed, snapshot, evidence, ids, in.OwnerUserID); err != nil {
			return err
		}
		encoded, err := json.Marshal(ids)
		if string(encoded) != string(fixedIdentities) {
			return ErrCopilotRerunConflict
		}
		if err != nil {
			return ErrCopilotRerunInvalid
		}
		result = &model.CopilotRerunIntent{IntentID: intentID, OwnerUserID: in.OwnerUserID, ProjectID: in.ProjectID, RequestID: in.RequestID, PipelineID: scope.Pipeline.PipelineID, OriginalRunID: scope.Run.RunID, OriginalJobRunID: scope.Job.JobRunID, SnapshotID: snapshot.SnapshotID, SnapshotDigest: snapshot.BindingDigest, OriginalEvidenceJSON: string(evidenceJSON), OriginalEvidenceDigest: evidenceDigest, NewRunID: ids.RunID, NewJobRunID: ids.JobRunID, ScheduleID: ids.ScheduleID, StepIdentitiesJSON: string(encoded), GateID: ids.GateID, ReviewedRunContext: reviewed.RunContext, ReviewedRunContextDigest: reviewed.RunContextDigest, ReviewedAdmissionPolicyDigest: reviewed.AdmissionPolicyDigest, ExpectedSourceCommit: reviewed.ExpectedSourceCommit, State: CopilotRerunPrepared, Version: 1, CreatedAt: now, ExpiresAt: in.ExpiresAt}
		result.IntentDigest = rerunIntentDigest(result)
		if err := tx.Create(result).Error; err != nil {
			return err
		}
		if !time.Now().Before(in.ExpiresAt) {
			return ErrCopilotRerunExpired
		}
		if err := reviewCurrent(ctx); err != nil {
			return err
		}
		return current(ctx)
	})
	if err != nil {
		return nil, rerunError(err)
	}
	return result, nil
}
