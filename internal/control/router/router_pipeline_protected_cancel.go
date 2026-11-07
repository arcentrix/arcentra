// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"errors"
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

// AuthenticatedPipelineStop 接收中间件验证的主体，不接受请求体提供的执行者身份
type AuthenticatedPipelineStop interface {
	StopPipelineAs(context.Context, authz.Subject, *pipelinev1.StopPipelineRequest, *service.ProtectedRunCancellationService, ...service.PipelineStopBodyValidation) (service.PipelineRunStopReceipt, error)
}

// ProtectedPipelineStopHandler 仅构造惰性处理器，不注册或替换生产停止路由
// Install only after the existing authentication, Subject and pipeline:cancel
// middleware. The service repeats live permission checks in the mutation tx.
func ProtectedPipelineStopHandler(pipeline AuthenticatedPipelineStop, protected *service.ProtectedRunCancellationService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		subject, ok := middleware.SubjectFromLocals(c)
		if !ok || subject.UserID == "" || subject.IsAgent {
			return pipelineStopHTTPError(c, repo.ErrCopilotRerunForbidden)
		}
		if pipeline == nil || protected == nil {
			return pipelineStopHTTPError(c, repo.ErrCopilotRerunUnavailable)
		}
		pipelineID, runID := strings.TrimSpace(c.Params("pipelineID")), strings.TrimSpace(c.Params("runID"))
		if pipelineID == "" || runID == "" {
			c.Locals("copilotResponse", true)
			return apphttp.Err(c, apphttp.BadRequest.Code, "pipeline id and run id are required")
		}
		var in struct {
			Reason string `json:"reason"`
		}
		ordinaryValid := c.BodyParser(&in) == nil
		var strict struct {
			Reason string `json:"reason"`
		}
		protectedValid := copilotHistoryBody(c, &strict, 2048)
		out, err := pipeline.StopPipelineAs(c.UserContext(), subject, &pipelinev1.StopPipelineRequest{PipelineId: pipelineID, RunId: runID, Reason: in.Reason}, protected, service.PipelineStopBodyValidation{OrdinaryValid: ordinaryValid, ProtectedValid: protectedValid})

		if err != nil {
			return pipelineStopHTTPError(c, err)
		}
		c.Locals("copilotResponse", true)
		if ordinary := out.OrdinaryOutcome(); ordinary != nil {
			if ordinary.BodyInvalid {
				return apphttp.Err(c, apphttp.RequestParameterParsingFailed.Code, apphttp.RequestParameterParsingFailed.Msg)
			}
			if ordinary.Err != nil {
				return apphttp.Err(c, apphttp.Failed.Code, ordinary.Err.Error())
			}
			response := ordinary.Response
			if e := pipelineAPIError(response.GetSuccess(), response.GetMessage(), response.GetError()); e != nil {
				return apphttp.Err(c, e.code, e.msg)
			}
			return apphttp.Operation(c)
		}
		return apphttp.Detail(c, out)
	}
}
func pipelineStopHTTPError(c *fiber.Ctx, err error) error {
	status, message := 503, "Cancellation outcome is unknown; inspect the existing run"
	switch {
	case errors.Is(err, repo.ErrCopilotRerunInvalid):
		status, message = 400, "Invalid cancellation request"
	case errors.Is(err, repo.ErrCopilotRerunForbidden):
		status, message = 403, "Cancellation access denied"
	case errors.Is(err, repo.ErrCopilotRerunConflict):
		status, message = 409, "Cancellation state changed; inspect the existing run"
	}
	c.Locals("copilotResponse", true)
	return apphttp.Err(c.Status(status), status, message)
}
