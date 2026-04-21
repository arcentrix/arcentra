// Copyright 2025 Arcentra Authors.
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
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

// settingRouter registers workspace-scoped setting routes.
func (rt *Router) settingRouter(r fiber.Router, auth fiber.Handler) {
	settingsGroup := r.Group("/settings")
	{
		settingsGroup.Get("/", auth, rt.listSettings)          // GET /settings?workspace=xxx
		settingsGroup.Get("/:name", auth, rt.getSetting)       // GET /settings/:name?workspace=xxx
		settingsGroup.Put("/:name", auth, rt.upsertSetting)    // PUT /settings/:name?workspace=xxx
		settingsGroup.Delete("/:name", auth, rt.deleteSetting) // DELETE /settings/:name?workspace=xxx
	}
}

// listSettings returns all settings for the given workspace.
func (rt *Router) listSettings(c *fiber.Ctx) error {
	workspace := c.Query("workspace")
	if workspace == "" {
		return http.Err(c, http.BadRequest.Code, "workspace is required")
	}

	settings, err := rt.Services.Setting.ListSettings(c.Context(), workspace)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, settings)
}

// getSetting returns a single setting by workspace and name.
func (rt *Router) getSetting(c *fiber.Ctx) error {
	workspace := c.Query("workspace")
	name := c.Params("name")

	if workspace == "" || name == "" {
		return http.Err(c, http.BadRequest.Code, "workspace and name are required")
	}

	setting, err := rt.Services.Setting.GetSetting(c.Context(), workspace, name)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, setting)
}

// upsertSetting creates or updates a setting.
func (rt *Router) upsertSetting(c *fiber.Ctx) error {
	workspace := c.Query("workspace")
	name := c.Params("name")

	if workspace == "" || name == "" {
		return http.Err(c, http.BadRequest.Code, "workspace and name are required")
	}

	var setting model.Setting
	if err := c.BodyParser(&setting); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	setting.Workspace = workspace
	setting.Name = name

	if err := rt.Services.Setting.UpsertSetting(c.Context(), &setting); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, setting)
}

// deleteSetting removes a setting by workspace and name.
func (rt *Router) deleteSetting(c *fiber.Ctx) error {
	workspace := c.Query("workspace")
	name := c.Params("name")

	if workspace == "" || name == "" {
		return http.Err(c, http.BadRequest.Code, "workspace and name are required")
	}

	if err := rt.Services.Setting.DeleteSetting(c.Context(), workspace, name); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}
