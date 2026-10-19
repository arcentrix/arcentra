// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"bytes"
	"io"
	"strconv"

	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
)

// All handlers belong to the already authenticated Copilot group. No additional
// project-prefix middleware may change unrelated deployment/collection responses.
func registerCopilotHistoryRoutes(group fiber.Router, svc *service.CopilotService) {
	respond := func(c *fiber.Ctx, value any, err error) error {
		if err != nil {
			return copilotHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, value)
	}
	group.Post("/conversations", func(c *fiber.Ctx) error {
		var in service.CopilotCreateConversationRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.CreateConversation(c.UserContext(), actor, c.Params("projectID"), in)
		return respond(c, value, err)
	})
	group.Get("/conversations", func(c *fiber.Ctx) error {
		limit, ok := copilotHistoryLimit(c.Query("limit"), 20, 50)
		if !ok || len(c.Query("cursor")) > 512 {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.ListConversations(c.UserContext(), actor, c.Params("projectID"), c.Query("cursor"), limit)
		return respond(c, value, err)
	})
	group.Get("/conversations/:conversationID", func(c *fiber.Ctx) error {
		limit, ok := copilotHistoryLimit(c.Query("limit"), 20, 20)
		after, err := copilotHistorySequence(c.Query("after"))
		if !ok || err != nil {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.GetConversation(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), after, limit)
		return respond(c, value, err)
	})
	group.Delete("/conversations/:conversationID", func(c *fiber.Ctx) error {
		var in struct {
			ExpectedVersion uint64 `json:"expectedVersion"`
		}
		if !copilotHistoryBody(c, &in, 1024) || in.ExpectedVersion == 0 {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		err := svc.DeleteConversation(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), in.ExpectedVersion)
		return respond(c, fiber.Map{"deleted": true}, err)
	})
	group.Post("/conversations/:conversationID/preview", func(c *fiber.Ctx) error {
		var in service.CopilotHistoryTurnRequest
		if !copilotHistoryBody(c, &in, 16384) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.PreviewConversationTurn(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), in)
		return respond(c, value, err)
	})
	group.Post("/conversations/:conversationID/turns", func(c *fiber.Ctx) error {
		var in service.CopilotHistoryTurnRequest
		if !copilotHistoryBody(c, &in, 16384) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.SendConversationTurn(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), in)
		return respond(c, value, err)
	})
	group.Get("/conversations/:conversationID/requests/:requestID", func(c *fiber.Ctx) error {
		actor, _ := collectionSubject(c)
		value, err := svc.GetConversationTurn(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), c.Params("requestID"))
		return respond(c, value, err)
	})
	group.Post("/conversations/:conversationID/requests/:requestID/cancel", func(c *fiber.Ctx) error {
		var in struct{}
		if !copilotHistoryBody(c, &in, 128) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.CancelConversationTurn(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), c.Params("requestID"))
		return respond(c, value, err)
	})
	group.Post("/conversations/:conversationID/plans", func(c *fiber.Ctx) error {
		var in service.CopilotPlanRequest
		if !copilotHistoryBody(c, &in, 24576) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.CreateConversationPlan(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), in)
		return respond(c, value, err)
	})
	group.Get("/conversations/:conversationID/plans", func(c *fiber.Ctx) error {
		limit, ok := copilotHistoryLimit(c.Query("limit"), 20, 20)
		after, err := copilotHistorySequence(c.Query("after"))
		if !ok || err != nil {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.ListConversationPlans(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), after, limit)
		return respond(c, value, err)
	})
	group.Get("/conversations/:conversationID/plans/:planID", func(c *fiber.Ctx) error {
		actor, _ := collectionSubject(c)
		value, err := svc.GetConversationPlan(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), c.Params("planID"))
		return respond(c, value, err)
	})
	group.Post("/conversations/:conversationID/plans/:planID/review", func(c *fiber.Ctx) error {
		var in service.CopilotPlanReviewRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.ReviewConversationPlan(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), c.Params("planID"), in)
		return respond(c, value, err)
	})
	group.Post("/conversations/:conversationID/plans/:planID/cancel", func(c *fiber.Ctx) error {
		var in service.CopilotPlanCancelRequest
		if !copilotHistoryBody(c, &in, 1024) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.CancelConversationPlan(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), c.Params("planID"), in)
		return respond(c, value, err)
	})
}
func copilotHistoryBody(c *fiber.Ctx, out any, max int) bool {
	if len(c.Body()) == 0 || len(c.Body()) > max {
		return false
	}
	d := sonic.ConfigDefault.NewDecoder(bytes.NewReader(c.Body()))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil && d.Decode(new(any)) == io.EOF
}
func copilotHistoryLimit(raw string, fallback, max int) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil && n >= 1 && n <= max && strconv.Itoa(n) == raw
}
func copilotHistorySequence(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != raw {
		return 0, service.ErrCopilotInput
	}
	return n, nil
}
