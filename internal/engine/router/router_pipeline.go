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

package router

import (
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/engine/service"
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) pipelineRouter(r fiber.Router, authMiddleware fiber.Handler) {
	pipeline := r.Group("/pipelines")
	{
		pipeline.Post("/", authMiddleware, rt.createPipeline)
		pipeline.Put("/:pipelineId", authMiddleware, rt.updatePipeline)
		pipeline.Get("/:pipelineId", authMiddleware, rt.getPipeline)
		pipeline.Get("/", authMiddleware, rt.listPipelines)
		pipeline.Delete("/:pipelineId", authMiddleware, rt.deletePipeline)

		pipeline.Get("/:pipelineId/spec", authMiddleware, rt.getPipelineSpec)
		pipeline.Post("/:pipelineId/spec/validate", authMiddleware, rt.validatePipelineSpec)
		pipeline.Post("/:pipelineId/spec/save", authMiddleware, rt.savePipelineSpec)

		pipeline.Post("/:pipelineId/trigger", authMiddleware, rt.triggerPipeline)
		pipeline.Get("/:pipelineId/runs", authMiddleware, rt.listPipelineRuns)
		pipeline.Get("/runs/:runId", authMiddleware, rt.getPipelineRun)

		pipeline.Post("/:pipelineId/runs/:runId/stop", authMiddleware, rt.stopPipeline)
		pipeline.Post("/:pipelineId/runs/:runId/pause", authMiddleware, rt.pausePipeline)
		pipeline.Post("/:pipelineId/runs/:runId/resume", authMiddleware, rt.resumePipeline)
	}
}

func (rt *Router) createPipeline(c *fiber.Ctx) error {
	var req struct {
		ProjectId        string            `json:"projectId"`
		Name             string            `json:"name"`
		Description      string            `json:"description"`
		RepoUrl          string            `json:"repoUrl"`
		DefaultBranch    string            `json:"defaultBranch"`
		PipelineFilePath string            `json:"pipelineFilePath"`
		SaveMode         string            `json:"saveMode"`
		PrTargetBranch   string            `json:"prTargetBranch"`
		Metadata         map[string]string `json:"metadata"`
		CreatedBy        string            `json:"createdBy"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}

	createdBy := strings.TrimSpace(req.CreatedBy)
	if createdBy == "" {
		createdBy = rt.currentUserId(c)
	}

	resp, err := rt.pipelineService().CreatePipeline(c.Context(), &pipelinev1.CreatePipelineRequest{
		ProjectId:        strings.TrimSpace(req.ProjectId),
		Name:             strings.TrimSpace(req.Name),
		Description:      strings.TrimSpace(req.Description),
		RepoUrl:          strings.TrimSpace(req.RepoUrl),
		DefaultBranch:    strings.TrimSpace(req.DefaultBranch),
		PipelineFilePath: strings.TrimSpace(req.PipelineFilePath),
		SaveMode:         parseSaveMode(req.SaveMode),
		PrTargetBranch:   strings.TrimSpace(req.PrTargetBranch),
		Metadata:         req.Metadata,
		CreatedBy:        createdBy,
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]any{
		"pipelineId": resp.GetPipelineId(),
		"message":    resp.GetMessage(),
	})
	return nil
}

func (rt *Router) updatePipeline(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}

	var req struct {
		Name             string            `json:"name"`
		Description      string            `json:"description"`
		RepoUrl          string            `json:"repoUrl"`
		DefaultBranch    string            `json:"defaultBranch"`
		PipelineFilePath string            `json:"pipelineFilePath"`
		SaveMode         string            `json:"saveMode"`
		PrTargetBranch   string            `json:"prTargetBranch"`
		Metadata         map[string]string `json:"metadata"`
		IsEnabled        int32             `json:"isEnabled"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}

	resp, err := rt.pipelineService().UpdatePipeline(c.Context(), &pipelinev1.UpdatePipelineRequest{
		PipelineId:       pipelineId,
		Name:             req.Name,
		Description:      req.Description,
		RepoUrl:          req.RepoUrl,
		DefaultBranch:    req.DefaultBranch,
		PipelineFilePath: req.PipelineFilePath,
		SaveMode:         parseSaveMode(req.SaveMode),
		PrTargetBranch:   req.PrTargetBranch,
		Metadata:         req.Metadata,
		IsEnabled:        req.IsEnabled,
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}

	c.Locals(middleware.OPERATION, pipelineId)
	return nil
}

func (rt *Router) getPipeline(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}
	resp, err := rt.pipelineService().GetPipeline(c.Context(), &pipelinev1.GetPipelineRequest{PipelineId: pipelineId})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.DETAIL, resp.GetPipeline())
	return nil
}

func (rt *Router) listPipelines(c *fiber.Ctx) error {
	req := &pipelinev1.ListPipelinesRequest{
		ProjectId: strings.TrimSpace(c.Query("projectId")),
		Name:      strings.TrimSpace(c.Query("name")),
		Page:      int32(maxInt(rt.Http.QueryInt(c, "page"), 1)),
		PageSize:  int32(maxInt(rt.Http.QueryInt(c, "pageSize"), 1)),
		Status:    parsePipelineStatus(c.Query("status")),
	}
	resp, err := rt.pipelineService().ListPipelines(c.Context(), req)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if !resp.GetSuccess() {
		return http.WithRepErrMsg(c, http.Failed.Code, resp.GetMessage(), c.Path())
	}
	c.Locals(middleware.DETAIL, map[string]any{
		"list":     resp.GetPipelines(),
		"total":    resp.GetTotal(),
		"page":     resp.GetPage(),
		"pageSize": resp.GetPageSize(),
	})
	return nil
}

func (rt *Router) deletePipeline(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}
	resp, err := rt.pipelineService().DeletePipeline(c.Context(), &pipelinev1.DeletePipelineRequest{PipelineId: pipelineId})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.OPERATION, pipelineId)
	return nil
}

func (rt *Router) getPipelineSpec(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}
	resp, err := rt.pipelineService().GetPipelineSpec(c.Context(), &pipelinev1.GetPipelineSpecRequest{PipelineId: pipelineId})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.DETAIL, map[string]any{
		"spec":             resp.GetSpec(),
		"format":           resp.GetFormat().String(),
		"headCommitSha":    resp.GetHeadCommitSha(),
		"branch":           resp.GetBranch(),
		"pipelineFilePath": resp.GetPipelineFilePath(),
	})
	return nil
}

func (rt *Router) validatePipelineSpec(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}
	var req struct {
		Spec   *pipelinev1.Spec `json:"spec"`
		Format string           `json:"format"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}
	resp, err := rt.pipelineService().ValidatePipelineSpec(c.Context(), &pipelinev1.ValidatePipelineSpecRequest{
		PipelineId: pipelineId,
		Spec:       req.Spec,
		Format:     parseSpecFormat(req.Format),
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.DETAIL, map[string]any{
		"jobsCount": resp.GetJobsCount(),
		"warnings":  resp.GetWarnings(),
	})
	return nil
}

func (rt *Router) savePipelineSpec(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}
	var req struct {
		Spec                  *pipelinev1.Spec `json:"spec"`
		Format                string           `json:"format"`
		ExpectedHeadCommitSha string           `json:"expectedHeadCommitSha"`
		CommitMessage         string           `json:"commitMessage"`
		RequestId             string           `json:"requestId"`
		Editor                string           `json:"editor"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}
	editor := strings.TrimSpace(req.Editor)
	if editor == "" {
		editor = rt.currentUserId(c)
	}
	resp, err := rt.pipelineService().SavePipelineSpec(c.Context(), &pipelinev1.SavePipelineSpecRequest{
		PipelineId:            pipelineId,
		Spec:                  req.Spec,
		Format:                parseSpecFormat(req.Format),
		ExpectedHeadCommitSha: req.ExpectedHeadCommitSha,
		CommitMessage:         req.CommitMessage,
		RequestId:             req.RequestId,
		Editor:                editor,
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.DETAIL, map[string]any{
		"commitSha": resp.GetCommitSha(),
		"branch":    resp.GetBranch(),
		"saveMode":  resp.GetSaveMode().String(),
		"prUrl":     resp.GetPrUrl(),
		"prBranch":  resp.GetPrBranch(),
	})
	return nil
}

func (rt *Router) triggerPipeline(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}
	var req struct {
		Variables   map[string]string `json:"variables"`
		TriggeredBy string            `json:"triggeredBy"`
		RequestId   string            `json:"requestId"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}
	triggeredBy := strings.TrimSpace(req.TriggeredBy)
	if triggeredBy == "" {
		triggeredBy = rt.currentUserId(c)
	}
	resp, err := rt.pipelineService().TriggerPipeline(c.Context(), &pipelinev1.TriggerPipelineRequest{
		PipelineId:  pipelineId,
		Variables:   req.Variables,
		TriggeredBy: triggeredBy,
		RequestId:   req.RequestId,
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.DETAIL, map[string]any{
		"runId":   resp.GetRunId(),
		"message": resp.GetMessage(),
	})
	return nil
}

func (rt *Router) listPipelineRuns(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	if pipelineId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id is required", c.Path())
	}
	resp, err := rt.pipelineService().ListPipelineRuns(c.Context(), &pipelinev1.ListPipelineRunsRequest{
		PipelineId: pipelineId,
		Status:     parsePipelineStatus(c.Query("status")),
		Page:       int32(maxInt(rt.Http.QueryInt(c, "page"), 1)),
		PageSize:   int32(maxInt(rt.Http.QueryInt(c, "pageSize"), 1)),
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if !resp.GetSuccess() {
		return http.WithRepErrMsg(c, http.Failed.Code, resp.GetMessage(), c.Path())
	}
	c.Locals(middleware.DETAIL, map[string]any{
		"list":     resp.GetRuns(),
		"total":    resp.GetTotal(),
		"page":     resp.GetPage(),
		"pageSize": resp.GetPageSize(),
	})
	return nil
}

func (rt *Router) getPipelineRun(c *fiber.Ctx) error {
	runId := strings.TrimSpace(c.Params("runId"))
	if runId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "run id is required", c.Path())
	}
	resp, err := rt.pipelineService().GetPipelineRun(c.Context(), &pipelinev1.GetPipelineRunRequest{RunId: runId})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.DETAIL, resp.GetRun())
	return nil
}

func (rt *Router) stopPipeline(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	runId := strings.TrimSpace(c.Params("runId"))
	if pipelineId == "" || runId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id and run id are required", c.Path())
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}
	resp, err := rt.pipelineService().StopPipeline(c.Context(), &pipelinev1.StopPipelineRequest{
		PipelineId: pipelineId,
		RunId:      runId,
		Reason:     req.Reason,
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.OPERATION, runId)
	return nil
}

func (rt *Router) pausePipeline(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	runId := strings.TrimSpace(c.Params("runId"))
	if pipelineId == "" || runId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id and run id are required", c.Path())
	}
	var req struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		operator = rt.currentUserId(c)
	}
	resp, err := rt.pipelineService().PausePipeline(c.Context(), &pipelinev1.PausePipelineRequest{
		PipelineId: pipelineId,
		RunId:      runId,
		Reason:     req.Reason,
		Operator:   operator,
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.OPERATION, runId)
	return nil
}

func (rt *Router) resumePipeline(c *fiber.Ctx) error {
	pipelineId := strings.TrimSpace(c.Params("pipelineId"))
	runId := strings.TrimSpace(c.Params("runId"))
	if pipelineId == "" || runId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "pipeline id and run id are required", c.Path())
	}
	var req struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg, c.Path())
	}
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		operator = rt.currentUserId(c)
	}
	resp, err := rt.pipelineService().ResumePipeline(c.Context(), &pipelinev1.ResumePipelineRequest{
		PipelineId: pipelineId,
		RunId:      runId,
		Reason:     req.Reason,
		Operator:   operator,
	})
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.WithRepErrMsg(c, e.code, e.msg, c.Path())
	}
	c.Locals(middleware.OPERATION, runId)
	return nil
}

func (rt *Router) pipelineService() *service.PipelineServiceImpl {
	return service.NewPipelineServiceImpl(rt.Services)
}

func (rt *Router) currentUserId(c *fiber.Ctx) string {
	claims, err := auth.ParseAuthorizationToken(c, rt.Http.Auth.SecretKey)
	if err != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.UserId)
}

type pipelineRespErr struct {
	code int
	msg  string
}

func pipelineAPIError(success bool, message string, err *pipelinev1.Error) *pipelineRespErr {
	if success {
		return nil
	}
	msg := strings.TrimSpace(message)
	code := http.Failed.Code
	if err != nil {
		if err.GetCode() > 0 {
			code = int(err.GetCode())
		}
		if strings.TrimSpace(err.GetMessage()) != "" {
			msg = strings.TrimSpace(err.GetMessage())
		}
	}
	if msg == "" {
		msg = http.Failed.Msg
	}
	log.Warnw("pipeline http request failed", "code", code, "message", msg)
	return &pipelineRespErr{code: code, msg: msg}
}

func parseSaveMode(mode string) pipelinev1.PipelineSaveMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "pr":
		return pipelinev1.PipelineSaveMode_PIPELINE_SAVE_MODE_PR
	case "direct":
		return pipelinev1.PipelineSaveMode_PIPELINE_SAVE_MODE_DIRECT
	default:
		return pipelinev1.PipelineSaveMode_PIPELINE_SAVE_MODE_UNSPECIFIED
	}
}

func parseSpecFormat(format string) pipelinev1.SpecFormat {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return pipelinev1.SpecFormat_SPEC_FORMAT_JSON
	case "yaml", "yml":
		return pipelinev1.SpecFormat_SPEC_FORMAT_YAML
	default:
		return pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED
	}
}

func parsePipelineStatus(status string) pipelinev1.PipelineStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING
	case "running":
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING
	case "success":
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS
	case "failed":
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED
	case "cancelled", "canceled":
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED
	case "paused":
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_PAUSED
	default:
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_UNSPECIFIED
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
