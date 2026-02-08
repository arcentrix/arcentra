package router

import (
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

// scmRouter is the router for the scm service
func (rt *Router) scmRouter(r fiber.Router) {
	scmGroup := r.Group("/scm")
	{
		scmGroup.Post("/webhooks/:projectId", rt.handleScmWebhook)
	}
}

// handleScmWebhook is the handler for the scm webhook
func (rt *Router) handleScmWebhook(c *fiber.Ctx) error {
	projectId := c.Params("projectId")
	if projectId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "project id is required", c.Path())
	}

	rawHeaders := c.GetReqHeaders()
	headers := make(map[string]string, len(rawHeaders))
	for k, vv := range rawHeaders {
		if len(vv) > 0 {
			headers[k] = vv[0]
		}
	}
	body := c.Body()

	events, err := rt.Services.Scm.HandleWebhook(c.Context(), projectId, headers, body)
	if err != nil {
		log.Warnw("scm webhook handle failed", "projectId", projectId, "error", err)
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]any{
		"events": events,
	})
	return nil
}
