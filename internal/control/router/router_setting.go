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
	"errors"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// settingRouter registers setting routes.
func (rt *Router) settingRouter(r fiber.Router, auth fiber.Handler) {
	settingsGroup := r.Group("/settings", sensitiveHTTPStatus, auth, rt.liveSensitiveUser(), rt.platformAccess(consts.PermOrgUpdate))
	{
		settingsGroup.Get("/", rt.listSettings)                                                      // GET /settings - list every setting
		settingsGroup.Get("/:name", rt.getSetting)                                                   // GET /settings/:name - get a single setting
		settingsGroup.Put("/:name", rt.upsertSetting)                                                // PUT /settings/:name - create or update a setting
		settingsGroup.Delete("/:name", rt.platformAccess(consts.PermSecretDelete), rt.deleteSetting) // DELETE /settings/:name - delete a setting
	}
}

// listSettings returns every setting in the table.
func (rt *Router) listSettings(c *fiber.Ctx) error {
	settings, err := rt.Services.Setting.ListSettings(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	views := make([]settingView, 0, len(settings))
	for _, item := range settings {
		if item != nil {
			views = append(views, publicSetting(item))
		}
	}
	return http.Detail(c, views)
}

// getSetting returns a single setting by workspace and name.
func (rt *Router) getSetting(c *fiber.Ctx) error {
	name := c.Params("name")

	if name == "" {
		return http.Err(c, http.BadRequest.Code, "name is required")
	}

	setting, err := rt.Services.Setting.GetSetting(c.Context(), name)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, publicSetting(setting))
}

// upsertSetting creates or updates a setting.
func (rt *Router) upsertSetting(c *fiber.Ctx) error {
	name := c.Params("name")

	if name == "" {
		return http.Err(c, http.BadRequest.Code, "name is required")
	}

	var setting model.Setting
	if err := c.BodyParser(&setting); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	if setting.Name != "" && setting.Name != name {
		return sensitiveError(c, 400, "setting name must match the path")
	}
	setting.Name = name
	existing, err := rt.Services.Repos().Setting.Get(c.UserContext(), name)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return sensitiveError(c, 503, "configuration is unavailable")
	}
	var previous []byte
	if existing != nil && err == nil {
		previous = existing.Value
	}
	merged, secretInput, err := mergeConfigSecrets(previous, setting.Value, secretField(name))
	if err != nil {
		return sensitiveError(c, 400, err.Error())
	}
	if secretInput {
		if err := rt.requirePlatform(c, consts.PermSecretWrite); err != nil || c.Response().StatusCode() != 200 {
			return err
		}
	}
	setting.Value = merged
	if err := rt.Services.Setting.UpsertSetting(c.Context(), &setting); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, publicSetting(&setting))
}

// deleteSetting removes a setting by name.
func (rt *Router) deleteSetting(c *fiber.Ctx) error {
	name := c.Params("name")

	if name == "" {
		return http.Err(c, http.BadRequest.Code, "name is required")
	}

	if err := rt.Services.Setting.DeleteSetting(c.Context(), name); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Operation(c)
}
