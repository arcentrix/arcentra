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
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) pipelineRouter(r fiber.Router, authMiddleware fiber.Handler) {
	pipeline := r.Group("/pipelines")
	{
		pipeline.Post("/", authMiddleware, rt.createPipeline)
		pipeline.Put("/:pipelineID", authMiddleware, rt.updatePipeline)
		pipeline.Get("/:pipelineID", authMiddleware, rt.getPipeline)
		pipeline.Get("/", authMiddleware, rt.listPipelines)
		pipeline.Delete("/:pipelineID", authMiddleware, rt.deletePipeline)

		pipeline.Get("/:pipelineID/spec", authMiddleware, rt.getPipelineSpec)
		pipeline.Post("/:pipelineID/spec/validate", authMiddleware, rt.validatePipelineSpec)
		pipeline.Post("/:pipelineID/spec/save", authMiddleware, rt.savePipelineSpec)

		pipeline.Post("/:pipelineID/trigger", authMiddleware, rt.triggerPipeline)
		pipeline.Get("/:pipelineID/runs", authMiddleware, rt.listPipelineRuns)
		pipeline.Get("/runs/:runID", authMiddleware, rt.getPipelineRun)

		pipeline.Post("/:pipelineID/runs/:runID/stop", authMiddleware, rt.stopPipeline)
		pipeline.Post("/:pipelineID/runs/:runID/pause", authMiddleware, rt.pausePipeline)
		pipeline.Post("/:pipelineID/runs/:runID/resume", authMiddleware, rt.resumePipeline)
	}
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
		createdBy = auth.CurrentUserID(c, rt.HTTP.Auth.SecretKey)
	}

	resp, err := rt.pipelineService().CreatePipeline(c.Context(), &pipelinev1.CreatePipelineRequest{
		ProjectId:        strings.TrimSpace(req.ProjectID),
		Name:             strings.TrimSpace(req.Name),
		Description:      strings.TrimSpace(req.Description),
		RepoUrl:          strings.TrimSpace(req.RepoURL),
		DefaultBranch:    strings.TrimSpace(req.DefaultBranch),
		PipelineFilePath: strings.TrimSpace(req.PipelineFilePath),
		SaveMode:         parseSaveMode(req.SaveMode),
		PrTargetBranch:   strings.TrimSpace(req.PrTargetBranch),
		Metadata:         req.Metadata,
		CreatedBy:        createdBy,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}

	return http.Detail(c, map[string]any{
		"pipelineID": resp.GetPipelineId(),
		"message":    resp.GetMessage(),
	})
}

func (rt *Router) updatePipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}

	var req struct {
		Name             string            `json:"name"`
		Description      string            `json:"description"`
		RepoURL          string            `json:"repoUrl"`
		DefaultBranch    string            `json:"defaultBranch"`
		PipelineFilePath string            `json:"pipelineFilePath"`
		SaveMode         string            `json:"saveMode"`
		PrTargetBranch   string            `json:"prTargetBranch"`
		Metadata         map[string]string `json:"metadata"`
		IsEnabled        int32             `json:"isEnabled"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	resp, err := rt.pipelineService().UpdatePipeline(c.Context(), &pipelinev1.UpdatePipelineRequest{
		PipelineId:       pipelineID,
		Name:             req.Name,
		Description:      req.Description,
		RepoUrl:          req.RepoURL,
		DefaultBranch:    req.DefaultBranch,
		PipelineFilePath: req.PipelineFilePath,
		SaveMode:         parseSaveMode(req.SaveMode),
		PrTargetBranch:   req.PrTargetBranch,
		Metadata:         req.Metadata,
		IsEnabled:        req.IsEnabled,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}

	return http.Operation(c)
}

func (rt *Router) getPipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}
	resp, err := rt.pipelineService().GetPipeline(c.Context(), &pipelinev1.GetPipelineRequest{PipelineId: pipelineID})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Detail(c, resp.GetPipeline())
}

func (rt *Router) listPipelines(c *fiber.Ctx) error {
	req := &pipelinev1.ListPipelinesRequest{
		ProjectId: strings.TrimSpace(c.Query("projectId")),
		Name:      strings.TrimSpace(c.Query("name")),
		Page:      int32(maxIntWithOne(rt.HTTP.QueryInt(c, "page"))),
		PageSize:  int32(maxIntWithOne(rt.HTTP.QueryInt(c, "pageSize"))),
		Status:    parsePipelineStatus(c.Query("status")),
	}
	resp, err := rt.pipelineService().ListPipelines(c.Context(), req)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if !resp.GetSuccess() {
		return http.Err(c, http.Failed.Code, resp.GetMessage())
	}
	return http.Detail(c, map[string]any{
		"list":     resp.GetPipelines(),
		"total":    resp.GetTotal(),
		"page":     resp.GetPage(),
		"pageSize": resp.GetPageSize(),
	})
}

func (rt *Router) deletePipeline(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}
	resp, err := rt.pipelineService().DeletePipeline(c.Context(), &pipelinev1.DeletePipelineRequest{PipelineId: pipelineID})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Operation(c)
}

func (rt *Router) getPipelineSpec(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}
	resp, err := rt.pipelineService().GetPipelineSpec(c.Context(), &pipelinev1.GetPipelineSpecRequest{PipelineId: pipelineID})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Detail(c, map[string]any{
		"spec":             resp.GetSpec(),
		"format":           resp.GetFormat().String(),
		"headCommitSha":    resp.GetHeadCommitSha(),
		"branch":           resp.GetBranch(),
		"pipelineFilePath": resp.GetPipelineFilePath(),
	})
}

func (rt *Router) validatePipelineSpec(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}
	var req struct {
		Spec   *pipelinev1.Spec `json:"spec"`
		Format string           `json:"format"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}
	resp, err := rt.pipelineService().ValidatePipelineSpec(c.Context(), &pipelinev1.ValidatePipelineSpecRequest{
		PipelineId: pipelineID,
		Spec:       req.Spec,
		Format:     parseSpecFormat(req.Format),
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Detail(c, map[string]any{
		"jobsCount": resp.GetJobsCount(),
		"warnings":  resp.GetWarnings(),
	})
}

func (rt *Router) savePipelineSpec(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}
	var req struct {
		Spec                  *pipelinev1.Spec `json:"spec"`
		Format                string           `json:"format"`
		ExpectedHeadCommitSha string           `json:"expectedHeadCommitSha"`
		CommitMessage         string           `json:"commitMessage"`
		RequestID             string           `json:"requestId"`
		Editor                string           `json:"editor"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}
	editor := strings.TrimSpace(req.Editor)
	if editor == "" {
		editor = auth.CurrentUserID(c, rt.HTTP.Auth.SecretKey)
	}
	resp, err := rt.pipelineService().SavePipelineSpec(c.Context(), &pipelinev1.SavePipelineSpecRequest{
		PipelineId:            pipelineID,
		Spec:                  req.Spec,
		Format:                parseSpecFormat(req.Format),
		ExpectedHeadCommitSha: req.ExpectedHeadCommitSha,
		CommitMessage:         req.CommitMessage,
		RequestId:             req.RequestID,
		Editor:                editor,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Detail(c, map[string]any{
		"commitSha": resp.GetCommitSha(),
		"branch":    resp.GetBranch(),
		"saveMode":  resp.GetSaveMode().String(),
		"prUrl":     resp.GetPrUrl(),
		"prBranch":  resp.GetPrBranch(),
	})
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
		triggeredBy = auth.CurrentUserID(c, rt.HTTP.Auth.SecretKey)
	}
	resp, err := rt.pipelineService().TriggerPipeline(c.Context(), &pipelinev1.TriggerPipelineRequest{
		PipelineId:  pipelineID,
		Variables:   req.Variables,
		TriggeredBy: triggeredBy,
		RequestId:   req.RequestID,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Detail(c, map[string]any{
		"runID":   resp.GetRunId(),
		"message": resp.GetMessage(),
	})
}

func (rt *Router) listPipelineRuns(c *fiber.Ctx) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	if pipelineID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id is required")
	}
	resp, err := rt.pipelineService().ListPipelineRuns(c.Context(), &pipelinev1.ListPipelineRunsRequest{
		PipelineId: pipelineID,
		Status:     parsePipelineStatus(c.Query("status")),
		Page:       int32(maxIntWithOne(rt.HTTP.QueryInt(c, "page"))),
		PageSize:   int32(maxIntWithOne(rt.HTTP.QueryInt(c, "pageSize"))),
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if !resp.GetSuccess() {
		return http.Err(c, http.Failed.Code, resp.GetMessage())
	}
	return http.Detail(c, map[string]any{
		"list":     resp.GetRuns(),
		"total":    resp.GetTotal(),
		"page":     resp.GetPage(),
		"pageSize": resp.GetPageSize(),
	})
}

func (rt *Router) getPipelineRun(c *fiber.Ctx) error {
	runID := strings.TrimSpace(c.Params("runID"))
	if runID == "" {
		return http.Err(c, http.BadRequest.Code, "run id is required")
	}
	resp, err := rt.pipelineService().GetPipelineRun(c.Context(), &pipelinev1.GetPipelineRunRequest{RunId: runID})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Detail(c, resp.GetRun())
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
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}
	resp, err := rt.pipelineService().StopPipeline(c.Context(), &pipelinev1.StopPipelineRequest{
		PipelineId: pipelineID,
		RunId:      runID,
		Reason:     req.Reason,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return http.Operation(c)
}

func (rt *Router) pausePipeline(c *fiber.Ctx) error {
	return rt.changePipelinePauseState(c, true)
}

func (rt *Router) resumePipeline(c *fiber.Ctx) error {
	return rt.changePipelinePauseState(c, false)
}

func (rt *Router) changePipelinePauseState(c *fiber.Ctx, pause bool) error {
	pipelineID := strings.TrimSpace(c.Params("pipelineID"))
	runID := strings.TrimSpace(c.Params("runID"))
	if pipelineID == "" || runID == "" {
		return http.Err(c, http.BadRequest.Code, "pipeline id and run id are required")
	}
	var req struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}
	operator := strings.TrimSpace(req.Operator)
	if operator == "" {
		operator = auth.CurrentUserID(c, rt.HTTP.Auth.SecretKey)
	}
	if err := rt.applyPauseState(c, pause, pipelineID, runID, req.Reason, operator); err != nil {
		return err
	}
	return http.Operation(c)
}

func (rt *Router) applyPauseState(
	c *fiber.Ctx,
	pause bool,
	pipelineID, runID, reason, operator string,
) error {
	if pause {
		resp, err := rt.pipelineService().PausePipeline(c.Context(), &pipelinev1.PausePipelineRequest{
			PipelineId: pipelineID,
			RunId:      runID,
			Reason:     reason,
			Operator:   operator,
		})
		if err != nil {
			return http.Err(c, http.Failed.Code, err.Error())
		}
		if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
			return http.Err(c, e.code, e.msg)
		}
		return nil
	}

	resp, err := rt.pipelineService().ResumePipeline(c.Context(), &pipelinev1.ResumePipelineRequest{
		PipelineId: pipelineID,
		RunId:      runID,
		Reason:     reason,
		Operator:   operator,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	if e := pipelineAPIError(resp.GetSuccess(), resp.GetMessage(), resp.GetError()); e != nil {
		return http.Err(c, e.code, e.msg)
	}
	return nil
}

func (rt *Router) pipelineService() *service.PipelineServiceImpl {
	return service.NewPipelineServiceImpl(rt.Services)
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

func maxIntWithOne(a int) int {
	if a > 1 {
		return a
	}
	return 1
}
