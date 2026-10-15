// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"strings"
)

func authorizeRetryActor(ctx context.Context, repositories *repo.Repositories, actorID, projectID, pipelineID, approvalAction string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, value := range []string{actorID, projectID, pipelineID} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
			return repo.ErrApprovalForbidden
		}
	}
	if repositories == nil || repositories.User == nil || repositories.TeamMember == nil || repositories.Team == nil || repositories.Project == nil || repositories.Pipeline == nil {
		return repo.ErrApprovalForbidden
	}
	actors, ok := repositories.User.(repo.IExecutionActorReader)
	if !ok {
		return repo.ErrApprovalForbidden
	}
	// 与登记输入校验共享 Pipeline → Project → User 锁顺序
	pipeline, err := repositories.Pipeline.Get(ctx, pipelineID)
	if err != nil || pipeline == nil || pipeline.PipelineID != pipelineID || pipeline.ProjectID != projectID || pipeline.IsEnabled != 1 {
		return repo.ErrApprovalForbidden
	}
	project, err := repositories.Project.Get(ctx, projectID)
	if err != nil || project == nil || project.ProjectID != projectID || project.IsEnabled != 1 || project.Status != model.ProjectStatusActive {
		return repo.ErrApprovalForbidden
	}
	user, err := actors.GetExecutionActor(ctx, actorID)
	if err != nil || user == nil || user.UserID != actorID || user.IsEnabled != 1 {
		return repo.ErrApprovalForbidden
	}
	memberships, err := repositories.TeamMember.ListUserTeams(ctx, actorID)
	if err != nil || len(memberships) > 1000 {
		return repo.ErrApprovalForbidden
	}
	ids := make([]string, 0, len(memberships))
	seen := map[string]bool{}
	for _, member := range memberships {
		if member.UserID != actorID || member.TeamID == "" {
			return repo.ErrApprovalForbidden
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
			return repo.ErrApprovalForbidden
		}
		for _, team := range teams {
			if team == nil || !seen[team.TeamID] {
				return repo.ErrApprovalForbidden
			}
			if team.IsEnabled == 1 {
				subject.TeamIDs = append(subject.TeamIDs, team.TeamID)
			}
		}
	}
	authorizer, err := authz.NewLiveAuthorizer(ctx, repositories)
	if err != nil {
		return repo.ErrApprovalForbidden
	}
	projectScope := authz.ResourceRef{Type: "project", ID: projectID, ProjectID: projectID, OrgID: project.OrgID}
	projectPermissions := []string{"project:read"}
	if approvalAction != "" {
		projectPermissions = []string{"project:read", approvalAction}
	}
	for _, permission := range projectPermissions {
		allowed, err := authorizer.Check(ctx, subject, permission, projectScope)
		if err != nil || !allowed {
			return repo.ErrApprovalForbidden
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
			return repo.ErrApprovalForbidden
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
