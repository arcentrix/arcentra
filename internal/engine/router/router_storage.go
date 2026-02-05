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
	"github.com/arcentrix/arcentra/internal/engine/service"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

// storageRouter registers storage related routes
func (rt *Router) storageRouter(r fiber.Router, auth fiber.Handler) {
	storageGroup := r.Group("/storage", auth)
	{
		// File upload routes
		storageGroup.Post("/upload", rt.uploadFile)                       // POST /storage/upload - upload file to default storage
		storageGroup.Post("/upload/:storageId", rt.uploadFileWithStorage) // POST /storage/upload/:storageId - upload file to specific storage

		// Storage configuration routes
		storageGroup.Post("/configs", rt.createStorageConfig)                 // POST /storage/configs - create storage config
		storageGroup.Get("/configs", rt.listStorageConfigs)                   // GET /storage/configs - list storage configs
		storageGroup.Get("/configs/default", rt.getDefaultStorageConfig)      // GET /storage/configs/default - get default storage config
		storageGroup.Get("/configs/:id", rt.getStorageConfig)                 // GET /storage/configs/:id - get storage config
		storageGroup.Put("/configs/:id", rt.updateStorageConfig)              // PUT /storage/configs/:id - update storage config
		storageGroup.Delete("/configs/:id", rt.deleteStorageConfig)           // DELETE /storage/configs/:id - delete storage config
		storageGroup.Post("/configs/:id/default", rt.setDefaultStorageConfig) // POST /storage/configs/:id/default - set default storage config
	}
}

// uploadFile uploads a file to default storage
func (rt *Router) uploadFile(c *fiber.Ctx) error {
	uploadService := rt.Services.Upload

	// get file from form
	file, err := c.FormFile("file")
	if err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "file is required", c.Path())
	}

	// get optional custom path from query parameter
	customPath := c.Query("path")

	// upload file
	response, err := uploadService.UploadFile(file, "", customPath)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, response)
	c.Locals(middleware.OPERATION, "upload file")
	return nil
}

// uploadFileWithStorage uploads a file to specific storage
func (rt *Router) uploadFileWithStorage(c *fiber.Ctx) error {
	uploadService := rt.Services.Upload

	// get storage ID from path parameter
	storageId := c.Params("storageId")
	if storageId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "storageId is required", c.Path())
	}

	// get file from form
	file, err := c.FormFile("file")
	if err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "file is required", c.Path())
	}

	// get optional custom path from query parameter
	customPath := c.Query("path")

	// upload file
	response, err := uploadService.UploadFile(file, storageId, customPath)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, response)
	c.Locals(middleware.OPERATION, "upload file")
	return nil
}

// createStorageConfig creates a new storage configuration
func (rt *Router) createStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	var req service.CreateStorageConfigRequest
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	storageConfig, err := storageService.CreateStorageConfig(&req)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, storageConfig)
	c.Locals(middleware.OPERATION, "create storage config")
	return nil
}

// listStorageConfigs gets storage configuration list
func (rt *Router) listStorageConfigs(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageConfigs, err := storageService.ListStorageConfigs()
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, storageConfigs)
	c.Locals(middleware.OPERATION, "list storage configs")
	return nil
}

// getStorageConfig gets a storage configuration by ID
func (rt *Router) getStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "storage id is required", c.Path())
	}

	storageConfig, err := storageService.GetStorageConfig(storageID)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, storageConfig)
	c.Locals(middleware.OPERATION, "get storage config")
	return nil
}

// updateStorageConfig updates a storage configuration
func (rt *Router) updateStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "storage id is required", c.Path())
	}

	var req service.UpdateStorageConfigRequest
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	req.StorageId = storageID
	storageConfig, err := storageService.UpdateStorageConfig(&req)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, storageConfig)
	c.Locals(middleware.OPERATION, "update storage config")
	return nil
}

// deleteStorageConfig deletes a storage configuration
func (rt *Router) deleteStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "storage id is required", c.Path())
	}

	err := storageService.DeleteStorageConfig(storageID)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]interface{}{"id": storageID})
	c.Locals(middleware.OPERATION, "delete storage config")
	return nil
}

// setDefaultStorageConfig sets a storage configuration as default
func (rt *Router) setDefaultStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageID := c.Params("id")
	if storageID == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "storage id is required", c.Path())
	}

	err := storageService.SetDefaultStorageConfig(storageID)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, map[string]interface{}{"id": storageID})
	c.Locals(middleware.OPERATION, "set default storage config")
	return nil
}

// getDefaultStorageConfig gets the default storage configuration
func (rt *Router) getDefaultStorageConfig(c *fiber.Ctx) error {
	storageService := rt.Services.Storage

	storageConfig, err := storageService.GetDefaultStorageConfig()
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, storageConfig)
	c.Locals(middleware.OPERATION, "get default storage config")
	return nil
}
