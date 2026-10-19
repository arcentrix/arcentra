// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/polling"
	"gorm.io/gorm"
	"sort"
	"strings"
)

// ConfigurePollingAuthorization 为轮询变更与执行入场安装事务内实时授权器
func (s *Services) ConfigurePollingAuthorization() error {
	return repo.SetPollingAuthorizer(s.repos, func(ctx context.Context, view repo.PollingAuthorizationView, actorID, projectID string, pipelineIDs []string) error {
		if view == nil {
			return polling.ErrForbidden
		}
		return authorizePollingActor(ctx, view.LiveRepositories(), actorID, projectID, pipelineIDs)
	})
}

func pollingSubject(ctx context.Context, repositories *repo.Repositories, actorID string) (authz.Subject, error) {
	var subject authz.Subject
	if actorID == "" || actorID != strings.TrimSpace(actorID) || len(actorID) > 64 || repositories == nil || repositories.User == nil || repositories.TeamMember == nil || repositories.Team == nil {
		return subject, polling.ErrForbidden
	}
	reader, ok := repositories.User.(repo.IExecutionActorReader)
	if !ok {
		return subject, fmt.Errorf("current polling actor reader unavailable")
	}
	actor, err := reader.GetExecutionActor(ctx, actorID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return subject, polling.ErrForbidden
	}
	if err != nil {
		return subject, fmt.Errorf("read polling actor: %w", err)
	}
	if actor == nil || actor.UserID != actorID || actor.IsEnabled != 1 {
		return subject, polling.ErrForbidden
	}
	memberships, err := repositories.TeamMember.ListUserTeams(ctx, actorID)
	if err != nil {
		return subject, fmt.Errorf("read polling memberships: %w", err)
	}
	if len(memberships) > 1000 {
		return subject, polling.ErrForbidden
	}
	ids := make([]string, 0, len(memberships))
	seen := map[string]bool{}
	for _, member := range memberships {
		if member.UserID != actorID || member.TeamID == "" {
			return subject, polling.ErrForbidden
		}
		if !seen[member.TeamID] {
			ids = append(ids, member.TeamID)
			seen[member.TeamID] = true
		}
	}
	sort.Strings(ids)
	subject.UserID = actorID
	if len(ids) > 0 {
		teams, err := repositories.Team.BatchGet(ctx, ids)
		if err != nil {
			return authz.Subject{}, fmt.Errorf("read polling teams: %w", err)
		}
		for _, team := range teams {
			if team == nil || !seen[team.TeamID] {
				return authz.Subject{}, polling.ErrForbidden
			}
			if team.IsEnabled == 1 {
				subject.TeamIDs = append(subject.TeamIDs, team.TeamID)
			}
		}
	}
	return subject, ctx.Err()
}

// 调用者提供仓储事务视图时，所有主体与权限读取保持 current read 直至事务提交
func authorizePollingActor(ctx context.Context, repositories *repo.Repositories, actorID, projectID string, pipelineIDs []string) error {
	if repositories == nil || repositories.Project == nil || repositories.Pipeline == nil || projectID == "" || len(pipelineIDs) > polling.MaxPipelines {
		return polling.ErrForbidden
	}
	// 与仓储锁顺序一致，先排序 pipeline，再 project，再用户及授权目录
	ids := append([]string(nil), pipelineIDs...)
	sort.Strings(ids)
	for i, pipelineID := range ids {
		if pipelineID == "" || (i > 0 && pipelineID == ids[i-1]) {
			return polling.ErrForbidden
		}
		pipeline, err := repositories.Pipeline.Get(ctx, pipelineID)
		if err != nil {
			return fmt.Errorf("read polling pipeline scope: %w", err)
		}
		if pipeline == nil || pipeline.PipelineID != pipelineID || pipeline.ProjectID != projectID || pipeline.IsEnabled != 1 || pipeline.IsDeleted != 0 {
			return polling.ErrForbidden
		}
	}
	project, err := repositories.Project.Get(ctx, projectID)
	if err != nil {
		return fmt.Errorf("read polling project scope: %w", err)
	}
	if project == nil || project.ProjectID != projectID {
		return polling.ErrForbidden
	}
	// 禁用仍可由有权操作者完成；就绪条件由相应仓储变更及准入检查负责
	subject, err := pollingSubject(ctx, repositories, actorID)
	if err != nil {
		return err
	}
	authorizer, err := authz.NewLiveAuthorizer(ctx, repositories)
	if err != nil {
		return fmt.Errorf("read polling authorization: %w", err)
	}
	scope := authz.ResourceRef{Type: "project", ID: projectID, ProjectID: projectID, OrgID: project.OrgID}
	for _, action := range []string{"project:read", "project:update"} {
		allowed, err := authorizer.Check(ctx, subject, action, scope)
		if err != nil {
			return fmt.Errorf("check polling project authorization: %w", err)
		}
		if !allowed {
			return polling.ErrForbidden
		}
	}
	for _, pipelineID := range ids {
		scope := authz.ResourceRef{Type: "pipeline", ID: pipelineID, ProjectID: projectID, OrgID: project.OrgID}
		for _, action := range []string{"pipeline:read", "pipeline:trigger"} {
			allowed, err := authorizer.Check(ctx, subject, action, scope)
			if err != nil {
				return fmt.Errorf("check polling pipeline authorization: %w", err)
			}
			if !allowed {
				return polling.ErrForbidden
			}
		}
	}
	return ctx.Err()
}

func pollingProjectEligible(project *model.Project) bool {
	if project == nil || project.ProjectID == "" || project.IsEnabled != 1 || project.Status != model.ProjectStatusActive || project.TriggerMode&model.TriggerModeMR == 0 || (project.AuthType != model.AuthTypeToken && project.AuthType != model.AuthTypeNone) {
		return false
	}
	repository, err := polling.CanonicalRepository(project.RepoURL)
	if err != nil {
		return false
	}
	_, _, _, err = polling.ProviderRepository(project.RepoType, repository)
	return err == nil
}
