// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"errors"
	"strconv"

	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) pollingRouter(r fiber.Router, authMW fiber.Handler) {
	group := r.Group("/project/:projectID/polling", sensitiveHTTPStatus, authMW)
	group.Get("/", rt.getPollingStatus)
	group.Get("/candidates", rt.getPollingCandidates)
	group.Post("/baseline", rt.createPollingBaseline)
	group.Post("/enable", rt.enablePolling)
	group.Post("/disable", rt.disablePolling)
}
func (rt *Router) pollingActor(c *fiber.Ctx) (string, error) {
	claims, ok := c.Locals("claims").(*jwt.AuthClaims)
	if !ok || claims == nil || claims.UserID == "" {
		return "", polling.ErrForbidden
	}
	if rt.Services == nil || rt.Services.Polling == nil {
		return "", errors.New("polling service unavailable")
	}
	return claims.UserID, nil
}
func pollingHTTPError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, polling.ErrForbidden):
		return sensitiveError(c, fiber.StatusForbidden, "current polling access denied")
	case errors.Is(err, polling.ErrConflict):
		return sensitiveError(c, fiber.StatusConflict, "polling state changed; refresh and review the current state")
	case errors.Is(err, polling.ErrBlocked):
		return sensitiveError(c, fiber.StatusUnprocessableEntity, "polling configuration or selection is not eligible")
	case errors.Is(err, polling.ErrUnresolved):
		return sensitiveError(c, fiber.StatusConflict, "polling has unresolved execution evidence; operator reconciliation is required")
	default:
		return sensitiveError(c, fiber.StatusServiceUnavailable, "polling operation unavailable; no successful change is confirmed")
	}
}
func (rt *Router) getPollingStatus(c *fiber.Ctx) error {
	actor, e := rt.pollingActor(c)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	result, e := rt.Services.Polling.Status(c.UserContext(), actor, c.Params("projectID"))
	if e != nil {
		return pollingHTTPError(c, e)
	}
	c.Locals("sensitiveAuthorized", true)
	return apphttp.Detail(c, result)
}
func (rt *Router) getPollingCandidates(c *fiber.Ctx) error {
	actor, e := rt.pollingActor(c)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	page, e := strconv.Atoi(c.Query("pageNum", "1"))
	if e != nil {
		return sensitiveError(c, 400, "invalid page number")
	}
	size, e := strconv.Atoi(c.Query("pageSize", "25"))
	if e != nil {
		return sensitiveError(c, 400, "invalid page size")
	}
	result, e := rt.Services.Polling.Candidates(c.UserContext(), actor, c.Params("projectID"), page, size)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	c.Locals("sensitiveAuthorized", true)
	return apphttp.Detail(c, result)
}
func pollingBody(c *fiber.Ctx, out any) error {
	if len(c.Body()) == 0 || len(c.Body()) > 16384 {
		return errors.New("invalid bounded polling request")
	}
	return sonic.Unmarshal(c.Body(), out)
}
func (rt *Router) createPollingBaseline(c *fiber.Ctx) error {
	actor, e := rt.pollingActor(c)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	var request struct {
		ExpectedRevision   uint64 `json:"expectedRevision"`
		AcknowledgeDiscard bool   `json:"acknowledgeDiscard"`
	}
	if e := pollingBody(c, &request); e != nil {
		return sensitiveError(c, 400, "invalid polling baseline request")
	}
	result, e := rt.Services.Polling.Baseline(c.UserContext(), actor, c.Params("projectID"), request.ExpectedRevision, request.AcknowledgeDiscard)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	c.Locals("sensitiveAuthorized", true)
	return apphttp.Detail(c, result)
}
func (rt *Router) enablePolling(c *fiber.Ctx) error {
	actor, e := rt.pollingActor(c)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	var request struct {
		ExpectedRevision uint64   `json:"expectedRevision"`
		BaselineID       string   `json:"baselineId"`
		PipelineIDs      []string `json:"pipelineIds"`
	}
	if e := pollingBody(c, &request); e != nil || request.BaselineID == "" || len(request.PipelineIDs) == 0 || len(request.PipelineIDs) > polling.MaxPipelines {
		return sensitiveError(c, 400, "invalid explicit polling selection")
	}
	result, e := rt.Services.Polling.Enable(c.UserContext(), actor, c.Params("projectID"), request.ExpectedRevision, request.BaselineID, request.PipelineIDs)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	c.Locals("sensitiveAuthorized", true)
	return apphttp.Detail(c, result)
}
func (rt *Router) disablePolling(c *fiber.Ctx) error {
	actor, e := rt.pollingActor(c)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
	}
	if e := pollingBody(c, &request); e != nil {
		return sensitiveError(c, 400, "invalid polling disable request")
	}
	result, e := rt.Services.Polling.Disable(c.UserContext(), actor, c.Params("projectID"), request.ExpectedRevision)
	if e != nil {
		return pollingHTTPError(c, e)
	}
	c.Locals("sensitiveAuthorized", true)
	return apphttp.Detail(c, result)
}
