// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

// liveProject 刻意不使用输入中的团队或缓存鉴权器，也不复制请求锁和取消状态
func (s *CopilotService) liveProject(ctx context.Context, subject authz.Subject, projectID string, primary func(context.Context) (*repo.Repositories, error)) (*CopilotService, *model.Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if subject.IsAgent || !copilot.ValidID(subject.UserID) || !copilot.ValidID(projectID) {
		return nil, nil, ErrCopilotDenied
	}
	if primary == nil {
		return nil, nil, ErrCopilotContext
	}
	repositories, err := primary(ctx)
	if err != nil || repositories == nil || repositories.User == nil || repositories.TeamMember == nil || repositories.Team == nil {
		return nil, nil, ErrCopilotContext
	}
	actors, ok := repositories.User.(repo.IExecutionActorReader)
	if !ok {
		return nil, nil, ErrCopilotContext
	}
	user, err := actors.GetExecutionActor(ctx, subject.UserID)
	if err != nil || user == nil || user.UserID != subject.UserID || user.IsEnabled != 1 {
		return nil, nil, ErrCopilotDenied
	}
	members, err := repositories.TeamMember.ListUserTeams(ctx, subject.UserID)
	if err != nil || len(members) > 1000 {
		return nil, nil, ErrCopilotDenied
	}
	liveSubject := authz.Subject{UserID: subject.UserID}
	ids, seen := []string{}, map[string]bool{}
	for _, member := range members {
		if member.UserID != subject.UserID || !copilot.ValidID(member.TeamID) {
			return nil, nil, ErrCopilotDenied
		}
		if !seen[member.TeamID] {
			ids = append(ids, member.TeamID)
			seen[member.TeamID] = true
		}
	}
	if len(ids) > 0 {
		teams, err := repositories.Team.BatchGet(ctx, ids)
		if err != nil {
			return nil, nil, ErrCopilotDenied
		}
		returned := map[string]bool{}
		for _, team := range teams {
			if team == nil || !seen[team.TeamID] || returned[team.TeamID] {
				return nil, nil, ErrCopilotDenied
			}
			returned[team.TeamID] = true
			if team.IsEnabled == 1 {
				liveSubject.TeamIDs = append(liveSubject.TeamIDs, team.TeamID)
			}
		}
	}
	authorizer, err := authz.NewLiveAuthorizer(ctx, repositories)
	if err != nil {
		return nil, nil, ErrCopilotDenied
	}
	local := &CopilotService{repos: repositories, authorizer: authorizer, registry: s.registry, secrets: s.secrets, sourceSubject: liveSubject}
	if s.artifacts != nil {
		local.artifacts = &ArtifactService{artifacts: repositories.Artifact, storageRepo: repositories.Storage, root: s.artifacts.root, newStorage: s.artifacts.newStorage}
	}
	project, err := local.project(ctx, liveSubject, projectID)
	if err != nil {
		return nil, nil, err
	}
	if project.IsEnabled != 1 || project.Status != model.ProjectStatusActive {
		return nil, nil, ErrCopilotDenied
	}
	return local, project, nil
}

// liveScope 为所有上下文读取提供当前主库身份、所属链及项目和流水线授权
func (s *CopilotService) liveScope(ctx context.Context, subject authz.Subject, projectID, runID string, primary func(context.Context) (*repo.Repositories, error)) (*CopilotService, *model.Project, *model.PipelineRun, error) {
	if !copilot.ValidID(runID) {
		return nil, nil, nil, ErrCopilotDenied
	}
	local, project, err := s.liveProject(ctx, subject, projectID, primary)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, ok := local.repos.Secret.(repo.ICopilotSecretValueReader); !ok {
		return nil, nil, nil, ErrCopilotSecrets
	}
	run, err := local.run(ctx, local.sourceSubject, project, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	pipeline, err := local.repos.Pipeline.Get(ctx, run.PipelineID)
	if err != nil || pipeline == nil || pipeline.PipelineID != run.PipelineID || pipeline.ProjectID != projectID || pipeline.IsEnabled != 1 || pipeline.IsDeleted != 0 {
		return nil, nil, nil, ErrCopilotDenied
	}
	return local, project, run, nil
}

// verifyCurrentContext 在内容返回及模型发送边界再次检查当前权限和原始所属链
func (s *CopilotService) verifyCurrentContext(ctx context.Context, subject authz.Subject, project *model.Project, run *model.PipelineRun) error {
	_, currentProject, currentRun, err := s.liveScope(ctx, subject, project.ProjectID, run.RunID, s.contextPrimary)
	if err != nil {
		return err
	}
	if currentProject.OrgID != project.OrgID || currentRun.PipelineID != run.PipelineID {
		return ErrCopilotDenied
	}
	return ctx.Err()
}

// historyAuthorize is invoked under the repository's primary authorization locks.
func historyAuthorize(ctx context.Context, repositories *repo.Repositories, owner string, project *model.Project, pipelineID string) error {
	if !copilot.ValidID(owner) || repositories == nil || repositories.User == nil || repositories.TeamMember == nil || repositories.Team == nil {
		return ErrCopilotDenied
	}
	actors, ok := repositories.User.(repo.IExecutionActorReader)
	if !ok {
		return ErrCopilotDenied
	}
	user, err := actors.GetExecutionActor(ctx, owner)
	if err != nil || user == nil || user.UserID != owner || user.IsEnabled != 1 {
		return ErrCopilotDenied
	}
	subject := authz.Subject{UserID: owner}
	members, err := repositories.TeamMember.ListUserTeams(ctx, owner)
	if err != nil || len(members) > 1000 {
		return ErrCopilotDenied
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range members {
		if m.UserID != owner || !copilot.ValidID(m.TeamID) {
			return ErrCopilotDenied
		}
		if !seen[m.TeamID] {
			seen[m.TeamID] = true
			ids = append(ids, m.TeamID)
		}
	}
	if len(ids) > 0 {
		teams, err := repositories.Team.BatchGet(ctx, ids)
		if err != nil {
			return ErrCopilotDenied
		}
		returned := map[string]bool{}
		for _, t := range teams {
			if t == nil || !seen[t.TeamID] || returned[t.TeamID] {
				return ErrCopilotDenied
			}
			returned[t.TeamID] = true
			if t.IsEnabled == 1 {
				subject.TeamIDs = append(subject.TeamIDs, t.TeamID)
			}
		}
	}
	if project == nil {
		return ctx.Err()
	}
	if project.IsEnabled != 1 || project.Status != model.ProjectStatusActive {
		return ErrCopilotDenied
	}
	a, err := authz.NewLiveAuthorizer(ctx, repositories)
	if err != nil {
		return ErrCopilotDenied
	}
	allowed, err := a.Check(ctx, subject, "project:read", authz.ResourceRef{Type: "project", ID: project.ProjectID, ProjectID: project.ProjectID, OrgID: project.OrgID})
	if err != nil || !allowed {
		return ErrCopilotDenied
	}
	if pipelineID != "" {
		allowed, err = a.Check(ctx, subject, "pipeline:read", authz.ResourceRef{Type: "pipeline", ID: pipelineID, ProjectID: project.ProjectID, OrgID: project.OrgID})
		if err != nil || !allowed {
			return ErrCopilotDenied
		}
	}
	return ctx.Err()
}
