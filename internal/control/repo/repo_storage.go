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
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/bytedance/sonic"
	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type StorageRepo struct {
	database.IDatabase
	cache.ICache
}

const (
	// Redis 缓存键前缀
	storageConfigCacheKeyPrefix  = "storage:config:"
	storageDefaultConfigCacheKey = "storage:config:default"
)

// IStorageRepository defines storage config persistence with context support.
type IStorageRepository interface {
	GetDefault(ctx context.Context) (*model.StorageConfig, error)
	Get(ctx context.Context, storageID string) (*model.StorageConfig, error)
	ListEnabled(ctx context.Context) ([]model.StorageConfig, error)
	ListByType(ctx context.Context, storageType string) ([]model.StorageConfig, error)
	Create(ctx context.Context, storageConfig *model.StorageConfig) error
	Update(ctx context.Context, storageConfig *model.StorageConfig) error
	UpdateConfig(ctx context.Context, storageID string, mutate func(*model.StorageConfig) error) (*model.StorageConfig, error)
	Delete(ctx context.Context, storageID string) error
	SetDefault(ctx context.Context, storageID string) error
	ParseStorageConfig(storageConfig *model.StorageConfig) (interface{}, error)
}

func NewStorageRepo(db database.IDatabase, ch cache.ICache) IStorageRepository {
	return &StorageRepo{
		IDatabase: db,
		ICache:    ch,
	}
}

// GetDB 返回数据库实例（供插件适配器使用）
func (sr *StorageRepo) GetDB() database.IDatabase {
	return sr.IDatabase
}

// GetDefault 从主库读取默认配置，避免缓存回填与未知提交结果留下旧凭据
func (sr *StorageRepo) GetDefault(ctx context.Context) (*model.StorageConfig, error) {
	var rows []model.StorageConfig
	err := sr.Database().WithContext(ctx).Clauses(database.Write()).Model(&model.StorageConfig{}).
		Select("id", "storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled", "created_at", "updated_at").
		Where("is_default = ? AND is_enabled = ?", 1, 1).Limit(2).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get default storage config: %w", err)
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(rows) > 1 {
		return nil, ErrStorageConfigConflict
	}
	if artifactutil.Identity(rows[0].StorageID) != nil {
		return nil, ErrStorageConfigID
	}
	return &rows[0], nil
}

// Get 从主库按业务标识读取配置，现有重复标识不会被任意解析
func (sr *StorageRepo) Get(ctx context.Context, storageID string) (*model.StorageConfig, error) {
	return storageTarget(sr.Database().WithContext(ctx).Clauses(database.Write()), storageID)
}

// ListEnabled returns all enabled storage configs.
func (sr *StorageRepo) ListEnabled(ctx context.Context) ([]model.StorageConfig, error) {
	var storageConfigs []model.StorageConfig
	var storageConfig model.StorageConfig
	err := sr.Database().WithContext(ctx).Clauses(database.Write()).Table(storageConfig.TableName()).
		Select("storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled").
		Where("is_enabled = ?", 1).
		Order("is_default DESC, storage_id ASC").
		Find(&storageConfigs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get enabled storage configs: %w", err)
	}
	return storageConfigs, nil
}

// ListByType returns storage configs by storageType.
func (sr *StorageRepo) ListByType(ctx context.Context, storageType string) ([]model.StorageConfig, error) {
	var storageConfigs []model.StorageConfig
	var storageConfig model.StorageConfig
	err := sr.Database().WithContext(ctx).Clauses(database.Write()).Table(storageConfig.TableName()).
		Select("storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled").
		Where("storage_type = ? AND is_enabled = ?", storageType, 1).
		Order("is_default DESC, storage_id ASC").
		Find(&storageConfigs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get storage configs by type %s: %w", storageType, err)
	}
	return storageConfigs, nil
}

// ErrStorageConfigNotFound 表示目标配置不存在或已禁用
var ErrStorageConfigNotFound = errors.New("storage configuration not found or disabled")

// ErrStorageConfigConflict 表示业务标识已存在或不能唯一解析
var ErrStorageConfigConflict = errors.New("storage configuration id already exists or is ambiguous")

// ErrStorageTargetImmutable 表示已有配置的对象存储位置被修改
var ErrStorageTargetImmutable = errors.New("create a new configuration to change the storage target")

// ErrStorageConfigID 表示业务标识不符合制品使用的身份约束
var ErrStorageConfigID = errors.New("storage id must be a valid artifact identity of at most 64 bytes")

// ErrStorageConfigFlags 表示默认或启用标记不合法
var ErrStorageConfigFlags = errors.New("storage flags must be 0 or 1 and a default must be enabled")

const storageMutationAttempts = 3

// storageTransaction 的锁定读取使用主库与 RR，空表范围锁也参与并发创建仲裁
// 只重试 MySQL 明确回滚的死锁，不重试未知提交结果或锁等待超时
// 回调只允许数据库内修改，缓存失效必须在提交成功之后执行
// 配置包含只写凭据，本事务禁用 SQL 值日志，避免慢查询和失败路径泄露凭据
func (sr *StorageRepo) storageTransaction(ctx context.Context, operation func(*gorm.DB, []model.StorageConfig) error) error {
	for attempt := 0; attempt < storageMutationAttempts; attempt++ {
		err := sr.Database().WithContext(ctx).Clauses(database.Write()).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			var rows []model.StorageConfig
			if err := tx.Model(&model.StorageConfig{}).Select("id", "storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled", "created_at", "updated_at").
				Clauses(clause.Locking{Strength: "UPDATE"}).Order("id ASC").Find(&rows).Error; err != nil {
				return err
			}
			return operation(tx, rows)
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		var mysqlErr *drivermysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1213 || attempt == storageMutationAttempts-1 {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return errors.New("storage mutation retry exhausted")
}

func storageTarget(tx *gorm.DB, storageID string) (*model.StorageConfig, error) {
	if artifactutil.Identity(storageID) != nil {
		return nil, ErrStorageConfigID
	}
	// 使用数据库排序规则查找，避免大小写别名绕过唯一性检查
	var rows []model.StorageConfig
	if err := tx.Model(&model.StorageConfig{}).Select("id", "storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled", "created_at", "updated_at").Where("storage_id = ?", storageID).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 1 {
		return nil, ErrStorageConfigConflict
	}
	if len(rows) == 0 || rows[0].IsEnabled != 1 {
		return nil, ErrStorageConfigNotFound
	}
	if rows[0].StorageID != storageID {
		return nil, ErrStorageConfigConflict
	}
	return &rows[0], nil
}

func validateStorageFlags(config *model.StorageConfig) error {
	if (config.IsDefault != 0 && config.IsDefault != 1) || (config.IsEnabled != 0 && config.IsEnabled != 1) || (config.IsDefault == 1 && config.IsEnabled != 1) {
		return ErrStorageConfigFlags
	}
	return nil
}

func sameStorageTarget(previous, next *model.StorageConfig) bool {
	var old, current model.StorageConfigDetail
	if sonic.Unmarshal(previous.Config, &old) != nil || sonic.Unmarshal(next.Config, &current) != nil {
		return false
	}
	return artifactutil.StorageFingerprint(previous.StorageType, old.Endpoint, old.Bucket, old.Region, old.BasePath, old.UseTLS) ==
		artifactutil.StorageFingerprint(next.StorageType, current.Endpoint, current.Bucket, current.Region, current.BasePath, current.UseTLS)
}

func clearOtherStorageDefaults(tx *gorm.DB, storageID string) error {
	return tx.Model(&model.StorageConfig{}).Where("is_default = ? AND storage_id <> ?", 1, storageID).Update("is_default", 0).Error
}

func (sr *StorageRepo) invalidateStorageMutation(ctx context.Context, storageID string, previousDefaults []string) {
	sr.clearDefaultStorageConfigCache(ctx)
	sr.clearStorageConfigCache(ctx, storageID)
	for _, previous := range previousDefaults {
		if previous != storageID {
			sr.clearStorageConfigCache(ctx, previous)
		}
	}
}

func storageDefaultIDs(rows []model.StorageConfig) []string {
	var ids []string
	for _, row := range rows {
		if row.IsDefault == 1 {
			ids = append(ids, row.StorageID)
		}
	}
	return ids
}

// Create 原子创建配置并在需要时切换默认，重复业务标识不会插入新记录
func (sr *StorageRepo) Create(ctx context.Context, storageConfig *model.StorageConfig) error {
	if artifactutil.Identity(storageConfig.StorageID) != nil {
		return ErrStorageConfigID
	}
	if err := validateStorageFlags(storageConfig); err != nil {
		return err
	}
	original := *storageConfig
	var created model.StorageConfig
	var previousDefaults []string
	err := sr.storageTransaction(ctx, func(tx *gorm.DB, rows []model.StorageConfig) error {
		var duplicates int64
		if err := tx.Model(&model.StorageConfig{}).Where("storage_id = ?", original.StorageID).Count(&duplicates).Error; err != nil {
			return err
		}
		if duplicates != 0 {
			return ErrStorageConfigConflict
		}
		previousDefaults = storageDefaultIDs(rows)
		created = original
		if created.IsDefault == 1 {
			if err := clearOtherStorageDefaults(tx, created.StorageID); err != nil {
				return err
			}
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create storage config: %w", err)
	}
	*storageConfig = created
	sr.invalidateStorageMutation(ctx, storageConfig.StorageID, previousDefaults)
	return nil
}

// Update 更新完整配置并持久化零值，目标位置保持不变
func (sr *StorageRepo) Update(ctx context.Context, storageConfig *model.StorageConfig) error {
	updated, err := sr.UpdateConfig(ctx, storageConfig.StorageID, func(current *model.StorageConfig) error {
		base := current.BaseModel
		*current = *storageConfig
		current.BaseModel = base
		return nil
	})
	if err == nil {
		*storageConfig = *updated
	}
	return err
}

// UpdateConfig 在锁定的最新配置上合并修改，回调不可包含外部副作用
func (sr *StorageRepo) UpdateConfig(ctx context.Context, storageID string, mutate func(*model.StorageConfig) error) (*model.StorageConfig, error) {
	var updated model.StorageConfig
	var previousDefaults []string
	err := sr.storageTransaction(ctx, func(tx *gorm.DB, rows []model.StorageConfig) error {
		current, err := storageTarget(tx, storageID)
		if err != nil {
			return err
		}
		previous := *current
		previous.Config = append([]byte(nil), current.Config...)
		updated = *current
		if err := mutate(&updated); err != nil {
			return err
		}
		if updated.StorageID != storageID || !sameStorageTarget(&previous, &updated) {
			return ErrStorageTargetImmutable
		}
		if err := validateStorageFlags(&updated); err != nil {
			return err
		}
		previousDefaults = storageDefaultIDs(rows)
		if updated.IsDefault == 1 {
			if err := clearOtherStorageDefaults(tx, storageID); err != nil {
				return err
			}
		}
		return tx.Model(&model.StorageConfig{}).Where("id = ?", current.ID).
			Updates(map[string]any{"name": updated.Name, "storage_type": updated.StorageType, "config": updated.Config, "description": updated.Description, "is_default": updated.IsDefault, "is_enabled": updated.IsEnabled}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update storage config: %w", err)
	}
	sr.invalidateStorageMutation(ctx, storageID, previousDefaults)
	return &updated, nil
}

// Delete 删除配置，并与默认切换使用相同的数据库锁顺序
func (sr *StorageRepo) Delete(ctx context.Context, storageID string) error {
	err := sr.storageTransaction(ctx, func(tx *gorm.DB, _ []model.StorageConfig) error {
		return tx.Where("storage_id = ?", storageID).Delete(&model.StorageConfig{}).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete storage config: %w", err)
	}
	sr.invalidateStorageMutation(ctx, storageID, nil)
	return nil
}

// SetDefault 原子切换默认配置，缺失或禁用的目标不会清除已有默认
func (sr *StorageRepo) SetDefault(ctx context.Context, storageID string) error {
	var previousDefaults []string
	err := sr.storageTransaction(ctx, func(tx *gorm.DB, rows []model.StorageConfig) error {
		target, err := storageTarget(tx, storageID)
		if err != nil {
			return err
		}
		previousDefaults = storageDefaultIDs(rows)
		if err := clearOtherStorageDefaults(tx, storageID); err != nil {
			return err
		}
		// 已默认的配置允许重复请求；MySQL 的 unchanged-row RowsAffected=0 不表示目标丢失
		return tx.Model(&model.StorageConfig{}).Where("id = ? AND is_enabled = ?", target.ID, 1).Update("is_default", 1).Error
	})
	if err != nil {
		return fmt.Errorf("failed to set default storage config: %w", err)
	}
	sr.invalidateStorageMutation(ctx, storageID, previousDefaults)
	return nil
}

func (sr *StorageRepo) clearStorageConfigCache(ctx context.Context, storageID string) {
	keyFunc := func(params ...any) string {
		return storageConfigCacheKeyPrefix + params[0].(string)
	}
	cq := cache.NewCachedQuery[*model.StorageConfig](sr.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, storageID)
}

func (sr *StorageRepo) clearDefaultStorageConfigCache(ctx context.Context) {
	keyFunc := func(_ ...any) string {
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
		if err := sonic.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse MinIO config: %w", err)
		}
		return &config, nil
	case "s3":
		var config model.S3Config
		if err := sonic.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse S3 config: %w", err)
		}
		return &config, nil
	case "oss":
		var config model.OSSConfig
		if err := sonic.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse OSS config: %w", err)
		}
		return &config, nil
	case "gcs":
		var config model.GCSConfig
		if err := sonic.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse GCS config: %w", err)
		}
		return &config, nil
	case "cos":
		var config model.COSConfig
		if err := sonic.Unmarshal(configBytes, &config); err != nil {
			return nil, fmt.Errorf("failed to parse COS config: %w", err)
		}
		return &config, nil
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageConfig.StorageType)
	}
}
