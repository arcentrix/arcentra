// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"errors"

	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

// RegisterCopilotRerunRoutes installs the authenticated independent execution API.
// A nil startup bundle keeps all endpoints unavailable without bypassing authentication.
// The caller supplies the normal authentication and live-subject middleware.
func RegisterCopilotRerunRoutes(r fiber.Router, authMW, subjectMW fiber.Handler, svc *service.CopilotRerunService) {
	group := r.Group("/projects/:projectID/copilot/rerun-intents", copilotHTTPStatus, authMW, subjectMW, func(c *fiber.Ctx) error {
		if _, ok := collectionSubject(c); !ok {
			return copilotRerunHTTPError(c, repo.ErrCopilotRerunForbidden)
		}
		if svc == nil {
			return copilotRerunHTTPError(c, repo.ErrCopilotRerunUnavailable)
		}
		return c.Next()
	})
	respond := func(c *fiber.Ctx, value any, err error) error {
		if err != nil {
			return copilotRerunHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, value)
	}
	group.Get("/availability", func(c *fiber.Ctx) error {
		subject, _ := collectionSubject(c)
		out, err := svc.Availability(c.UserContext(), subject, c.Params("projectID"))
		return respond(c, out, err)
	})
	group.Get("/eligibility", func(c *fiber.Ctx) error {
		subject, _ := collectionSubject(c)
		out, err := svc.Eligibility(c.UserContext(), subject, c.Params("projectID"), c.Query("originalJobRunId"))
		return respond(c, out, err)
	})
	// This literal route must precede the intent parameter for recovery.
	group.Get("/by-request/:requestID", func(c *fiber.Ctx) error {
		subject, _ := collectionSubject(c)
		out, err := svc.LookupRequest(c.UserContext(), subject, c.Params("projectID"), c.Params("requestID"))
		return respond(c, out, err)
	})
	group.Post("/", func(c *fiber.Ctx) error {
		var in service.CopilotRerunPrepareRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotRerunHTTPError(c, repo.ErrCopilotRerunInvalid)
		}
		subject, _ := collectionSubject(c)
		out, err := svc.Prepare(c.UserContext(), subject, c.Params("projectID"), in)
		return respond(c, out, err)
	})
	group.Get("/:intentID", func(c *fiber.Ctx) error {
		subject, _ := collectionSubject(c)
		out, err := svc.Inspect(c.UserContext(), subject, c.Params("projectID"), c.Params("intentID"))
		return respond(c, out, err)
	})
	group.Post("/:intentID/confirm", func(c *fiber.Ctx) error {
		var in service.CopilotRerunConfirmRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotRerunHTTPError(c, repo.ErrCopilotRerunInvalid)
		}
		subject, _ := collectionSubject(c)
		out, err := svc.Confirm(c.UserContext(), subject, c.Params("projectID"), c.Params("intentID"), in)
		return respond(c, out, err)
	})
	group.Post("/:intentID/cancel-prepared", func(c *fiber.Ctx) error {
		var in service.CopilotRerunCancelRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotRerunHTTPError(c, repo.ErrCopilotRerunInvalid)
		}
		subject, _ := collectionSubject(c)
		out, err := svc.CancelPrepared(c.UserContext(), subject, c.Params("projectID"), c.Params("intentID"), in)
		return respond(c, out, err)
	})
}
func copilotRerunHTTPError(c *fiber.Ctx, err error) error {
	status, message := fiber.StatusServiceUnavailable, "Whole-job new execution is temporarily unavailable"
	switch {
	case errors.Is(err, repo.ErrCopilotRerunInvalid):
		status, message = 400, "Invalid whole-job new execution request"
	case errors.Is(err, repo.ErrCopilotRerunForbidden):
		status, message = 403, "Whole-job new execution access denied"
	case errors.Is(err, repo.ErrCopilotRerunNotFound):
		status, message = 404, "Whole-job new execution request not found"
	case errors.Is(err, repo.ErrCopilotRerunExpired):
		status, message = 410, "Whole-job new execution review expired"
	case errors.Is(err, repo.ErrCopilotRerunUnsupported):
		status, message = 409, "This original run is not supported for whole-job new execution"
	case errors.Is(err, repo.ErrCopilotRerunConflict):
		status, message = 409, "Whole-job new execution evidence changed; inspect the existing request"
	}
	c.Locals("copilotResponse", true)
	return apphttp.Err(c.Status(status), status, message)
}
