// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

// ErrDeploymentAuthorization 隐藏鉴权存储细节，并在身份或权限不可用时拒绝部署。
var ErrDeploymentAuthorization = errors.New("deployment authorization denied or unavailable")

// NewPipelineDeploymentAuthorizer 每次读取当前用户、团队和有效授权，拒绝过期的执行权限。
func NewPipelineDeploymentAuthorizer(repositories *repo.Repositories, deployments *DeploymentService) PipelineDeploymentAuthorizer {
	return newDeploymentAuthorizer(repositories, deployments, "")
}

// NewDeploymentApprovalAuthorizer 每次复核候选审批人的身份、项目读取、审批和发布等级权限
func NewDeploymentApprovalAuthorizer(repositories *repo.Repositories, deployments *DeploymentService) PipelineDeploymentAuthorizer {
	return newDeploymentAuthorizer(repositories, deployments, "approval:approve")
}

// NewDeploymentRejectionAuthorizer 每次复核候选审批人的拒绝权限，不隐含同意权限
func NewDeploymentRejectionAuthorizer(repositories *repo.Repositories, deployments *DeploymentService) PipelineDeploymentAuthorizer {
	return newDeploymentAuthorizer(repositories, deployments, "approval:reject")
}

func newDeploymentAuthorizer(repositories *repo.Repositories, deployments *DeploymentService, approvalAction string) PipelineDeploymentAuthorizer {
	return func(ctx context.Context, actorID, projectID, pipelineID, environmentID string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, value := range []string{actorID, projectID, pipelineID, environmentID} {
			if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
				return ErrDeploymentAuthorization
			}
		}
		if repositories == nil || repositories.User == nil || repositories.TeamMember == nil || repositories.Team == nil || repositories.Project == nil || repositories.Pipeline == nil || deployments == nil {
			return ErrDeploymentAuthorization
		}
		actors, ok := repositories.User.(repo.IExecutionActorReader)
		if !ok {
			return ErrDeploymentAuthorization
		}
		user, err := actors.GetExecutionActor(ctx, actorID)
		if err != nil || user == nil || user.UserID != actorID || user.IsEnabled != 1 {
			return ErrDeploymentAuthorization
		}
		project, err := repositories.Project.Get(ctx, projectID)
		if err != nil || project == nil || project.ProjectID != projectID || project.IsEnabled != 1 || project.Status != model.ProjectStatusActive {
			return ErrDeploymentAuthorization
		}
		pipeline, err := repositories.Pipeline.Get(ctx, pipelineID)
		if err != nil || pipeline == nil || pipeline.PipelineID != pipelineID || pipeline.ProjectID != projectID || pipeline.IsEnabled != 1 {
			return ErrDeploymentAuthorization
		}
		memberships, err := repositories.TeamMember.ListUserTeams(ctx, actorID)
		if err != nil || len(memberships) > 1000 {
			return ErrDeploymentAuthorization
		}
		ids := make([]string, 0, len(memberships))
		seen := map[string]bool{}
		for _, member := range memberships {
			if member.UserID != actorID || member.TeamID == "" {
				return ErrDeploymentAuthorization
			}
			if !seen[member.TeamID] {
				ids = append(ids, member.TeamID)
				seen[member.TeamID] = true
			}
		}
		subject := authz.Subject{UserID: actorID}
		if len(ids) > 0 {
			teams, err := repositories.Team.BatchGet(ctx, ids)
			if err != nil {
				return ErrDeploymentAuthorization
			}
			for _, team := range teams {
				if team == nil || !seen[team.TeamID] {
					return ErrDeploymentAuthorization
				}
				if team.IsEnabled == 1 {
					subject.TeamIDs = append(subject.TeamIDs, team.TeamID)
				}
			}
		}
		action, err := deployments.DeploymentAction(ctx, projectID, environmentID)
		if err != nil || (action != "release:deploy_dev" && action != "release:deploy_staging" && action != "release:deploy_prod") {
			return ErrDeploymentAuthorization
		}
		authorizer, err := authz.NewLiveAuthorizer(ctx, repositories)
		if err != nil {
			return ErrDeploymentAuthorization
		}
		projectScope := authz.ResourceRef{Type: "project", ID: projectID, ProjectID: projectID, OrgID: project.OrgID}
		projectPermissions := []string{"project:read", "project:update", action}
		if approvalAction != "" {
			projectPermissions = []string{"project:read", approvalAction, action}
		}
		for _, permission := range projectPermissions {
			allowed, err := authorizer.Check(ctx, subject, permission, projectScope)
			if err != nil || !allowed {
				return ErrDeploymentAuthorization
			}
		}
		pipelineScope := authz.ResourceRef{Type: "pipeline", ID: pipelineID, ProjectID: projectID, OrgID: project.OrgID}
		pipelinePermissions := []string{"pipeline:read", "pipeline:trigger"}
		if approvalAction != "" {
			pipelinePermissions = []string{"pipeline:read"}
		}
		for _, permission := range pipelinePermissions {
			allowed, err := authorizer.Check(ctx, subject, permission, pipelineScope)
			if err != nil || !allowed {
				return ErrDeploymentAuthorization
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}
