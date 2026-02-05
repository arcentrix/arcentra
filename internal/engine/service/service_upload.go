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

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"path/filepath"
	"slices"
	"strings"
	"time"

	storagemodel "github.com/arcentrix/arcentra/internal/engine/model"
	storagerepo "github.com/arcentrix/arcentra/internal/engine/repo"
	storagepkg "github.com/arcentrix/arcentra/internal/pkg/storage"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
)

type UploadService struct {
	storageRepo storagerepo.IStorageRepository
}

func NewUploadService(storageRepo storagerepo.IStorageRepository) *UploadService {
	return &UploadService{
		storageRepo: storageRepo,
	}
}

// buildCompleteURL builds complete URL from object path and storage config
// Format: {protocol}://{bucket}.{endpoint}{basePath}/{ObjectName}
func (us *UploadService) buildCompleteURL(objectPath string, storageConfig *storagemodel.StorageConfig) string {
	if objectPath == "" {
		return ""
	}

	// parse storage config
	var configDetail storagemodel.StorageConfigDetail
	if err := json.Unmarshal(storageConfig.Config, &configDetail); err != nil {
		log.Warnw("failed to unmarshal storage config", "error", err)
		return objectPath
	}

	// determine protocol based on useTLS
	protocol := "http"
	if configDetail.UseTLS {
		protocol = "https"
	}

	// build complete URL: {protocol}://{bucket}.{endpoint}{basePath}/{ObjectName}
	endpoint := strings.TrimSuffix(configDetail.Endpoint, "/")
	bucket := configDetail.Bucket
	basePath := strings.Trim(configDetail.BasePath, "/")
	path := strings.TrimPrefix(objectPath, "/")

	// construct URL path
	var urlPath string
	if basePath != "" && basePath != "/" {
		urlPath = fmt.Sprintf("%s/%s", basePath, path)
	} else {
		urlPath = path
	}

	// use virtual hosted-style URL: bucket.endpoint/path
	return fmt.Sprintf("%s://%s.%s/%s", protocol, bucket, endpoint, urlPath)
}

const (
	userAvatarPath = "avatars"       // avatar storage path
	maxAvatarSize  = 5 * 1024 * 1024 // 5MB for avatar
)

var maxFileSize = int64(100 * 1024 * 1024) // 100MB for general files

// getStorageProvider gets storage configuration and provider
func (us *UploadService) getStorageProvider(storageId string) (*storagemodel.StorageConfig, storagepkg.StorageProvider, error) {
	// get storage config
	var storageConfig *storagemodel.StorageConfig
	var err error
	if storageId != "" {
		storageConfig, err = us.storageRepo.GetStorageConfigByID(storageId)
	} else {
		storageConfig, err = us.storageRepo.GetDefaultStorageConfig()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get storage config: %w", err)
	}

	// check if storage is enabled
	if storageConfig.IsEnabled != 1 {
		return nil, nil, fmt.Errorf("storage config is disabled")
	}

	// create storage provider
	storageProvider, err := storagepkg.NewStorageDBProvider(us.storageRepo)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create storage provider: %w", err)
	}

	// switch to specific storage if provided
	if storageId != "" {
		if err := storageProvider.SwitchStorageConfig(storageId); err != nil {
			return nil, nil, fmt.Errorf("failed to switch storage config: %w", err)
		}
	}

	// get provider instance
	provider, err := storageProvider.GetStorageProvider()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get storage provider: %w", err)
	}

	return storageConfig, provider, nil
}

// uploadToStorage uploads file to storage and returns response
func (us *UploadService) uploadToStorage(file *multipart.FileHeader, storageId, objectPath, contentType string) (*UploadResponse, error) {
	if file == nil {
		return nil, fmt.Errorf("file is required")
	}
	if file.Size == 0 {
		return nil, fmt.Errorf("file size cannot be zero")
	}

	// get storage config and provider
	storageConfig, provider, err := us.getStorageProvider(storageId)
	if err != nil {
		return nil, err
	}

	// upload file
	uploadedPath, err := provider.PutObject(context.Background(), objectPath, file, contentType)
	if err != nil {
		log.Errorw("failed to upload file", "objectPath", objectPath, "error", err)
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	log.Infow("file uploaded successfully", "uploadedPath", uploadedPath, "size", file.Size)

	// build complete URL
	fileURL := us.buildCompleteURL(uploadedPath, storageConfig)

	return &UploadResponse{
		ObjectName:   uploadedPath,
		FileURL:      fileURL,
		OriginalName: file.Filename,
		Size:         file.Size,
		ContentType:  contentType,
		StorageId:    storageConfig.StorageId,
		StorageType:  storageConfig.StorageType,
		UploadTime:   time.Now(),
	}, nil
}

// UploadFile uploads a file to object storage
// storageId: optional, if empty, use default storage
// customPath: optional custom path, if empty, use default path structure
func (us *UploadService) UploadFile(file *multipart.FileHeader, storageId string, customPath string) (*UploadResponse, error) {
	// validate file size (max 100MB for general files)
	if file.Size > maxFileSize {
		return nil, fmt.Errorf("file size exceeds maximum limit of %d bytes", maxFileSize)
	}

	// generate object name
	objectName := us.generateObjectName(file.Filename, customPath)

	// get content type
	contentType := file.Header.Get("Content-Type")
	if contentType == "" {
		contentType = getContentType(file.Filename)
	}

	// upload to storage
	return us.uploadToStorage(file, storageId, objectName, contentType)
}

// generateObjectName generates a unique object name
func (us *UploadService) generateObjectName(originalName string, customPath string) string {
	// get file extension
	ext := filepath.Ext(originalName)

	// generate unique ID for filename
	uniqueId := id.GetUUID()

	// sanitize original name (remove extension)
	nameWithoutExt := strings.TrimSuffix(originalName, ext)
	nameWithoutExt = sanitizeFileName(nameWithoutExt)

	// construct filename
	fileName := fmt.Sprintf("%s_%s%s", nameWithoutExt, uniqueId, ext)

	// construct full path
	if customPath != "" {
		// use custom path
		customPath = strings.Trim(customPath, "/")
		return fmt.Sprintf("%s/%s", customPath, fileName)
	}

	// use default path: uploads/YYYY/MM/DD/filename
	now := time.Now()
	datePath := fmt.Sprintf("uploads/%04d/%02d/%02d", now.Year(), now.Month(), now.Day())
	return fmt.Sprintf("%s/%s", datePath, fileName)
}

// sanitizeFileName removes invalid characters from filename
func sanitizeFileName(name string) string {
	// replace spaces with underscores
	name = strings.ReplaceAll(name, " ", "_")

	// remove invalid characters
	invalidChars := []string{"/", "\\", "..", "<", ">", ":", "\"", "|", "?", "*"}
	for _, char := range invalidChars {
		name = strings.ReplaceAll(name, char, "")
	}

	// limit length
	if len(name) > 50 {
		name = name[:50]
	}

	return name
}

// getContentType gets content type from file extension
func getContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))

	contentTypes := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
		".pdf":  "application/pdf",
		".doc":  "application/msword",
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xls":  "application/vnd.ms-excel",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		".zip":  "application/zip",
		".txt":  "text/plain",
		".json": "application/json",
		".xml":  "application/xml",
		".csv":  "text/csv",
	}

	if contentType, ok := contentTypes[ext]; ok {
		return contentType
	}

	return "application/octet-stream"
}

// UploadAvatar uploads user avatar image
func (us *UploadService) UploadAvatar(file *multipart.FileHeader, userId string) (*UploadResponse, error) {
	// validate file size (max 5MB for avatar)
	if file.Size > maxAvatarSize {
		return nil, fmt.Errorf("avatar size exceeds maximum limit of 5MB")
	}

	// get and validate content type
	contentType := file.Header.Get("Content-Type")
	if contentType == "" {
		contentType = getContentType(file.Filename)
	}

	// validate image type
	allowedTypes := []string{"image/jpeg", "image/jpg", "image/png", "image/gif", "image/webp"}
	isValid := slices.Contains(allowedTypes, contentType)
	if !isValid {
		return nil, fmt.Errorf("invalid image type, only jpeg, png, gif, and webp are allowed")
	}

	// generate avatar path: avatars/userId/uuid.ext
	ext := filepath.Ext(file.Filename)
	objectPath := fmt.Sprintf("%s/%s/%s%s", userAvatarPath, userId, id.GetUUID(), ext)

	// upload to storage
	return us.uploadToStorage(file, "", objectPath, contentType)
}

// UploadResponse response for file upload
type UploadResponse struct {
	ObjectName   string    `json:"objectName,omitempty"` // relative path in storage (optional, for internal use)
	FileURL      string    `json:"fileUrl"`              // complete static URL for database storage and frontend access
	OriginalName string    `json:"originalName"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"contentType"`
	StorageId    string    `json:"storageId"`
	StorageType  string    `json:"storageType"`
	UploadTime   time.Time `json:"uploadTime"`
}
