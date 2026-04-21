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

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	tmpl "github.com/arcentrix/arcentra/internal/shared/pipeline/template"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) pipelineTemplateRouter(r fiber.Router, authMiddleware fiber.Handler) {
	libraries := r.Group("/pipeline-template-libraries")
	{
		libraries.Post("/", authMiddleware, rt.registerTemplateLibrary)
		libraries.Get("/", authMiddleware, rt.listTemplateLibraries)
		libraries.Get("/:libraryID", authMiddleware, rt.getTemplateLibrary)
		libraries.Put("/:libraryID", authMiddleware, rt.updateTemplateLibrary)
		libraries.Delete("/:libraryID", authMiddleware, rt.deleteTemplateLibrary)
		libraries.Post("/:libraryID/sync", authMiddleware, rt.syncTemplateLibrary)
		libraries.Post("/:libraryID/templates", authMiddleware, rt.createTemplateInLibrary)
	}

	templates := r.Group("/pipeline-templates")
	{
		templates.Get("/", authMiddleware, rt.listTemplates)
		templates.Get("/categories", authMiddleware, rt.listTemplateCategories)
		templates.Post("/instantiate", authMiddleware, rt.instantiateTemplate)
		templates.Get("/:templateID", authMiddleware, rt.getTemplate)
		templates.Get("/:templateID/versions", authMiddleware, rt.listTemplateVersions)
		templates.Put("/:templateID", authMiddleware, rt.saveTemplate)
		templates.Delete("/:templateID", authMiddleware, rt.deleteTemplate)
	}
}

// ---------------------------------------------------------------------------
// Library endpoints
// ---------------------------------------------------------------------------

func (rt *Router) registerTemplateLibrary(c *fiber.Ctx) error {
	var req struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		RepoURL      string `json:"repoUrl"`
		DefaultRef   string `json:"defaultRef"`
		AuthType     int    `json:"authType"`
		CredentialID string `json:"credentialId"`
		Scope        string `json:"scope"`
		ScopeID      string `json:"scopeId"`
		SyncInterval int    `json:"syncInterval"`
		TemplateDir  string `json:"templateDir"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	lib := &model.PipelineTemplateLibrary{
		Name:         strings.TrimSpace(req.Name),
		Description:  strings.TrimSpace(req.Description),
		RepoURL:      strings.TrimSpace(req.RepoURL),
		DefaultRef:   strings.TrimSpace(req.DefaultRef),
		AuthType:     req.AuthType,
		CredentialID: strings.TrimSpace(req.CredentialID),
		Scope:        strings.TrimSpace(req.Scope),
		ScopeID:      strings.TrimSpace(req.ScopeID),
		SyncInterval: req.SyncInterval,
		TemplateDir:  strings.TrimSpace(req.TemplateDir),
		CreatedBy:    rt.currentUserID(c),
	}

	if err := rt.Services.PipelineTemplate.RegisterLibrary(c.Context(), lib); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"libraryId": lib.LibraryID,
		"message":   "library registered",
	})
}

func (rt *Router) listTemplateLibraries(c *fiber.Ctx) error {
	query := &repo.TemplateLibraryQuery{
		Scope:    strings.TrimSpace(c.Query("scope")),
		ScopeID:  strings.TrimSpace(c.Query("scopeId")),
		Name:     strings.TrimSpace(c.Query("name")),
		Page:     maxIntWithOne(rt.HTTP.QueryInt(c, "page")),
		PageSize: maxIntWithOne(rt.HTTP.QueryInt(c, "pageSize")),
	}
	list, total, err := rt.Services.PipelineTemplate.ListLibraries(c.Context(), query)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{
		"list":     list,
		"total":    total,
		"page":     query.Page,
		"pageSize": query.PageSize,
	})
}

func (rt *Router) getTemplateLibrary(c *fiber.Ctx) error {
	libraryID := strings.TrimSpace(c.Params("libraryID"))
	if libraryID == "" {
		return http.Err(c, http.BadRequest.Code, "library id is required")
	}
	lib, err := rt.Services.PipelineTemplate.GetLibrary(c.Context(), libraryID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, lib)
}

func (rt *Router) updateTemplateLibrary(c *fiber.Ctx) error {
	libraryID := strings.TrimSpace(c.Params("libraryID"))
	if libraryID == "" {
		return http.Err(c, http.BadRequest.Code, "library id is required")
	}
	var req struct {
		Name         *string `json:"name"`
		Description  *string `json:"description"`
		DefaultRef   *string `json:"defaultRef"`
		AuthType     *int    `json:"authType"`
		CredentialID *string `json:"credentialId"`
		SyncInterval *int    `json:"syncInterval"`
		TemplateDir  *string `json:"templateDir"`
		IsEnabled    *int    `json:"isEnabled"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	updates := map[string]any{}
	if req.Name != nil {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.DefaultRef != nil {
		updates["default_ref"] = strings.TrimSpace(*req.DefaultRef)
	}
	if req.AuthType != nil {
		updates["auth_type"] = *req.AuthType
	}
	if req.CredentialID != nil {
		updates["credential_id"] = strings.TrimSpace(*req.CredentialID)
	}
	if req.SyncInterval != nil {
		updates["sync_interval"] = *req.SyncInterval
	}
	if req.TemplateDir != nil {
		updates["template_dir"] = strings.TrimSpace(*req.TemplateDir)
	}
	if req.IsEnabled != nil {
		updates["is_enabled"] = *req.IsEnabled
	}

	if err := rt.Services.PipelineTemplate.UpdateLibrary(c.Context(), libraryID, updates); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Operation(c)
}

func (rt *Router) deleteTemplateLibrary(c *fiber.Ctx) error {
	libraryID := strings.TrimSpace(c.Params("libraryID"))
	if libraryID == "" {
		return http.Err(c, http.BadRequest.Code, "library id is required")
	}
	if err := rt.Services.PipelineTemplate.DeleteLibrary(c.Context(), libraryID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Operation(c)
}

func (rt *Router) syncTemplateLibrary(c *fiber.Ctx) error {
	libraryID := strings.TrimSpace(c.Params("libraryID"))
	if libraryID == "" {
		return http.Err(c, http.BadRequest.Code, "library id is required")
	}
	if err := rt.Services.PipelineTemplate.SyncLibrary(c.Context(), libraryID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Operation(c)
}

// ---------------------------------------------------------------------------
// Template endpoints
// ---------------------------------------------------------------------------

func (rt *Router) listTemplates(c *fiber.Ctx) error {
	query := &repo.TemplateQuery{
		Scope:    strings.TrimSpace(c.Query("scope")),
		ScopeID:  strings.TrimSpace(c.Query("scopeId")),
		Category: strings.TrimSpace(c.Query("category")),
		Name:     strings.TrimSpace(c.Query("name")),
		Page:     maxIntWithOne(rt.HTTP.QueryInt(c, "page")),
		PageSize: maxIntWithOne(rt.HTTP.QueryInt(c, "pageSize")),
	}
	list, total, err := rt.Services.PipelineTemplate.ListTemplates(c.Context(), query)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{
		"list":     list,
		"total":    total,
		"page":     query.Page,
		"pageSize": query.PageSize,
	})
}

func (rt *Router) getTemplate(c *fiber.Ctx) error {
	templateID := strings.TrimSpace(c.Params("templateID"))
	if templateID == "" {
		return http.Err(c, http.BadRequest.Code, "template id is required")
	}
	t, err := rt.Services.PipelineTemplate.GetTemplate(c.Context(), templateID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, t)
}

func (rt *Router) listTemplateVersions(c *fiber.Ctx) error {
	templateID := strings.TrimSpace(c.Params("templateID"))
	if templateID == "" {
		return http.Err(c, http.BadRequest.Code, "template id is required")
	}
	versions, err := rt.Services.PipelineTemplate.ListTemplateVersions(c.Context(), templateID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{"versions": versions})
}

func (rt *Router) listTemplateCategories(c *fiber.Ctx) error {
	categories, err := rt.Services.PipelineTemplate.ListCategories(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{"categories": categories})
}

func (rt *Router) instantiateTemplate(c *fiber.Ctx) error {
	var req struct {
		TemplateID string         `json:"templateId"`
		Version    string         `json:"version"`
		Params     map[string]any `json:"params"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}
	if strings.TrimSpace(req.TemplateID) == "" {
		return http.Err(c, http.BadRequest.Code, "templateId is required")
	}

	rendered, err := rt.Services.PipelineTemplate.InstantiateTemplate(c.Context(), service.InstantiateRequest{
		TemplateID: strings.TrimSpace(req.TemplateID),
		Version:    strings.TrimSpace(req.Version),
		Params:     req.Params,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{"spec": rendered})
}

func (rt *Router) saveTemplate(c *fiber.Ctx) error {
	templateID := strings.TrimSpace(c.Params("templateID"))
	if templateID == "" {
		return http.Err(c, http.BadRequest.Code, "template id is required")
	}
	var req struct {
		SpecContent   string             `json:"specContent"`
		Name          string             `json:"name"`
		Description   string             `json:"description"`
		Category      string             `json:"category"`
		Tags          []string           `json:"tags"`
		Params        []tmpl.ParamSchema `json:"params"`
		CommitMessage string             `json:"commitMessage"`
		Editor        string             `json:"editor"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	editor := strings.TrimSpace(req.Editor)
	if editor == "" {
		editor = rt.currentUserID(c)
	}

	commitSha, err := rt.Services.PipelineTemplate.SaveTemplate(c.Context(), service.SaveTemplateRequest{
		TemplateID:    templateID,
		SpecContent:   req.SpecContent,
		Name:          req.Name,
		Description:   req.Description,
		Category:      req.Category,
		Tags:          req.Tags,
		Params:        req.Params,
		CommitMessage: req.CommitMessage,
		Editor:        editor,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{"commitSha": commitSha})
}

func (rt *Router) createTemplateInLibrary(c *fiber.Ctx) error {
	libraryID := strings.TrimSpace(c.Params("libraryID"))
	if libraryID == "" {
		return http.Err(c, http.BadRequest.Code, "library id is required")
	}
	var req struct {
		Name          string             `json:"name"`
		Description   string             `json:"description"`
		Category      string             `json:"category"`
		Tags          []string           `json:"tags"`
		Params        []tmpl.ParamSchema `json:"params"`
		SpecContent   string             `json:"specContent"`
		CommitMessage string             `json:"commitMessage"`
		Editor        string             `json:"editor"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.RequestParameterParsingFailed.Code, http.RequestParameterParsingFailed.Msg)
	}

	editor := strings.TrimSpace(req.Editor)
	if editor == "" {
		editor = rt.currentUserID(c)
	}

	t, err := rt.Services.PipelineTemplate.CreateTemplateInLibrary(c.Context(), service.CreateTemplateRequest{
		LibraryID:     libraryID,
		Name:          strings.TrimSpace(req.Name),
		Description:   strings.TrimSpace(req.Description),
		Category:      strings.TrimSpace(req.Category),
		Tags:          req.Tags,
		Params:        req.Params,
		SpecContent:   req.SpecContent,
		CommitMessage: strings.TrimSpace(req.CommitMessage),
		Editor:        editor,
	})
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Detail(c, map[string]any{
		"templateId": t.TemplateID,
		"message":    "template created",
	})
}

func (rt *Router) deleteTemplate(c *fiber.Ctx) error {
	templateID := strings.TrimSpace(c.Params("templateID"))
	if templateID == "" {
		return http.Err(c, http.BadRequest.Code, "template id is required")
	}
	var req struct {
		CommitMessage string `json:"commitMessage"`
		Editor        string `json:"editor"`
	}
	_ = c.BodyParser(&req)

	editor := strings.TrimSpace(req.Editor)
	if editor == "" {
		editor = rt.currentUserID(c)
	}

	if err := rt.Services.PipelineTemplate.DeleteTemplateFromLibrary(c.Context(), templateID, req.CommitMessage, editor); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}
	return http.Operation(c)
}
