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

	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) permission(action string, resolver middleware.ScopeResolver) fiber.Handler {
	if rt.Services == nil || rt.Services.Authorizer == nil {
		return middleware.RequirePermission(nil, action, resolver)
	}
	return middleware.RequirePermission(rt.Services.Authorizer, action, resolver)
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
