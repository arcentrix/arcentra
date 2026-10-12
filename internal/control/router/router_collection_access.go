package router

import (
	"context"
	"errors"
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func collectionSubject(c *fiber.Ctx) (authz.Subject, bool) {
	if subject, ok := middleware.SubjectFromLocals(c); ok && strings.TrimSpace(subject.UserID) != "" {
		return subject, true
	}
	if claims, ok := c.Locals("claims").(*jwt.AuthClaims); ok && claims != nil && strings.TrimSpace(claims.UserID) != "" {
		return authz.Subject{UserID: claims.UserID}, true
	}
	return authz.Subject{}, false
}

// collectionAccess 仅建立鉴权前置条件，资源权限在列表分页之前逐项检查
func (rt *Router) collectionAccess() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if rt.Services == nil || rt.Services.Authorizer == nil {
			return http.Err(c, http.InternalError.Code, http.InternalError.Msg)
		}
		if _, ok := collectionSubject(c); !ok {
			return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
		}
		return c.Next()
	}
}

func (rt *Router) projectResource(ctx context.Context, projectID string) (authz.ResourceRef, error) {
	if rt.Services == nil || rt.Services.ProjectRepo == nil {
		return authz.ResourceRef{}, errors.New("project repository is unavailable")
	}
	project, err := rt.Services.ProjectRepo.Get(ctx, projectID)
	if err != nil {
		return authz.ResourceRef{}, err
	}
	if project == nil || project.ProjectID == "" {
		return authz.ResourceRef{}, errors.New("project not found")
	}
	return authz.ResourceRef{Type: "project", ID: project.ProjectID, ProjectID: project.ProjectID, OrgID: project.OrgID}, nil
}

func (rt *Router) resolveCreatePipelineScope() middleware.ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		var request struct {
			ProjectID string `json:"projectId"`
		}
		if err := c.BodyParser(&request); err != nil {
			return authz.ResourceRef{}, err
		}
		if strings.TrimSpace(request.ProjectID) == "" {
			return authz.ResourceRef{}, errors.New("project id is required")
		}
		return rt.projectResource(c.Context(), strings.TrimSpace(request.ProjectID))
	}
}

func (rt *Router) allowed(c *fiber.Ctx, action string, resource authz.ResourceRef) (bool, error) {
	if rt.Services == nil || rt.Services.Authorizer == nil {
		return false, errors.New("authorizer is unavailable")
	}
	subject, ok := collectionSubject(c)
	if !ok {
		return false, errors.New("authenticated subject is required")
	}
	return rt.Services.Authorizer.Check(c.Context(), subject, action, resource)
}

func normalizedCollectionPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func (rt *Router) authorizedPipelineList(c *fiber.Ctx, request *pipelinev1.ListPipelinesRequest) ([]*pipelinev1.PipelineDetail, int64, error) {
	// 显式 projectId 必须由数据库补齐组织范围，不能信任调用方传入的组织
	resource := authz.ResourceRef{}
	if request.ProjectId != "" {
		scope, err := rt.projectResource(c.Context(), request.ProjectId)
		if err != nil {
			return nil, 0, err
		}
		resource = scope
	}
	allAllowed, err := rt.allowed(c, "pipeline:read", resource)
	if err != nil {
		return nil, 0, err
	}
	if allAllowed {
		response, err := rt.pipelineService().ListPipelines(c.Context(), request)
		if err != nil {
			return nil, 0, err
		}
		if !response.Success {
			return nil, 0, errors.New(response.Message)
		}
		return response.Pipelines, int64(response.Total), nil
	}
	page, size := normalizedCollectionPage(int(request.Page), int(request.PageSize))
	offset := int64(page-1) * int64(size)
	out := make([]*pipelinev1.PipelineDetail, 0, size)
	var total int64
	projects := make(map[string]authz.ResourceRef)
	for scanPage := int32(1); ; scanPage++ {
		response, err := rt.pipelineService().ListPipelines(c.Context(), &pipelinev1.ListPipelinesRequest{ProjectId: request.ProjectId, Name: request.Name, Status: request.Status, Page: scanPage, PageSize: 100})
		if err != nil {
			return nil, 0, err
		}
		if !response.Success {
			return nil, 0, errors.New(response.Message)
		}
		for _, item := range response.Pipelines {
			scope, exists := projects[item.ProjectId]
			if !exists {
				scope, err = rt.projectResource(c.Context(), item.ProjectId)
				if err != nil {
					return nil, 0, err
				}
				projects[item.ProjectId] = scope
			}
			scope.Type, scope.ID = "pipeline", item.PipelineId
			allowed, err := rt.allowed(c, "pipeline:read", scope)
			if err != nil {
				return nil, 0, err
			}
			if !allowed {
				continue
			}
			if total >= offset && len(out) < size {
				out = append(out, item)
			}
			total++
		}
		if len(response.Pipelines) == 0 || int64(scanPage)*100 >= int64(response.Total) {
			break
		}
	}
	return out, total, nil
}

func (rt *Router) authorizedProjectList(c *fiber.Ctx, query *model.ProjectQueryReq) ([]*model.Project, int64, error) {
	if rt.Services == nil || rt.Services.Project == nil {
		return nil, 0, errors.New("project service is unavailable")
	}
	allAllowed, err := rt.allowed(c, "project:read", authz.ResourceRef{OrgID: query.OrgID})
	if err != nil {
		return nil, 0, err
	}
	if allAllowed {
		return rt.Services.Project.ListProjects(c.Context(), query)
	}
	page, size := normalizedCollectionPage(query.PageNum, query.PageSize)
	offset := int64(page-1) * int64(size)
	out := make([]*model.Project, 0, size)
	var total int64
	for scanPage := 1; ; scanPage++ {
		scanQuery := *query
		scanQuery.PageNum, scanQuery.PageSize = scanPage, 100
		projects, fullTotal, err := rt.Services.Project.ListProjects(c.Context(), &scanQuery)
		if err != nil {
			return nil, 0, err
		}
		for _, project := range projects {
			allowed, err := rt.allowed(c, "project:read", authz.ResourceRef{Type: "project", ID: project.ProjectID, ProjectID: project.ProjectID, OrgID: project.OrgID})
			if err != nil {
				return nil, 0, err
			}
			if !allowed {
				continue
			}
			if total >= offset && len(out) < size {
				out = append(out, project)
			}
			total++
		}
		if len(projects) == 0 || int64(scanPage)*100 >= fullTotal {
			break
		}
	}
	return out, total, nil
}
