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
	"strconv"

	"github.com/arcentrix/arcentra/internal/domain/project"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) generalSettingsRoutes(r fiber.Router, auth fiber.Handler) {
	settingsGroup := r.Group("/general-settings")
	settingsGroup.Get("/", auth, rt.getGeneralSettingsList)
	settingsGroup.Get("/categories", auth, rt.getCategories)
	settingsGroup.Get("/:settingsId", auth, rt.getGeneralSettings)
	settingsGroup.Put("/:settingsId", auth, rt.updateGeneralSettings)
	settingsGroup.Get("/by-name/:category/:name", auth, rt.getGeneralSettingsByName)
}

func (rt *Router) updateGeneralSettings(c *fiber.Ctx) error {
	settingsID := c.Params("settingsId")
	if settingsID == "" {
		return http.Err(c, http.BadRequest.Code, "invalid settings id")
	}

	var settings project.GeneralSettings
	if err := c.BodyParser(&settings); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	settings.SettingsID = settingsID

	if err := rt.ManageSettings.UpdateSettings(c.Context(), &settings); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, settings)
}

func (rt *Router) getGeneralSettings(c *fiber.Ctx) error {
	settingsID := c.Params("settingsId")
	if settingsID == "" {
		return http.Err(c, http.BadRequest.Code, "invalid settings id")
	}

	settings, err := rt.ManageSettings.GetSettings(c.Context(), settingsID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, settings)
}

func (rt *Router) getGeneralSettingsByName(c *fiber.Ctx) error {
	category := c.Params("category")
	name := c.Params("name")

	if category == "" || name == "" {
		return http.Err(c, http.BadRequest.Code, "category and name are required")
	}

	settings, err := rt.ManageSettings.GetSettingsByName(c.Context(), category, name)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, settings)
}

func (rt *Router) getGeneralSettingsList(c *fiber.Ctx) error {
	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))
	category := c.Query("category", "")

	settingsList, total, err := rt.ManageSettings.ListSettings(c.Context(), pageNum, pageSize, category)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"list":     settingsList,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) getCategories(c *fiber.Ctx) error {
	categories, err := rt.ManageSettings.GetCategories(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{"categories": categories})
}
