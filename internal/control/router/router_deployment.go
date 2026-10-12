// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package router

import (
	"errors"
	"strconv"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

func (rt *Router) deploymentRouter(r fiber.Router, authMW, subjectMW fiber.Handler) {
	group := r.Group("/projects/:projectID", deploymentHTTPStatus, authMW, subjectMW, rt.deploymentAccess())
	group.Get("/environments", rt.listDeploymentEnvironments)
	group.Post("/environments", rt.createDeploymentEnvironment)
	group.Get("/releases", rt.listDeploymentReleases)
	group.Post("/releases", rt.createDeploymentRelease)
	group.Get("/releases/:releaseID", rt.getDeploymentRelease)
	group.Post("/releases/:releaseID/advance", rt.advanceDeploymentRelease)
	group.Get("/releases/:releaseID/inspect", rt.inspectDeploymentRelease)
	group.Post("/releases/:releaseID/recover", rt.recoverDeploymentRelease)
	group.Post("/releases/:releaseID/cancel", rt.cancelDeploymentRelease)
}
func deploymentHTTPStatus(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	err := c.Next()
	if err == nil && c.Response().StatusCode() == 200 && c.Locals("deploymentResponse") == nil {
		c.Status(401)
	}
	return err
}
func deploymentError(c *fiber.Ctx, status int, message string) error {
	c.Locals("deploymentResponse", true)
	c.Status(status)
	return http.Err(c, status, message)
}
func deploymentResult(c *fiber.Ctx, value any) error {
	c.Locals("deploymentResponse", true)
	return http.Detail(c, value)
}
func (rt *Router) deploymentAccess() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if rt.Services == nil || rt.Services.Deployment == nil || rt.Services.Authorizer == nil {
			return deploymentError(c, 503, "deployment service is unavailable")
		}
		if _, ok := collectionSubject(c); !ok {
			return deploymentError(c, 401, "authentication is required")
		}
		resource, err := rt.projectResource(c.UserContext(), c.Params("projectID"))
		if err != nil {
			return deploymentError(c, 404, "project not found")
		}
		allowed, err := rt.allowed(c, "project:read", resource)
		if err != nil {
			return deploymentError(c, 503, "unable to authorize project")
		}
		if !allowed {
			return deploymentError(c, 403, "project access denied")
		}
		c.Locals("deploymentScope", resource)
		return c.Next()
	}
}
func (rt *Router) deploymentAllowed(c *fiber.Ctx, pipelineID, action string) (bool, error) {
	scope, ok := c.Locals("deploymentScope").(authz.ResourceRef)
	if !ok {
		return false, errors.New("deployment scope unavailable")
	}
	if pipelineID != "" {
		scope.Type = "pipeline"
		scope.ID = pipelineID
	}
	return rt.allowed(c, action, scope)
}
func (rt *Router) requireDeploymentWrite(c *fiber.Ctx, pipelineID string) error {
	allowed, err := rt.deploymentAllowed(c, "", "project:update")
	if err != nil {
		return deploymentError(c, 503, "unable to authorize deployment")
	}
	if !allowed {
		return deploymentError(c, 403, "project update permission is required")
	}
	if pipelineID != "" {
		// 不依赖用户提交的流水线 ID 构造跨项目权限范围
		if rt.Services.PipelineRepo == nil {
			return deploymentError(c, 503, "pipeline service is unavailable")
		}
		pipeline, err := rt.Services.PipelineRepo.Get(c.UserContext(), pipelineID)
		if err != nil || pipeline == nil || pipeline.ProjectID != c.Params("projectID") {
			return deploymentError(c, 404, "pipeline not found in project")
		}
		for _, action := range []string{"pipeline:read", "pipeline:trigger"} {
			allowed, err = rt.deploymentAllowed(c, pipelineID, action)
			if err != nil {
				return deploymentError(c, 503, "unable to authorize pipeline")
			}
			if !allowed {
				return deploymentError(c, 403, "pipeline read and trigger permissions are required")
			}
		}
	}
	return nil
}
func (rt *Router) requireDeploymentTier(c *fiber.Ctx, environmentID string) error {
	action, err := rt.Services.Deployment.DeploymentAction(c.UserContext(), c.Params("projectID"), environmentID)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	allowed, err := rt.deploymentAllowed(c, "", action)
	if err != nil {
		return deploymentError(c, 503, "unable to authorize release target")
	}
	if !allowed {
		return deploymentError(c, 403, "release target deployment permission is required")
	}
	return nil
}
func deploymentPage(c *fiber.Ctx) (int, int, error) {
	page, size := 1, 20
	var err error
	if value := c.Query("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 1000000 {
			return 0, 0, service.ErrDeploymentInput
		}
	}
	if value := c.Query("pageSize"); value != "" {
		size, err = strconv.Atoi(value)
		if err != nil || size < 1 || size > 100 {
			return 0, 0, service.ErrDeploymentInput
		}
	}
	return page, size, nil
}
func deploymentServiceError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, service.ErrDeploymentNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		return deploymentError(c, 404, "deployment record not found")
	case errors.Is(err, service.ErrPipelineDeploymentDenied):
		return deploymentError(c, 403, "current deployment authorization was denied")
	case errors.Is(err, service.ErrPipelineDeploymentSource):
		return deploymentError(c, 422, err.Error())
	case errors.Is(err, service.ErrPipelineDeploymentUncertain), errors.Is(err, service.ErrPipelineDeploymentStopped), errors.Is(err, service.ErrPipelineDeploymentFailed):
		return deploymentError(c, 409, err.Error())
	case errors.Is(err, service.ErrDeploymentInput):
		return deploymentError(c, 400, err.Error())
	case errors.Is(err, service.ErrDeploymentSource):
		return deploymentError(c, 422, err.Error())
	case errors.Is(err, service.ErrDeploymentApproval), errors.Is(err, service.ErrDeploymentTargetChanged), errors.Is(err, repo.ErrDeploymentConflict), errors.Is(err, service.ErrDeploymentCancel), errors.Is(err, cluster.ErrConflict), errors.Is(err, cluster.ErrInvalidTarget), errors.Is(err, cluster.ErrSuperseded):
		return deploymentError(c, 409, err.Error())
	case errors.Is(err, cluster.ErrUnconfigured):
		return deploymentError(c, 503, "deployment target is not configured for this exact project and workload")
	default:
		return deploymentError(c, 503, "deployment operation is unavailable; no successful rollout has been inferred")
	}
}
func (rt *Router) listDeploymentEnvironments(c *fiber.Ctx) error {
	page, size, err := deploymentPage(c)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	result, err := rt.Services.Deployment.Environments(c.UserContext(), c.Params("projectID"), page, size)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	return deploymentResult(c, result)
}
func (rt *Router) createDeploymentEnvironment(c *fiber.Ctx) error {
	if err := rt.requireDeploymentWrite(c, ""); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	var input service.DeploymentEnvironmentInput
	if len(c.Body()) > 4096 || sonic.Unmarshal(c.Body(), &input) != nil {
		return deploymentError(c, 400, "invalid environment request")
	}
	result, err := rt.Services.Deployment.CreateEnvironment(c.UserContext(), c.Params("projectID"), input)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	return deploymentResult(c, result)
}
func (rt *Router) listDeploymentReleases(c *fiber.Ctx) error {
	page, size, err := deploymentPage(c)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	result, err := rt.Services.Deployment.Releases(c.UserContext(), c.Params("projectID"), page, size, func(pipelineID string) (bool, error) { return rt.deploymentAllowed(c, pipelineID, "pipeline:read") })
	if err != nil {
		return deploymentServiceError(c, err)
	}
	return deploymentResult(c, result)
}
func (rt *Router) createDeploymentRelease(c *fiber.Ctx) error {
	var input service.DeploymentReleaseInput
	if len(c.Body()) > 4096 || sonic.Unmarshal(c.Body(), &input) != nil {
		return deploymentError(c, 400, "invalid release request")
	}
	if err := rt.requireDeploymentWrite(c, input.PipelineID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	if err := rt.requireDeploymentTier(c, input.EnvironmentID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	subject, _ := collectionSubject(c)
	result, err := rt.Services.Deployment.CreateRelease(c.UserContext(), c.Params("projectID"), subject.UserID, input)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	return deploymentResult(c, result)
}
func (rt *Router) getDeploymentRelease(c *fiber.Ctx) error {
	result, err := rt.Services.Deployment.GetRelease(c.UserContext(), c.Params("projectID"), c.Params("releaseID"))
	if err != nil {
		return deploymentServiceError(c, err)
	}
	allowed, err := rt.deploymentAllowed(c, result.PipelineID, "pipeline:read")
	if err != nil {
		return deploymentError(c, 503, "unable to authorize release")
	}
	if !allowed {
		return deploymentError(c, 403, "release access denied")
	}
	return deploymentResult(c, result)
}
func (rt *Router) deploymentReleaseAction(c *fiber.Ctx, cancel bool) error {
	value, err := rt.Services.Deployment.GetRelease(c.UserContext(), c.Params("projectID"), c.Params("releaseID"))
	if err != nil {
		return deploymentServiceError(c, err)
	}
	if err := rt.requireDeploymentWrite(c, value.PipelineID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	if err := rt.requireDeploymentTier(c, value.EnvironmentID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	if cancel {
		value, err = rt.Services.Deployment.Cancel(c.UserContext(), c.Params("projectID"), value.ReleaseID)
	} else {
		subject, _ := collectionSubject(c)
		value, err = rt.Services.Deployment.AdvanceAuthorized(c.UserContext(), c.Params("projectID"), value.ReleaseID, subject.UserID)
	}
	if err != nil {
		return deploymentServiceError(c, err)
	}
	return deploymentResult(c, value)
}
func (rt *Router) advanceDeploymentRelease(c *fiber.Ctx) error {
	return rt.deploymentReleaseAction(c, false)
}
func (rt *Router) cancelDeploymentRelease(c *fiber.Ctx) error {
	return rt.deploymentReleaseAction(c, true)
}

func (rt *Router) inspectDeploymentRelease(c *fiber.Ctx) error {
	value, err := rt.Services.Deployment.GetRelease(c.UserContext(), c.Params("projectID"), c.Params("releaseID"))
	if err != nil {
		return deploymentServiceError(c, err)
	}
	if err := rt.requireDeploymentWrite(c, value.PipelineID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	if err := rt.requireDeploymentTier(c, value.EnvironmentID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	subject, _ := collectionSubject(c)
	inspection, err := rt.Services.Deployment.InspectAuthorized(c.UserContext(), c.Params("projectID"), value.ReleaseID, subject.UserID)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	return deploymentResult(c, inspection)
}
func (rt *Router) recoverDeploymentRelease(c *fiber.Ctx) error {
	var input struct {
		ConfirmRecovery bool `json:"confirmRecovery"`
	}
	if len(c.Body()) > 1024 || sonic.Unmarshal(c.Body(), &input) != nil || !input.ConfirmRecovery {
		return deploymentError(c, 400, "explicit confirmRecovery is required; the previous request may already have applied")
	}
	value, err := rt.Services.Deployment.GetRelease(c.UserContext(), c.Params("projectID"), c.Params("releaseID"))
	if err != nil {
		return deploymentServiceError(c, err)
	}
	if err := rt.requireDeploymentWrite(c, value.PipelineID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	if err := rt.requireDeploymentTier(c, value.EnvironmentID); err != nil || c.Locals("deploymentResponse") != nil {
		return err
	}
	subject, _ := collectionSubject(c)
	result, err := rt.Services.Deployment.RecoverAuthorized(c.UserContext(), c.Params("projectID"), value.ReleaseID, subject.UserID)
	if err != nil {
		return deploymentServiceError(c, err)
	}
	return deploymentResult(c, result)
}
