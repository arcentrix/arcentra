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

package middleware

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

// ScopeResolver 用于解析当前请求对应资源作用域
type ScopeResolver func(c *fiber.Ctx) (authz.ResourceRef, error)

// ResourceScopeLookup 定义作用域解析依赖接口
type ResourceScopeLookup interface {
	ResolveProjectScopeByPipelineID(ctx context.Context, pipelineID string) (orgID, projectID string, err error)
	ResolveProjectScopeByApprovalID(ctx context.Context, approvalID string) (orgID, projectID string, err error)
}

// RequirePermission 对请求执行动作权限校验
func RequirePermission(checker authz.IAuthorizer, action string, resolver ScopeResolver) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if checker == nil {
			return http.Err(c, http.InternalError.Code, http.InternalError.Msg)
		}
		subject, ok := SubjectFromLocals(c)
		if !ok {
			claimsValue := c.Locals("claims")
			claims, claimOK := claimsValue.(*jwt.AuthClaims)
			if !claimOK || claims == nil || claims.UserID == "" {
				return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
			}
			subject = authz.Subject{UserID: claims.UserID}
		}

		resource := authz.ResourceRef{}
		if resolver != nil {
			var err error
			resource, err = resolver(c)
			if err != nil {
				return http.Err(c, http.BadRequest.Code, http.BadRequest.Msg)
			}
		}

		allowed, err := checker.Check(c.Context(), subject, action, resource)
		if err != nil {
			log.Errorw("permission check failed", "action", action, "error", err)
			return http.Err(c, http.InternalError.Code, http.InternalError.Msg)
		}
		if !allowed {
			return http.Err(c, http.Forbidden.Code, http.Forbidden.Msg)
		}
		return c.Next()
	}
}

// ResolvePlatformScope 返回平台级解析器
func ResolvePlatformScope() ScopeResolver {
	return func(_ *fiber.Ctx) (authz.ResourceRef, error) {
		return authz.ResourceRef{}, nil
	}
}

// ResolveFromPathProjectID 返回按路径 projectID 解析的解析器
func ResolveFromPathProjectID(pathParam string) ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		projectID := c.Params(pathParam)
		if projectID == "" {
			return authz.ResourceRef{}, errors.New("project id is empty")
		}
		return authz.ResourceRef{
			ProjectID: projectID,
		}, nil
	}
}

// ResolveFromQueryProjectID 返回按 query projectId 解析的解析器
func ResolveFromQueryProjectID(queryKey string) ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		projectID := c.Query(queryKey)
		if projectID == "" {
			return authz.ResourceRef{}, errors.New("project id is empty")
		}
		return authz.ResourceRef{ProjectID: projectID}, nil
	}
}

// ResolveFromQueryOrgID 返回按 query orgId 解析的解析器
func ResolveFromQueryOrgID(queryKey string) ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		orgID := c.Query(queryKey)
		if orgID == "" {
			return authz.ResourceRef{}, errors.New("org id is empty")
		}
		return authz.ResourceRef{OrgID: orgID}, nil
	}
}

// ResolveFromPathPipelineID 返回按路径 pipelineID 解析的解析器
func ResolveFromPathPipelineID(pathParam string, lookup ResourceScopeLookup) ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		pipelineID := c.Params(pathParam)
		if pipelineID == "" {
			return authz.ResourceRef{}, errors.New("pipeline id is empty")
		}
		if lookup == nil {
			return authz.ResourceRef{}, errors.New("scope lookup is nil")
		}
		orgID, projectID, err := lookup.ResolveProjectScopeByPipelineID(c.Context(), pipelineID)
		if err != nil {
			return authz.ResourceRef{}, err
		}
		return authz.ResourceRef{
			Type:      string(consts.ScopeTypePipeline),
			ID:        pipelineID,
			ProjectID: projectID,
			OrgID:     orgID,
		}, nil
	}
}

// ResolveFromApprovalID 返回按路径 approvalID 解析的解析器
func ResolveFromApprovalID(pathParam string, lookup ResourceScopeLookup) ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		approvalID := c.Params(pathParam)
		if approvalID == "" {
			return authz.ResourceRef{}, errors.New("approval id is empty")
		}
		if lookup == nil {
			return authz.ResourceRef{}, errors.New("scope lookup is nil")
		}
		orgID, projectID, err := lookup.ResolveProjectScopeByApprovalID(c.Context(), approvalID)
		if err != nil {
			return authz.ResourceRef{}, err
		}
		return authz.ResourceRef{
			Type:      "approval",
			ID:        approvalID,
			ProjectID: projectID,
			OrgID:     orgID,
		}, nil
	}
}
