// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrManualTriggerConflict  = errors.New("requestId belongs to another manual run identity")
	ErrManualTriggerForbidden = errors.New("verified manual trigger unavailable")
)

// VerifiedManualTriggerAuthorize checks current trigger permission using only
// the transaction-bound view and already locked exact scope. It must not look
// up a pipeline after a project lock, or use an externally cached authorizer.
type VerifiedManualTriggerAuthorize func(context.Context, *Repositories, *model.Project, *model.Pipeline, string) error

// IVerifiedManualTriggerCreator is separate from generic/internal creation.
// Callers cannot grant personal recipient authority through CreateRun.
type IVerifiedManualTriggerCreator interface {
	CreateVerifiedManualTriggerRun(context.Context, string, *model.PipelineRun, VerifiedManualTriggerAuthorize) error
}

// IInboxActorReader rejects both exact duplicate users and case aliases even
// on binary-collated installations. It reads identity metadata only.
type IInboxActorReader interface {
	GetInboxActor(context.Context, string) (*model.User, error)
}

type IInboxMembershipReader interface {
	GetInboxMemberships(context.Context, string) ([]model.TeamMember, error)
}

type IInboxTeamReader interface {
	GetInboxTeams(context.Context, []string) ([]*model.Team, error)
}

func (r *retryApprovalUsers) GetInboxActor(ctx context.Context, id string) (*model.User, error) {
	var rows []model.User
	if err := r.view.query(ctx).Select("user_id", "is_enabled").Where("LOWER(user_id) = LOWER(?)", id).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].UserID != id || rows[0].IsEnabled != 1 {
		return nil, ErrManualTriggerForbidden
	}
	return &rows[0], nil
}

func (r *retryApprovalMembers) GetInboxMemberships(ctx context.Context, id string) ([]model.TeamMember, error) {
	var rows []model.TeamMember
	err := r.view.query(ctx).Select("team_id", "user_id").Where("LOWER(user_id) = LOWER(?)", id).Order("team_id ASC").Limit(1001).Find(&rows).Error
	return rows, err
}

func (r *retryApprovalTeams) GetInboxTeams(ctx context.Context, ids []string) ([]*model.Team, error) {
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, ErrManualTriggerForbidden
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	// Fold both sides in the database, including on binary installations.
	// The extra row makes duplicate/case-alias identities fail closed without
	// allowing an unbounded metadata result set.
	placeholders := strings.TrimSuffix(strings.Repeat("LOWER(?),", len(ids)), ",")
	var rows []*model.Team
	err := r.view.query(ctx).Select("team_id", "is_enabled").Where("LOWER(team_id) IN ("+placeholders+")", args...).Order("team_id ASC").Limit(len(ids) + 1).Find(&rows).Error
	return rows, err
}

func emptyRunInboxBinding(run *model.PipelineRun) bool {
	return run != nil && run.InboxActorID == "" && run.InboxOrigin == 0
}

func validRunInboxBinding(run *model.PipelineRun) bool {
	return emptyRunInboxBinding(run) || run != nil && run.InboxOrigin == model.RunInboxOriginVerifiedManual &&
		manualInboxID(run.InboxActorID, 64) && run.TriggerType == int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL) && run.TriggeredBy == run.InboxActorID
}

func manualInboxID(id string, limit int) bool {
	return id != "" && id == strings.TrimSpace(id) && len(id) <= limit && !strings.ContainsAny(id, "\x00\r\n")
}

func validateManualInboxCapture(run *model.PipelineRun, actor, projectID string) error {
	if run.RetryApprovalContext != "" || run.RetryApprovalContextDigest != "" {
		captured, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
		if err != nil {
			return ErrManualTriggerForbidden
		}
		digest, err := captured.Digest()
		if err != nil || digest != run.RetryApprovalContextDigest || captured.ActorID != actor || captured.ProjectID != projectID || captured.PipelineID != run.PipelineID || captured.PipelineRunID != run.RunID || captured.DefinitionCommitSHA != run.DefinitionCommitSha || captured.DefinitionPath != run.DefinitionPath {
			return ErrManualTriggerForbidden
		}
	}
	if run.AllowDeployments || run.DeploymentActorID != "" || run.DeploymentPlan != "" || run.DeploymentPlanHash != "" || run.DeploymentTargetFingerprint != "" {
		if !run.AllowDeployments || run.DeploymentActorID != actor || run.DeploymentTargetFingerprint == "" {
			return ErrManualTriggerForbidden
		}
		if _, err := deployment.ParsePipelinePlan(run.DeploymentPlan, run.DeploymentPlanHash); err != nil {
			return ErrManualTriggerForbidden
		}
	}
	return nil
}

// CreateVerifiedManualTriggerRun alone mints personal Inbox provenance. The
// lock order is pipeline -> project -> users/teams -> catalog/grants -> new run
// -> new work. Scoped membership/project mutations never acquire a pipeline
// after their project lock; retry admission uses the same authority suffix.
// On uncertain commit the caller must reconcile the stable request/run ID from
// the primary and never submit execution merely because an error was returned.
func (r *PipelineRepo) CreateVerifiedManualTriggerRun(ctx context.Context, actor string, run *model.PipelineRun, authorize VerifiedManualTriggerAuthorize) error {
	if run == nil || authorize == nil || !manualInboxID(actor, 64) || !emptyRunInboxBinding(run) ||
		!manualInboxID(run.RunID, 64) || !manualInboxID(run.PipelineID, 64) ||
		run.RequestID != "" && !manualInboxID(run.RequestID, 128) || run.ID != 0 ||
		run.TriggerType != int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL) || run.TriggeredBy != actor ||
		run.Status != model.PipelineStatusPending || run.ProjectID != "" || run.StartTime != nil || run.EndTime != nil ||
		run.PollingContext != "" || run.PollingContextDigest != "" || run.TriggeredBy == "polling" {
		return ErrManualTriggerForbidden
	}
	stored := *run
	if err := normalizeJSONFields(map[string]*string{"env": &stored.Env}, pipelineRunJSONDefaults); err != nil {
		return err
	}
	err := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		var pipelines []model.Pipeline
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("pipeline_id", "project_id", "is_enabled", "is_deleted").Where("LOWER(pipeline_id) = LOWER(?)", stored.PipelineID).Limit(2).Find(&pipelines).Error; err != nil {
			return err
		}
		if len(pipelines) != 1 || pipelines[0].PipelineID != stored.PipelineID || pipelines[0].IsEnabled != 1 || pipelines[0].IsDeleted != 0 || !manualInboxID(pipelines[0].ProjectID, 64) {
			return ErrManualTriggerForbidden
		}
		pipeline := &pipelines[0]
		var projects []model.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id", "org_id", "is_enabled", "status").Where("LOWER(project_id) = LOWER(?)", pipeline.ProjectID).Limit(2).Find(&projects).Error; err != nil {
			return err
		}
		if len(projects) != 1 || projects[0].ProjectID != pipeline.ProjectID || projects[0].IsEnabled != 1 || projects[0].Status != model.ProjectStatusActive {
			return ErrManualTriggerForbidden
		}
		if err := validateManualInboxCapture(&stored, actor, pipeline.ProjectID); err != nil {
			return err
		}
		view, current := NewScopedAuthorizationView(tx)
		// Actor existence/enabled/exact uniqueness are repository invariants,
		// even if a caller supplied an incomplete authorization callback.
		user, err := view.User.(IInboxActorReader).GetInboxActor(ctx, actor)
		if err != nil || user == nil || user.UserID != actor || user.IsEnabled != 1 {
			return ErrManualTriggerForbidden
		}
		if err := authorize(ctx, view, &projects[0], pipeline, actor); err != nil {
			return ErrManualTriggerForbidden
		}
		// A binary database must not turn case aliases into separate stable
		// business identities. Request lookup stays bounded to the locked
		// pipeline; run ID uniqueness is global. Current locks also serialize
		// a competing insert instead of trusting a replica/old snapshot.
		var existing []model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id").Where("LOWER(run_id) = LOWER(?)", stored.RunID).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) != 0 {
			return ErrManualTriggerConflict
		}
		if stored.RequestID != "" {
			existing = nil
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id").Where("pipeline_id = ? AND LOWER(request_id) = LOWER(?)", stored.PipelineID, stored.RequestID).Limit(1).Find(&existing).Error; err != nil {
				return err
			}
			if len(existing) != 0 {
				return ErrManualTriggerConflict
			}
		}
		stored.InboxActorID, stored.InboxOrigin = actor, model.RunInboxOriginVerifiedManual
		if err := tx.Create(&stored).Error; err != nil {
			return err
		}
		if err := enrollInboxWork(tx, "run", stored.RunID); err != nil {
			return err
		}
		return current(ctx)
	})
	if err == nil {
		*run = stored
	}
	return err
}
