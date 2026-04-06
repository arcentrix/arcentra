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
	"github.com/arcentrix/arcentra/internal/case/agent"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) storageRoutes(r fiber.Router, auth fiber.Handler) {
	storageGroup := r.Group("/storage", auth)
	storageGroup.Post("/upload", rt.uploadFile)
	storageGroup.Post("/upload/:storageID", rt.uploadFileWithStorage)
	storageGroup.Post("/configs", rt.createStorageConfig)
	storageGroup.Get("/configs", rt.listStorageConfigs)
	storageGroup.Get("/configs/default", rt.getDefaultStorageConfig)
	storageGroup.Get("/configs/:id", rt.getStorageConfig)
	storageGroup.Put("/configs/:id", rt.updateStorageConfig)
	storageGroup.Delete("/configs/:id", rt.deleteStorageConfig)
	storageGroup.Post("/configs/:id/default", rt.setDefaultStorageConfig)
}

func (rt *Router) uploadFile(c *fiber.Ctx) error {
	file, err := c.FormFile("file")
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "file is required")
	}

	customPath := c.Query("path")
	contentType := file.Header.Get("Content-Type")

	response, err := rt.UploadUC.Execute(c.Context(), agent.UploadFileInput{
		CustomPath:  customPath,
		ContentType: contentType,
	}, file)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, response)
}

func (rt *Router) uploadFileWithStorage(c *fiber.Ctx) error {
	storageID := c.Params("storageID")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storageID is required")
	}

	file, err := c.FormFile("file")
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "file is required")
	}

	customPath := c.Query("path")
	contentType := file.Header.Get("Content-Type")

	response, err := rt.UploadUC.Execute(c.Context(), agent.UploadFileInput{
		StorageID:   storageID,
		CustomPath:  customPath,
		ContentType: contentType,
	}, file)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, response)
}

func (rt *Router) createStorageConfig(c *fiber.Ctx) error {
	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	config, err := rt.ManageSettings.CreateStorageConfig(c.Context(), req)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, config)
}

func (rt *Router) listStorageConfigs(c *fiber.Ctx) error {
	configs, err := rt.ManageSettings.ListStorageConfigs(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, configs)
}

func (rt *Router) getStorageConfig(c *fiber.Ctx) error {
	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	config, err := rt.ManageSettings.GetStorageConfig(c.Context(), storageID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, config)
}

func (rt *Router) updateStorageConfig(c *fiber.Ctx) error {
	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}
	req["storageID"] = storageID

	config, err := rt.ManageSettings.UpdateStorageConfig(c.Context(), req)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, config)
}

func (rt *Router) deleteStorageConfig(c *fiber.Ctx) error {
	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	if err := rt.ManageSettings.DeleteStorageConfig(c.Context(), storageID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{"id": storageID})
}

func (rt *Router) setDefaultStorageConfig(c *fiber.Ctx) error {
	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	if err := rt.ManageSettings.SetDefaultStorageConfig(c.Context(), storageID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{"id": storageID})
}

func (rt *Router) getDefaultStorageConfig(c *fiber.Ctx) error {
	config, err := rt.ManageSettings.GetDefaultStorageConfig(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, config)
}
