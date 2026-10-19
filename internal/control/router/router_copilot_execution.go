// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

func registerCopilotExecutionRoutes(group fiber.Router, svc *service.CopilotService) {
	respond := func(c *fiber.Ctx, value any, err error) error {
		if err != nil {
			return copilotHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		return apphttp.Detail(c, value)
	}
	group.Post("/conversations/:conversationID/plans/:planID/execution-intents", func(c *fiber.Ctx) error {
		var in service.CopilotExecutionPrepareRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.PrepareRetryExecution(c.UserContext(), actor, c.Params("projectID"), c.Params("conversationID"), c.Params("planID"), in)
		return respond(c, value, err)
	})
	group.Get("/execution-intents", func(c *fiber.Ctx) error {
		limit, ok := copilotHistoryLimit(c.Query("limit"), 20, 20)
		after, err := copilotHistorySequence(c.Query("after"))
		if !ok || err != nil {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.ListRetryExecutions(c.UserContext(), actor, c.Params("projectID"), after, limit)
		return respond(c, value, err)
	})
	group.Get("/execution-intents/:intentID", func(c *fiber.Ctx) error {
		actor, _ := collectionSubject(c)
		value, err := svc.GetRetryExecution(c.UserContext(), actor, c.Params("projectID"), c.Params("intentID"))
		return respond(c, value, err)
	})
	group.Post("/execution-intents/:intentID/confirm", func(c *fiber.Ctx) error {
		var in service.CopilotExecutionConfirmRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.ConfirmRetryExecution(c.UserContext(), actor, c.Params("projectID"), c.Params("intentID"), in)
		return respond(c, value, err)
	})
	group.Post("/execution-intents/:intentID/cancel", func(c *fiber.Ctx) error {
		var in service.CopilotExecutionCancelRequest
		if !copilotHistoryBody(c, &in, 2048) {
			return copilotHTTPError(c, service.ErrCopilotInput)
		}
		actor, _ := collectionSubject(c)
		value, err := svc.CancelRetryExecution(c.UserContext(), actor, c.Params("projectID"), c.Params("intentID"), in)
		return respond(c, value, err)
	})
}
