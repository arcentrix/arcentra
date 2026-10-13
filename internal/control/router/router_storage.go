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
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

// storageRouter registers storage related routes
func (rt *Router) storageRouter(r fiber.Router, auth fiber.Handler) {
	storageGroup := r.Group("/storage", sensitiveHTTPStatus, auth, rt.liveSensitiveUser(), rt.platformAccess(consts.PermOrgUpdate))
	{
		// File upload routes
		storageGroup.Post("/upload", rt.platformAccess(consts.PermSecretWrite), rt.uploadFile)                       // POST /storage/upload - upload file to default storage
		storageGroup.Post("/upload/:storageID", rt.platformAccess(consts.PermSecretWrite), rt.uploadFileWithStorage) // POST /storage/upload/:storageID - upload file to specific storage

		// Storage configuration routes
		storageGroup.Post("/configs", rt.platformAccess(consts.PermSecretWrite), rt.createStorageConfig)                 // POST /storage/configs - create storage config
		storageGroup.Get("/configs", rt.listStorageConfigs)                                                              // GET /storage/configs - list storage configs
		storageGroup.Get("/configs/default", rt.getDefaultStorageConfig)                                                 // GET /storage/configs/default - get default storage config
		storageGroup.Get("/configs/:id", rt.getStorageConfig)                                                            // GET /storage/configs/:id - get storage config
		storageGroup.Put("/configs/:id", rt.platformAccess(consts.PermSecretWrite), rt.updateStorageConfig)              // PUT /storage/configs/:id - update storage config
		storageGroup.Delete("/configs/:id", rt.platformAccess(consts.PermSecretDelete), rt.deleteStorageConfig)          // DELETE /storage/configs/:id - delete storage config
		storageGroup.Post("/configs/:id/default", rt.platformAccess(consts.PermSecretWrite), rt.setDefaultStorageConfig) // POST /storage/configs/:id/default - set default storage config
	}
}

// uploadFile uploads a file to default storage
func (rt *Router) uploadFile(c *fiber.Ctx) error {
	uploadService := rt.Services.Upload

	// get file from form
	file, err := c.FormFile("file")
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "file is required")
	}

	// get optional custom path from query parameter
	customPath := c.Query("path")

	// upload file
	response, err := uploadService.UploadFile(c.Context(), file, "", customPath)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, response)
}

// uploadFileWithStorage uploads a file to specific storage
func (rt *Router) uploadFileWithStorage(c *fiber.Ctx) error {
	uploadService := rt.Services.Upload

	// get storage ID from path parameter
	storageID := c.Params("storageID")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storageID is required")
	}

	// get file from form
	file, err := c.FormFile("file")
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "file is required")
	}

	// get optional custom path from query parameter
	customPath := c.Query("path")

	// upload file
	response, err := uploadService.UploadFile(c.Context(), file, storageID, customPath)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, response)
}

// createStorageConfig creates a new storage configuration
func (rt *Router) createStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	var req service.CreateStorageConfigRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	merged, _, mergeErr := mergeConfigSecrets(nil, req.Config, false)
	if mergeErr != nil {
		return sensitiveError(c, 400, mergeErr.Error())
	}
	if err := validateStorageEndpoint(merged); err != nil {
		return sensitiveError(c, 400, err.Error())
	}
	req.Config = merged
	storageConfig, err := storageService.CreateStorageConfig(c.Context(), &req)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, publicStorage(storageConfig))
}

// listStorageConfigs gets storage configuration list
func (rt *Router) listStorageConfigs(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageConfigs, err := storageService.ListStorageConfigs(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	views := make([]storageConfigView, 0, len(storageConfigs))
	for i := range storageConfigs {
		views = append(views, publicStorage(&storageConfigs[i]))
	}
	return http.Detail(c, views)
}

// getStorageConfig gets a storage configuration by ID
func (rt *Router) getStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	storageConfig, err := storageService.GetStorageConfig(c.Context(), storageID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, publicStorage(storageConfig))
}

// updateStorageConfig updates a storage configuration
func (rt *Router) updateStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	var req service.UpdateStorageConfigRequest
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	existing, err := storageService.GetStorageConfig(c.UserContext(), storageID)
	if err != nil {
		return sensitiveError(c, 404, "storage configuration not found")
	}
	if req.StorageID != "" && req.StorageID != storageID {
		return sensitiveError(c, 400, "storage id must match the path")
	}
	if req.StorageType == "" {
		req.StorageType = existing.StorageType
	}
	if req.StorageType != existing.StorageType {
		return sensitiveError(c, 400, "create a new configuration to change storage provider")
	}
	merged, _, mergeErr := mergeConfigSecrets(existing.Config, req.Config, false)
	if mergeErr != nil {
		return sensitiveError(c, 400, mergeErr.Error())
	}
	if err := validateStorageEndpoint(merged); err != nil {
		return sensitiveError(c, 400, err.Error())
	}
	req.Config = merged
	req.StorageID = storageID
	storageConfig, err := storageService.UpdateStorageConfig(c.Context(), &req)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, publicStorage(storageConfig))
}

// deleteStorageConfig deletes a storage configuration
func (rt *Router) deleteStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	err := storageService.DeleteStorageConfig(c.Context(), storageID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]interface{}{"id": storageID})
}

// setDefaultStorageConfig sets a storage configuration as default
func (rt *Router) setDefaultStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.Err(c, http.BadRequest.Code, "storage id is required")
	}

	err := storageService.SetDefaultStorageConfig(c.Context(), storageID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]interface{}{"id": storageID})
}

// getDefaultStorageConfig gets the default storage configuration
func (rt *Router) getDefaultStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageConfig, err := storageService.GetDefaultStorageConfig(c.Context())
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, publicStorage(storageConfig))
}
