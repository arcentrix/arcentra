// Copyright 2025 Arcentra Team
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
	"strconv"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

// generalSettingsRouter registers general settings related routes
func (rt *Router) generalSettingsRouter(r fiber.Router, auth fiber.Handler) {
	settingsGroup := r.Group("/general-settings")
	{
		// General settings routes (authentication required)
		// Note: General settings are pre-defined system configurations, only updates are allowed
		settingsGroup.Get("/", auth, rt.getGeneralSettingsList)                          // GET /general-settings - list general settings
		settingsGroup.Get("/categories", auth, rt.getCategories)                         // GET /general-settings/categories - get all categories
		settingsGroup.Get("/:settingsId", auth, rt.getGeneralSettings)                   // GET /general-settings/:settingsId - get general settings
		settingsGroup.Put("/:settingsId", auth, rt.updateGeneralSettings)                // PUT /general-settings/:settingsId - update general settings
		settingsGroup.Get("/by-name/:category/:name", auth, rt.getGeneralSettingsByName) // GET /general-settings/by-name/:category/:name - get by name
	}
}

// updateGeneralSettings updates a general settings
func (rt *Router) updateGeneralSettings(c *fiber.Ctx) error {
	generalSettingsService := rt.Services.GeneralSettings

	// get settings ID from path parameter
	settingsId := c.Params("settingsId")
	if settingsId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid settings id", c.Path())
	}

	var settings model.GeneralSettings
	if err := c.BodyParser(&settings); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	if err := generalSettingsService.UpdateGeneralSettings(settingsId, &settings); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, settings)
	c.Locals(middleware.OPERATION, "update general settings")
	return nil
}

// getGeneralSettings gets a general settings by settings ID
func (rt *Router) getGeneralSettings(c *fiber.Ctx) error {
	generalSettingsService := rt.Services.GeneralSettings

	// get settings ID from path parameter
	settingsId := c.Params("settingsId")
	if settingsId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid settings id", c.Path())
	}

	settings, err := generalSettingsService.GetGeneralSettingsByID(settingsId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, settings)
	c.Locals(middleware.OPERATION, "get general settings")
	return nil
}

// getGeneralSettingsByName gets a general settings by category and name
func (rt *Router) getGeneralSettingsByName(c *fiber.Ctx) error {
	generalSettingsService := rt.Services.GeneralSettings

	category := c.Params("category")
	name := c.Params("name")

	if category == "" || name == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "category and name are required", c.Path())
	}

	settings, err := generalSettingsService.GetGeneralSettingsByName(category, name)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, settings)
	c.Locals(middleware.OPERATION, "get general settings by name")
	return nil
}

// getGeneralSettingsList gets general settings list with pagination and filters
func (rt *Router) getGeneralSettingsList(c *fiber.Ctx) error {
	generalSettingsService := rt.Services.GeneralSettings

	// get query parameters
	pageNum, _ := strconv.Atoi(c.Query("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))
	category := c.Query("category", "")

	settingsList, total, err := generalSettingsService.GetGeneralSettingsList(pageNum, pageSize, category)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// construct response
	response := map[string]interface{}{
		"list":     settingsList,
		"total":    total,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	}

	c.Locals(middleware.DETAIL, response)
	c.Locals(middleware.OPERATION, "get general settings list")
	return nil
}

// getCategories gets all distinct categories
func (rt *Router) getCategories(c *fiber.Ctx) error {
	generalSettingsService := rt.Services.GeneralSettings

	categories, err := generalSettingsService.GetCategories()
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]interface{}{"categories": categories})
	c.Locals(middleware.OPERATION, "get categories")
	return nil
}
