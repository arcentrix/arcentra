// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) copilotRouter(r fiber.Router, authMW, subjectMW fiber.Handler) {
	cfg := copilot.Config{}
	if rt.AppConf != nil {
		cfg = rt.AppConf.Copilot
	}
	var svc *service.CopilotService
	if rt.Services != nil {
		svc = service.NewCopilotService(rt.Services.Repos(), rt.Services.Authorizer, rt.Services.Artifact, copilot.NewRegistry(cfg), rt.Services.Secret)
		if err := svc.ConfigureHistory(cfg.History); err != nil {
			svc = nil
		}
		if svc != nil {
			svc.ConfigureHistoryPlans(rt.Services.Deployment)
			if err := svc.ConfigureSourceContext(cfg.SourceContext); err != nil {
				svc = nil
			}
		}
	}
	registerCopilotRoutes(r, authMW, subjectMW, svc)
}
func registerCopilotRoutes(r fiber.Router, authMW, subjectMW fiber.Handler, svc *service.CopilotService) {
	group := r.Group("/projects/:projectID/copilot", copilotHTTPStatus, authMW, subjectMW, func(c *fiber.Ctx) error {
		if svc == nil {
			return copilotHTTPError(c, service.ErrCopilotContext)
		}
		if _, ok := collectionSubject(c); !ok {
			return copilotHTTPError(c, service.ErrCopilotDenied)
		}
		return c.Next()
	})
	registerCopilotHistoryRoutes(group, svc)
	group.Get("/status", func(c *fiber.Ctx) error {
		subject, _ := collectionSubject(c)
		result, err := svc.Status(c.UserContext(), subject, c.Params("projectID"))
		if err != nil {
			return copilotHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, result)
	})
	group.Get("/runs/:runID/context", func(c *fiber.Ctx) error {
		subject, _ := collectionSubject(c)
		result, err := svc.Sources(c.UserContext(), subject, c.Params("projectID"), c.Params("runID"))
		if err != nil {
			return copilotHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, result)
	})
	group.Post("/preview", func(c *fiber.Ctx) error {
		if len(c.Body()) > 16384 {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		var input service.CopilotRequest
		decoder := sonic.ConfigDefault.NewDecoder(bytes.NewReader(c.Body()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		subject, _ := collectionSubject(c)
		result, err := svc.Preview(c.UserContext(), subject, c.Params("projectID"), input)
		if err != nil {
			return copilotHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, result)
	})
	group.Post("/analysis", func(c *fiber.Ctx) error {
		if len(c.Body()) > 16384 {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		var input service.CopilotRequest
		decoder := sonic.ConfigDefault.NewDecoder(bytes.NewReader(c.Body()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		subject, _ := collectionSubject(c)
		result, err := svc.Analyze(c.UserContext(), subject, c.Params("projectID"), input)
		if err != nil {
			return copilotHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, result)
	})
	group.Post("/analysis/:requestID/cancel", func(c *fiber.Ctx) error {
		subject, _ := collectionSubject(c)
		if err := svc.Cancel(c.UserContext(), subject, c.Params("projectID"), c.Params("requestID")); err != nil {
			return copilotHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, fiber.Map{"cancelRequested": true})
	})
}
func copilotHTTPStatus(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	err := c.Next()
	if err != nil {
		return err
	}
	if c.Response().StatusCode() == 200 && c.Locals("copilotResponse") == nil {
		c.Status(fiber.StatusUnauthorized)
	}
	return nil
}
func copilotHTTPError(c *fiber.Ctx, err error) error {
	status := fiber.StatusServiceUnavailable
	message := "Copilot is temporarily unavailable"
	switch {
	case errors.Is(err, service.ErrCopilotHistoryConflict), errors.Is(err, service.ErrCopilotHistoryContent), errors.Is(err, service.ErrCopilotPlanUnavailable):
		status = 409
		message = err.Error()
	case errors.Is(err, service.ErrCopilotHistoryTooLarge):
		status = 400
		message = err.Error()
	case errors.Is(err, service.ErrCopilotHistoryUnavailable):
		message = err.Error()
	case errors.Is(err, service.ErrCopilotInput):
		status = 400
		message = err.Error()
	case errors.Is(err, service.ErrCopilotDenied):
		status = 403
		message = err.Error()
	case errors.Is(err, service.ErrCopilotNotFound):
		status = 404
		message = err.Error()
	case errors.Is(err, service.ErrCopilotContext), errors.Is(err, service.ErrCopilotSecrets), errors.Is(err, service.ErrCopilotPreview):
		status = 409
		message = err.Error()
	case errors.Is(err, copilot.ErrUnconfigured):
		message = err.Error()
	case errors.Is(err, copilot.ErrBusy), errors.Is(err, copilot.ErrRateLimited):
		status = 429
		message = err.Error()
	case errors.Is(err, context.Canceled):
		status = 409
		message = "Copilot request was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		status = 504
		message = "Copilot request timed out"
	case errors.Is(err, copilot.ErrProvider), errors.Is(err, copilot.ErrInvalidResponse):
		status = 502
		message = err.Error()
	}
	c.Locals("copilotResponse", true)
	c.Status(status)
	return apphttp.Err(c, status, message)
}
