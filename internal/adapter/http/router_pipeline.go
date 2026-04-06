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

package http

import (
	"strings"

	"github.com/arcentrix/arcentra/internal/case/pipeline"
	domainPipeline "github.com/arcentrix/arcentra/internal/domain/pipeline"
	"github.com/arcentrix/arcentra/pkg/transport/auth"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) pipelineRoutes(r fiber.Router, authMiddleware fiber.Handler) {
	p := r.Group("/pipelines")
	p.Post("/", authMiddleware, rt.createPipeline)
	p.Put("/:pipelineID", authMiddleware, rt.updatePipeline)
	p.Get("/:pipelineID", authMiddleware, rt.getPipeline)
	p.Get("/", authMiddleware, rt.listPipelines)
	p.Delete("/:pipelineID", authMiddleware, rt.deletePipeline)
	p.Get("/:pipelineID/spec", authMiddleware, rt.getPipelineSpec)
	p.Post("/:pipelineID/spec/validate", authMiddleware, rt.validatePipelineSpec)
	p.Post("/:pipelineID/spec/save", authMiddleware, rt.savePipelineSpec)
	p.Post("/:pipelineID/trigger", authMiddleware, rt.triggerPipeline)
	p.Get("/:pipelineID/runs", authMiddleware, rt.listPipelineRuns)
	p.Get("/runs/:runID", authMiddleware, rt.getPipelineRun)
	p.Post("/:pipelineID/runs/:runID/stop", authMiddleware, rt.stopPipeline)
	p.Post("/:pipelineID/runs/:runID/pause", authMiddleware, rt.pausePipeline)
	p.Post("/:pipelineID/runs/:runID/resume", authMiddleware, rt.resumePipeline)
}

func (rt *Router) createPipeline(c *fiber.Ctx) error {
	var req struct {
		ProjectID        string            `json:"projectId"`
		Name             string            `json:"name"`
		Description      string            `json:"description"`
		RepoURL          string            `json:"repoUrl"`
		DefaultBranch    string            `json:"defaultBranch"`
		PipelineFilePath string            `json:"pipelineFilePath"`
		SaveMode         string            `json:"saveMode"`
		PrTargetBranch   string            `json:"prTargetBranch"`
		Metadata         map[string]string `json:"metadata"`
		CreatedBy        string            `json:"createdBy"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	createdBy := strings.TrimSpace(req.CreatedBy)
	if createdBy == "" {
		createdBy = rt.currentUserID(c)
	}

	p, err := rt.ManagePipeline.CreatePipeline(c.Context(), pipeline.CreatePipelineInput{
		ProjectID:        strings.TrimSpace(req.ProjectID),
		Name:             strings.TrimSpace(req.Name),
		Description:      strings.TrimSpace(req.Description),
		RepoURL:          strings.TrimSpace(req.RepoURL),
		DefaultBranch:    strings.TrimSpace(req.DefaultBranch),
		PipelineFilePath: strings.TrimSpace(req.PipelineFilePath),
		CreatedBy:        createdBy,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"pipelineID": p.PipelineID,
		"message":    "pipeline created successfully",
	})
}

func (rt *Router) updatePipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	if err := rt.ManagePipeline.UpdatePipeline(c.Context(), pipelineID, req); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) getPipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	p, err := rt.ManagePipeline.GetPipeline(c.Context(), pipelineID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, p)
}

func (rt *Router) listPipelines(c *fiber.Ctx) error {
	projectID := strings.TrimSpace(c.Query("projectId"))
	name := strings.TrimSpace(c.Query("name"))
	page := maxIntWithOne(rt.HTTP.QueryInt(c, "page"))
	pageSize := maxIntWithOne(rt.HTTP.QueryInt(c, "pageSize"))

	query := &domainPipeline.PipelineQuery{
		ProjectID: projectID,
		Name:      name,
		Page:      page,
		Size:      pageSize,
	}

	pipelines, total, err := rt.ManagePipeline.ListPipelines(c.Context(), query)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":     pipelines,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

func (rt *Router) deletePipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	if err := rt.ManagePipeline.DeletePipeline(c.Context(), pipelineID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) getPipelineSpec(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	spec, err := rt.ManagePipeline.GetPipelineSpec(c.Context(), pipelineID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, spec)
}

func (rt *Router) validatePipelineSpec(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	result, err := rt.ManagePipeline.ValidatePipelineSpec(c.Context(), pipelineID, req)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) savePipelineSpec(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}
	req["pipelineId"] = pipelineID
	if _, ok := req["editor"]; !ok || req["editor"] == "" {
		req["editor"] = rt.currentUserID(c)
	}

	result, err := rt.ManagePipeline.SavePipelineSpec(c.Context(), pipelineID, req)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, result)
}

func (rt *Router) triggerPipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	var req struct {
		Variables   map[string]string `json:"variables"`
		TriggeredBy string            `json:"triggeredBy"`
		RequestID   string            `json:"requestId"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	triggeredBy := strings.TrimSpace(req.TriggeredBy)
	if triggeredBy == "" {
		triggeredBy = rt.currentUserID(c)
	}

	run, err := rt.ManagePipeline.TriggerRun(c.Context(), pipeline.TriggerRunInput{
		PipelineID:  pipelineID,
		TriggeredBy: triggeredBy,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"runID":   run.RunID,
		"message": "pipeline triggered successfully",
	})
}

func (rt *Router) listPipelineRuns(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	page := maxIntWithOne(rt.HTTP.QueryInt(c, "page"))
	pageSize := maxIntWithOne(rt.HTTP.QueryInt(c, "pageSize"))

	query := &domainPipeline.PipelineRunQuery{
		PipelineID: pipelineID,
		Page:       page,
		Size:       pageSize,
	}

	runs, total, err := rt.ManagePipeline.ListRuns(c.Context(), query)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":     runs,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

func (rt *Router) getPipelineRun(c *fiber.Ctx) error {
	runID := strings.TrimSpace(c.Params("runID"))
	if runID == "" {
		return http.Err(c, http.BadRequest.Code, "run id is required")
	}

	run, err := rt.ManagePipeline.GetRun(c.Context(), runID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, run)
}

func (rt *Router) stopPipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	runID := strings.TrimSpace(c.Params("runID"))
	if pipelineID == "" || runID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id and run id are required")
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.BodyParser(&req)

	if err := rt.ManagePipeline.StopRun(c.Context(), pipelineID, runID, req.Reason); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) pausePipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	runID := strings.TrimSpace(c.Params("runID"))
	if pipelineID == "" || runID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id and run id are required")
	}

	var req struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	_ = c.BodyParser(&req)
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		operator = rt.currentUserID(c)
	}

	if err := rt.ManagePipeline.PauseRun(c.Context(), pipelineID, runID, req.Reason, operator); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) resumePipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	runID := strings.TrimSpace(c.Params("runID"))
	if pipelineID == "" || runID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id and run id are required")
	}

	var req struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	_ = c.BodyParser(&req)
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		operator = rt.currentUserID(c)
	}

	if err := rt.ManagePipeline.ResumeRun(c.Context(), pipelineID, runID, req.Reason, operator); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}

func (rt *Router) currentUserID(c *fiber.Ctx) string {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.UserID)
}

func maxIntWithOne(a int) int {
	if a > 1 {
		return a
	}
	return 1
}
