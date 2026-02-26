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

package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/arcentrix/arcentra/internal/engine/model"
	storagerepo "github.com/arcentrix/arcentra/internal/engine/repo"
)

type StorageService struct {
	storageRepo storagerepo.IStorageRepository
}

func NewStorageService(storageRepo storagerepo.IStorageRepository) *StorageService {
	return &StorageService{
		storageRepo: storageRepo,
	}
}

// CreateStorageConfig creates storage config.
func (ss *StorageService) CreateStorageConfig(ctx context.Context, req *CreateStorageConfigRequest) (*model.StorageConfig, error) {
	if err := ss.validateStorageConfig(req.StorageType, req.Config); err != nil {
		return nil, fmt.Errorf("invalid storage config: %w", err)
	}

	storageConfig := &model.StorageConfig{
		StorageId:   req.StorageId,
		Name:        req.Name,
		StorageType: req.StorageType,
		Config:      req.Config,
		Description: req.Description,
		IsDefault:   req.IsDefault,
		IsEnabled:   1,
	}

	if err := ss.storageRepo.Create(ctx, storageConfig); err != nil {
		return nil, fmt.Errorf("failed to create storage config: %w", err)
	}

	if req.IsDefault == 1 {
		if err := ss.storageRepo.SetDefault(ctx, req.StorageId); err != nil {
			return nil, fmt.Errorf("failed to set as default storage config: %w", err)
		}
	}

	return storageConfig, nil
}

// UpdateStorageConfig updates storage config.
func (ss *StorageService) UpdateStorageConfig(ctx context.Context, req *UpdateStorageConfigRequest) (*model.StorageConfig, error) {
	if err := ss.validateStorageConfig(req.StorageType, req.Config); err != nil {
		return nil, fmt.Errorf("invalid storage config: %w", err)
	}

	storageConfig := &model.StorageConfig{
		StorageId:   req.StorageId,
		Name:        req.Name,
		StorageType: req.StorageType,
		Config:      req.Config,
		Description: req.Description,
		IsDefault:   req.IsDefault,
		IsEnabled:   req.IsEnabled,
	}

	if err := ss.storageRepo.Update(ctx, storageConfig); err != nil {
		return nil, fmt.Errorf("failed to update storage config: %w", err)
	}

	if req.IsDefault == 1 {
		if err := ss.storageRepo.SetDefault(ctx, req.StorageId); err != nil {
			return nil, fmt.Errorf("failed to set as default storage config: %w", err)
		}
	}

	return storageConfig, nil
}

// GetStorageConfig returns storage config by storageId.
func (ss *StorageService) GetStorageConfig(ctx context.Context, storageId string) (*model.StorageConfig, error) {
	return ss.storageRepo.Get(ctx, storageId)
}

// GetDefaultStorageConfig returns default storage config.
func (ss *StorageService) GetDefaultStorageConfig(ctx context.Context) (*model.StorageConfig, error) {
	return ss.storageRepo.GetDefault(ctx)
}

// ListStorageConfigs returns enabled storage configs.
func (ss *StorageService) ListStorageConfigs(ctx context.Context) ([]model.StorageConfig, error) {
	return ss.storageRepo.ListEnabled(ctx)
}

// DeleteStorageConfig deletes storage config.
func (ss *StorageService) DeleteStorageConfig(ctx context.Context, storageId string) error {
	return ss.storageRepo.Delete(ctx, storageId)
}

// SetDefaultStorageConfig sets default storage config.
func (ss *StorageService) SetDefaultStorageConfig(ctx context.Context, storageId string) error {
	return ss.storageRepo.SetDefault(ctx, storageId)
}

// validateStorageConfig 验证存储配置
func (ss *StorageService) validateStorageConfig(storageType string, configJSON []byte) error {
	switch storageType {
	case "minio":
		var config model.MinIOConfig
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return fmt.Errorf("invalid MinIO config: %w", err)
		}
		return ss.validateMinIOConfig(&config)
	case "s3":
		var config model.S3Config
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return fmt.Errorf("invalid S3 config: %w", err)
		}
		return ss.validateS3Config(&config)
	case "oss":
		var config model.OSSConfig
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return fmt.Errorf("invalid OSS config: %w", err)
		}
		return ss.validateOSSConfig(&config)
	case "gcs":
		var config model.GCSConfig
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return fmt.Errorf("invalid GCS config: %w", err)
		}
		return ss.validateGCSConfig(&config)
	case "cos":
		var config model.COSConfig
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return fmt.Errorf("invalid COS config: %w", err)
		}
		return ss.validateCOSConfig(&config)
	default:
		return fmt.Errorf("unsupported storage type: %s", storageType)
	}
}

// validateMinIOConfig 验证 MinIO 配置
func (ss *StorageService) validateMinIOConfig(config *model.MinIOConfig) error {
	if config.Endpoint == "" {
		return fmt.Errorf("MinIO endpoint is required")
	}
	if config.AccessKey == "" {
		return fmt.Errorf("MinIO access key is required")
	}
	if config.SecretKey == "" {
		return fmt.Errorf("MinIO secret key is required")
	}
	if config.Bucket == "" {
		return fmt.Errorf("MinIO bucket is required")
	}
	return nil
}

// validateS3Config 验证 S3 配置
func (ss *StorageService) validateS3Config(config *model.S3Config) error {
	if config.Endpoint == "" {
		return fmt.Errorf("s3 endpoint is required")
	}
	if config.AccessKey == "" {
		return fmt.Errorf("s3 access key is required")
	}
	if config.SecretKey == "" {
		return fmt.Errorf("s3 secret key is required")
	}
	if config.Bucket == "" {
		return fmt.Errorf("s3 bucket is required")
	}
	return nil
}

// validateOSSConfig 验证 OSS 配置
func (ss *StorageService) validateOSSConfig(config *model.OSSConfig) error {
	if config.Endpoint == "" {
		return fmt.Errorf("OSS endpoint is required")
	}
	if config.AccessKey == "" {
		return fmt.Errorf("OSS access key is required")
	}
	if config.SecretKey == "" {
		return fmt.Errorf("OSS secret key is required")
	}
	if config.Bucket == "" {
		return fmt.Errorf("OSS bucket is required")
	}
	return nil
}

// validateGCSConfig 验证 GCS 配置
func (ss *StorageService) validateGCSConfig(config *model.GCSConfig) error {
	if config.AccessKey == "" {
		return fmt.Errorf("GCS service account key is required")
	}
	if config.Bucket == "" {
		return fmt.Errorf("GCS bucket is required")
	}
	return nil
}

// validateCOSConfig 验证 COS 配置
func (ss *StorageService) validateCOSConfig(config *model.COSConfig) error {
	if config.Endpoint == "" {
		return fmt.Errorf("COS endpoint is required")
	}
	if config.AccessKey == "" {
		return fmt.Errorf("COS access key is required")
	}
	if config.SecretKey == "" {
		return fmt.Errorf("COS secret key is required")
	}
	if config.Bucket == "" {
		return fmt.Errorf("COS bucket is required")
	}
	return nil
}

type CreateStorageConfigRequest struct {
	StorageId   string `json:"storageId" binding:"required"`
	Name        string `json:"name" binding:"required"`
	StorageType string `json:"storageType" binding:"required"`
	Config      []byte `json:"config" binding:"required"`
	Description string `json:"description"`
	IsDefault   int    `json:"isDefault"`
}

type UpdateStorageConfigRequest struct {
	StorageId   string `json:"storageId" binding:"required"`
	Name        string `json:"name"`
	StorageType string `json:"storageType"`
	Config      []byte `json:"config"`
	Description string `json:"description"`
	IsDefault   int    `json:"isDefault"`
	IsEnabled   int    `json:"isEnabled"`
}
