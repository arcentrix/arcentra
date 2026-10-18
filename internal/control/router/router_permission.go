// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package router

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

// Live checks make scoped member additions and revocations visible across control-plane instances.
// These request-bound reads do not replace transactional authorization in mutation repositories.
func (rt *Router) permission(action string, resolver middleware.ScopeResolver) fiber.Handler {
	return func(c *fiber.Ctx) error {
		live, err := rt.liveRequestAuthorization(c)
		if err != nil {
			return organizationError(c, err)
		}
		resource := authz.ResourceRef{}
		if resolver != nil {
			resource, err = resolver(c)
			if err != nil {
				return sensitiveError(c, 400, "resource scope unavailable")
			}
		}
		resource, err = live.resource(c.UserContext(), resource)
		if err != nil {
			return sensitiveError(c, 400, "resource scope unavailable")
		}
		allowed, err := live.checker.Check(c.UserContext(), live.subject, action, resource)
		if err != nil {
			return sensitiveError(c, 503, "authorization unavailable")
		}
		if !allowed {
			return sensitiveError(c, 403, "access denied")
		}
		return c.Next()
	}
}

type requestAuthorization struct {
	subject      authz.Subject
	checker      authz.IAuthorizer
	repositories *repo.Repositories
}

func (rt *Router) liveRequestAuthorization(c *fiber.Ctx) (*requestAuthorization, error) {
	if existing, ok := c.Locals("liveRequestAuthorization").(*requestAuthorization); ok {
		return existing, nil
	}
	claims, ok := c.Locals("claims").(*jwt.AuthClaims)
	if !ok || claims == nil || claims.UserID == "" {
		return nil, repo.ErrOrganizationForbidden
	}
	if rt.Services == nil || rt.Services.Authorizer == nil || rt.Services.Repos() == nil {
		return nil, errors.New("live authorization unavailable")
	}
	repositories, err := rt.Services.Repos().PollingPrimaryRepositories()
	if err != nil {
		return nil, err
	}
	subject, err := currentApprovalSubject(c.UserContext(), repositories, claims.UserID)
	if err != nil {
		return nil, err
	}
	checker, err := authz.NewLiveAuthorizer(c.UserContext(), repositories)
	if err != nil {
		return nil, err
	}
	live := &requestAuthorization{subject: subject, checker: checker, repositories: repositories}
	c.Locals("liveRequestAuthorization", live)
	return live, nil
}
func (live *requestAuthorization) resource(ctx context.Context, resource authz.ResourceRef) (authz.ResourceRef, error) {
	if resource.Type == "pipeline" && resource.ID != "" {
		pipeline, err := live.repositories.Pipeline.Get(ctx, resource.ID)
		if err != nil {
			return authz.ResourceRef{}, err
		}
		if pipeline == nil || pipeline.PipelineID != resource.ID {
			return authz.ResourceRef{}, errors.New("pipeline not found")
		}
		resource.ProjectID = pipeline.ProjectID
	}
	if resource.ProjectID != "" {
		project, err := live.repositories.AuthorizationProject(ctx, resource.ProjectID)
		if err != nil {
			return authz.ResourceRef{}, err
		}
		if project == nil || project.ProjectID != resource.ProjectID {
			return authz.ResourceRef{}, errors.New("project not found")
		}
		resource.OrgID = project.OrgID
	}
	return resource, nil
}

func (rt *Router) resolvePipelineScope(ctx context.Context, pipelineID string) (orgID, projectID string, err error) {
	if rt.Services == nil || rt.Services.PipelineRepo == nil {
		return "", "", errors.New("pipeline repository is not configured")
	}
	pipeline, err := rt.Services.PipelineRepo.Get(ctx, pipelineID)
	if err != nil {
		return "", "", err
	}
	if pipeline == nil {
		return "", "", errors.New("pipeline not found")
	}
	projectID = pipeline.ProjectID
	if projectID == "" {
		return "", "", errors.New("pipeline has no project scope")
	}
	if rt.Services.ProjectRepo == nil {
		return "", "", errors.New("project repository is not configured")
	}
	project, err := rt.Services.ProjectRepo.Get(ctx, projectID)
	if err != nil {
		return "", projectID, err
	}
	if project == nil {
		return "", "", errors.New("project not found")
	}
	return project.OrgID, projectID, nil
}

// ResolveProjectScopeByPipelineID 解析 pipeline 对应的作用域信息
func (rt *Router) ResolveProjectScopeByPipelineID(ctx context.Context, pipelineID string) (orgID, projectID string, err error) {
	return rt.resolvePipelineScope(ctx, pipelineID)
}

// ResolveProjectScopeByApprovalID 解析审批单对应的作用域信息
func (rt *Router) ResolveProjectScopeByApprovalID(ctx context.Context, approvalID string) (orgID, projectID string, err error) {
	if rt.Services == nil || rt.Services.Approval == nil {
		return "", "", errors.New("approval service is not configured")
	}
	approval, err := rt.Services.Approval.GetApproval(ctx, approvalID)
	if err != nil {
		return "", "", err
	}
	if approval == nil {
		return "", "", errors.New("approval not found")
	}
	runID := approval.PipelineRunID
	if runID == "" || rt.Services.PipelineRepo == nil {
		return "", "", errors.New("approval has no resolvable pipeline run")
	}
	run, err := rt.Services.PipelineRepo.GetRun(ctx, runID)
	if err != nil {
		return "", "", err
	}
	if run == nil {
		return "", "", errors.New("pipeline run not found")
	}
	return rt.resolvePipelineScope(ctx, run.PipelineID)
}
