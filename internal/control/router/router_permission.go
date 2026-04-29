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

	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) permission(action string, resolver middleware.ScopeResolver) fiber.Handler {
	if rt.Services == nil || rt.Services.Authorizer == nil {
		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}
	return middleware.RequirePermission(rt.Services.Authorizer, action, resolver)
}

func (rt *Router) resolvePipelineScope(ctx context.Context, pipelineID string) (orgID, projectID string, err error) {
	if rt.Services == nil || rt.Services.PipelineRepo == nil {
		return "", "", nil
	}
	pipeline, err := rt.Services.PipelineRepo.Get(ctx, pipelineID)
	if err != nil {
		return "", "", err
	}
	projectID = pipeline.ProjectID
	if projectID == "" || rt.Services.ProjectRepo == nil {
		return "", projectID, nil
	}
	project, err := rt.Services.ProjectRepo.Get(ctx, projectID)
	if err != nil {
		return "", projectID, err
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
		return "", "", nil
	}
	approval, err := rt.Services.Approval.GetApproval(ctx, approvalID)
	if err != nil {
		return "", "", err
	}
	runID := approval.PipelineRunID
	if runID == "" || rt.Services.PipelineRepo == nil {
		return "", "", nil
	}
	run, err := rt.Services.PipelineRepo.GetRun(ctx, runID)
	if err != nil {
		return "", "", err
	}
	return rt.resolvePipelineScope(ctx, run.PipelineID)
}
