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

package repo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
)

type StorageRepo struct {
	database.IDatabase
	cache.ICache
}

const (
	// Redis 缓存键前缀
	storageConfigCacheKeyPrefix  = "storage:config:"
	storageDefaultConfigCacheKey = "storage:config:default"

	// 缓存过期时间（1小时）
	storageConfigCacheTTL = 24 * time.Hour
)

type IStorageRepository interface {
	GetDefaultStorageConfig() (*model.StorageConfig, error)
	GetStorageConfigByID(storageID string) (*model.StorageConfig, error)
	GetEnabledStorageConfigs() ([]model.StorageConfig, error)
	GetStorageConfigByType(storageType string) ([]model.StorageConfig, error)
	CreateStorageConfig(storageConfig *model.StorageConfig) error
	UpdateStorageConfig(storageConfig *model.StorageConfig) error
	DeleteStorageConfig(storageID string) error
	SetDefaultStorageConfig(storageID string) error
	ParseStorageConfig(storageConfig *model.StorageConfig) (interface{}, error)
}

func NewStorageRepo(db database.IDatabase, cache cache.ICache) IStorageRepository {
	return &StorageRepo{
		IDatabase: db,
		ICache:    cache,
	}
}

// GetDB 返回数据库实例（供插件适配器使用）
func (sr *StorageRepo) GetDB() database.IDatabase {
	return sr.IDatabase
}

// GetDefaultStorageConfig 获取默认存储配置（带Redis缓存）
func (sr *StorageRepo) GetDefaultStorageConfig() (*model.StorageConfig, error) {
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return storageDefaultConfigCacheKey
	}

	queryFunc := func(ctx context.Context) (*model.StorageConfig, error) {
		var storageConfig model.StorageConfig
		err := sr.Database().Table(storageConfig.TableName()).
			Select("id", "storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled", "created_at", "updated_at").
			Where("is_default = ? AND is_enabled = ?", 1, 1).
			First(&storageConfig).Error
		if err != nil {
			return nil, fmt.Errorf("failed to get default storage config: %w", err)
		}
		return &storageConfig, nil
	}

	cq := cache.NewCachedQuery(
		sr.ICache,
		keyFunc,
		queryFunc,
		cache.WithTTL[*model.StorageConfig](storageConfigCacheTTL),
		cache.WithLogPrefix[*model.StorageConfig]("[StorageRepo]"),
	)

	return cq.Get(ctx)
}

// GetStorageConfigByID 根据存储ID获取配置（带Redis缓存）
func (sr *StorageRepo) GetStorageConfigByID(storageID string) (*model.StorageConfig, error) {
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return storageConfigCacheKeyPrefix + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*model.StorageConfig, error) {
		var storageConfig model.StorageConfig
		err := sr.Database().Table(storageConfig.TableName()).
			Select("id", "storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled", "created_at", "updated_at").
			Where("storage_id = ? AND is_enabled = ?", storageID, 1).
			First(&storageConfig).Error
		if err != nil {
			return nil, fmt.Errorf("failed to get storage config by ID %s: %w", storageID, err)
		}
		return &storageConfig, nil
	}

	cq := cache.NewCachedQuery(
		sr.ICache,
		keyFunc,
		queryFunc,
		cache.WithTTL[*model.StorageConfig](storageConfigCacheTTL),
		cache.WithLogPrefix[*model.StorageConfig]("[StorageRepo]"),
	)

	return cq.Get(ctx, storageID)
}

// GetEnabledStorageConfigs 获取所有启用的存储配置
func (sr *StorageRepo) GetEnabledStorageConfigs() ([]model.StorageConfig, error) {
	var storageConfigs []model.StorageConfig
	var storageConfig model.StorageConfig
	err := sr.Database().Table(storageConfig.TableName()).
		Select("storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled").
		Where("is_enabled = ?", 1).
		Order("is_default DESC, storage_id ASC").
		Find(&storageConfigs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get enabled storage configs: %w", err)
	}
	return storageConfigs, nil
}

// GetStorageConfigByType 根据存储类型获取配置
func (sr *StorageRepo) GetStorageConfigByType(storageType string) ([]model.StorageConfig, error) {
	var storageConfigs []model.StorageConfig
	var storageConfig model.StorageConfig
	err := sr.Database().Table(storageConfig.TableName()).
		Select("storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled").
		Where("storage_type = ? AND is_enabled = ?", storageType, 1).
		Order("is_default DESC, storage_id ASC").
		Find(&storageConfigs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get storage configs by type %s: %w", storageType, err)
	}
	return storageConfigs, nil
}

// CreateStorageConfig 创建存储配置
func (sr *StorageRepo) CreateStorageConfig(storageConfig *model.StorageConfig) error {
	err := sr.Database().Table(storageConfig.TableName()).Create(storageConfig).Error
	if err != nil {
		return fmt.Errorf("failed to create storage config: %w", err)
	}
	return nil
}

// UpdateStorageConfig 更新存储配置
func (sr *StorageRepo) UpdateStorageConfig(storageConfig *model.StorageConfig) error {
	err := sr.Database().Table(storageConfig.TableName()).
		Where("storage_id = ?", storageConfig.StorageId).
		Updates(storageConfig).Error
	if err != nil {
		return fmt.Errorf("failed to update storage config: %w", err)
	}

	// 清除缓存
	sr.clearStorageConfigCache(storageConfig.StorageId)
	// 如果是默认配置，也清除默认配置缓存
	if storageConfig.IsDefault == 1 {
		sr.clearDefaultStorageConfigCache()
	}

	return nil
}

// DeleteStorageConfig 删除存储配置
func (sr *StorageRepo) DeleteStorageConfig(storageID string) error {
	var storageConfig model.StorageConfig
	err := sr.Database().Table(storageConfig.TableName()).
		Where("storage_id = ?", storageID).
		Delete(&model.StorageConfig{}).Error
	if err != nil {
		return fmt.Errorf("failed to delete storage config: %w", err)
	}

	// 清除缓存
	sr.clearStorageConfigCache(storageID)
	sr.clearDefaultStorageConfigCache()

	return nil
}

// SetDefaultStorageConfig 设置默认存储配置
func (sr *StorageRepo) SetDefaultStorageConfig(storageID string) error {
	// 先取消所有默认配置
	var storageConfig model.StorageConfig
	err := sr.Database().Table(storageConfig.TableName()).
		Where("is_default = ?", 1).
		Update("is_default", 0).Error
	if err != nil {
		return fmt.Errorf("failed to clear default storage configs: %w", err)
	}

	// 设置新的默认配置
	err = sr.Database().Table(storageConfig.TableName()).
		Where("storage_id = ?", storageID).
		Update("is_default", 1).Error
	if err != nil {
		return fmt.Errorf("failed to set default storage config: %w", err)
	}

	// 清除默认配置缓存和相关配置缓存
	sr.clearDefaultStorageConfigCache()
	sr.clearStorageConfigCache(storageID)

	return nil
}

// clearStorageConfigCache 清除指定存储配置的缓存
func (sr *StorageRepo) clearStorageConfigCache(storageID string) {
	ctx := context.Background()
	keyFunc := func(params ...any) string {
		return storageConfigCacheKeyPrefix + params[0].(string)
	}
	cq := cache.NewCachedQuery[*model.StorageConfig](sr.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, storageID)
}

// clearDefaultStorageConfigCache 清除默认存储配置的缓存
func (sr *StorageRepo) clearDefaultStorageConfigCache() {
	ctx := context.Background()
	keyFunc := func(params ...any) string {
		return storageDefaultConfigCacheKey
	}
	cq := cache.NewCachedQuery[*model.StorageConfig](sr.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx)
}

// ParseStorageConfig 解析存储配置JSON为具体配置结构
func (sr *StorageRepo) ParseStorageConfig(storageConfig *model.StorageConfig) (interface{}, error) {
	raw := storageConfig.Config

	// 兼容 Base64 + JSON 两种情况
	var configBytes []byte
	configStr := string(raw)
	if strings.HasPrefix(configStr, "{") {
		configBytes = raw
	} else {
		decoded, err := base64.StdEncoding.DecodeString(configStr)
		if err != nil {
			return nil, fmt.Errorf("invalid config encoding: %w", err)
		}
		configBytes = decoded
	}

	switch storageConfig.StorageType {
	case "minio":
		var config model.MinIOConfig
		if err := json.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse MinIO config: %w", err)
		}
		return &config, nil
	case "s3":
		var config model.S3Config
		if err := json.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse S3 config: %w", err)
		}
		return &config, nil
	case "oss":
		var config model.OSSConfig
		if err := json.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse OSS config: %w", err)
		}
		return &config, nil
	case "gcs":
		var config model.GCSConfig
		if err := json.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse GCS config: %w", err)
		}
		return &config, nil
	case "cos":
		var config model.COSConfig
		if err := json.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse COS config: %w", err)
		}
		return &config, nil
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageConfig.StorageType)
	}
}
