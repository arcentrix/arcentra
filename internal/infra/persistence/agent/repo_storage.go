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

package agent

import (
	"context"
	"fmt"
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/agent"
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/arcentrix/arcentra/pkg/store/database"
)

var _ domain.IStorageRepository = (*StorageRepo)(nil)

const (
	storageConfigCacheKeyPrefix  = "storage:config:"
	storageDefaultConfigCacheKey = "storage:config:default"
	storageConfigCacheTTL        = 24 * time.Hour
)

var storageSelectFields = []string{
	"id", "storage_id", "name", "storage_type", "config",
	"description", "is_default", "is_enabled", "created_at", "updated_at",
}

// StorageRepo implements domain.IStorageRepository using GORM and an optional cache.
type StorageRepo struct {
	db    database.IDatabase
	cache cache.ICache
}

func NewStorageRepo(db database.IDatabase, ch cache.ICache) *StorageRepo {
	return &StorageRepo{db: db, cache: ch}
}

func (r *StorageRepo) GetDefault(ctx context.Context) (*domain.StorageConfig, error) {
	keyFunc := func(_ ...any) string { return storageDefaultConfigCacheKey }

	queryFunc := func(ctx context.Context) (*StorageConfigPO, error) {
		var po StorageConfigPO
		err := r.db.Database().WithContext(ctx).Table(po.TableName()).
			Select(storageSelectFields).
			Where("is_default = ? AND is_enabled = ?", 1, 1).
			First(&po).Error
		if err != nil {
			return nil, fmt.Errorf("failed to get default storage config: %w", err)
		}
		return &po, nil
	}

	cq := cache.NewCachedQuery(r.cache, keyFunc, queryFunc,
		cache.WithTTL[*StorageConfigPO](storageConfigCacheTTL),
		cache.WithLogPrefix[*StorageConfigPO]("[StorageRepo]"),
	)
	po, err := cq.Get(ctx)
	if err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *StorageRepo) Get(ctx context.Context, storageID string) (*domain.StorageConfig, error) {
	keyFunc := func(params ...any) string {
		return storageConfigCacheKeyPrefix + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*StorageConfigPO, error) {
		var po StorageConfigPO
		err := r.db.Database().WithContext(ctx).Table(po.TableName()).
			Select(storageSelectFields).
			Where("storage_id = ? AND is_enabled = ?", storageID, 1).
			First(&po).Error
		if err != nil {
			return nil, fmt.Errorf("failed to get storage config by ID %s: %w", storageID, err)
		}
		return &po, nil
	}

	cq := cache.NewCachedQuery(r.cache, keyFunc, queryFunc,
		cache.WithTTL[*StorageConfigPO](storageConfigCacheTTL),
		cache.WithLogPrefix[*StorageConfigPO]("[StorageRepo]"),
	)
	po, err := cq.Get(ctx, storageID)
	if err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *StorageRepo) ListEnabled(ctx context.Context) ([]domain.StorageConfig, error) {
	var pos []StorageConfigPO
	tbl := StorageConfigPO{}.TableName()
	err := r.db.Database().WithContext(ctx).Table(tbl).
		Select("storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled").
		Where("is_enabled = ?", 1).
		Order("is_default DESC, storage_id ASC").
		Find(&pos).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get enabled storage configs: %w", err)
	}
	return toDomainStorageList(pos), nil
}

func (r *StorageRepo) ListByType(ctx context.Context, storageType domain.StorageType) ([]domain.StorageConfig, error) {
	var pos []StorageConfigPO
	tbl := StorageConfigPO{}.TableName()
	err := r.db.Database().WithContext(ctx).Table(tbl).
		Select("storage_id", "name", "storage_type", "config", "description", "is_default", "is_enabled").
		Where("storage_type = ? AND is_enabled = ?", string(storageType), 1).
		Order("is_default DESC, storage_id ASC").
		Find(&pos).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get storage configs by type %s: %w", storageType, err)
	}
	return toDomainStorageList(pos), nil
}

func (r *StorageRepo) Create(ctx context.Context, config *domain.StorageConfig) error {
	po := StorageConfigPOFromDomain(config)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return fmt.Errorf("failed to create storage config: %w", err)
	}
	config.ID = po.ID
	return nil
}

func (r *StorageRepo) Update(ctx context.Context, config *domain.StorageConfig) error {
	po := StorageConfigPOFromDomain(config)
	err := r.db.Database().WithContext(ctx).Table(po.TableName()).
		Where("storage_id = ?", po.StorageID).
		Updates(po).Error
	if err != nil {
		return fmt.Errorf("failed to update storage config: %w", err)
	}

	r.clearStorageConfigCache(ctx, config.StorageID)
	if config.IsDefault {
		r.clearDefaultStorageConfigCache(ctx)
	}
	return nil
}

func (r *StorageRepo) Delete(ctx context.Context, storageID string) error {
	tbl := StorageConfigPO{}.TableName()
	err := r.db.Database().WithContext(ctx).Table(tbl).
		Where("storage_id = ?", storageID).
		Delete(&StorageConfigPO{}).Error
	if err != nil {
		return fmt.Errorf("failed to delete storage config: %w", err)
	}
	r.clearStorageConfigCache(ctx, storageID)
	r.clearDefaultStorageConfigCache(ctx)
	return nil
}

func (r *StorageRepo) SetDefault(ctx context.Context, storageID string) error {
	tbl := StorageConfigPO{}.TableName()
	if err := r.db.Database().WithContext(ctx).Table(tbl).
		Where("is_default = ?", 1).
		Update("is_default", 0).Error; err != nil {
		return fmt.Errorf("failed to clear default storage configs: %w", err)
	}
	if err := r.db.Database().WithContext(ctx).Table(tbl).
		Where("storage_id = ?", storageID).
		Update("is_default", 1).Error; err != nil {
		return fmt.Errorf("failed to set default storage config: %w", err)
	}
	r.clearDefaultStorageConfigCache(ctx)
	r.clearStorageConfigCache(ctx, storageID)
	return nil
}

// --- cache helpers ---

func (r *StorageRepo) clearStorageConfigCache(ctx context.Context, storageID string) {
	keyFunc := func(params ...any) string {
		return storageConfigCacheKeyPrefix + params[0].(string)
	}
	cq := cache.NewCachedQuery[*StorageConfigPO](r.cache, keyFunc, nil)
	_ = cq.Invalidate(ctx, storageID)
}

func (r *StorageRepo) clearDefaultStorageConfigCache(ctx context.Context) {
	keyFunc := func(_ ...any) string { return storageDefaultConfigCacheKey }
	cq := cache.NewCachedQuery[*StorageConfigPO](r.cache, keyFunc, nil)
	_ = cq.Invalidate(ctx)
}

func toDomainStorageList(pos []StorageConfigPO) []domain.StorageConfig {
	result := make([]domain.StorageConfig, len(pos))
	for i := range pos {
		result[i] = *pos[i].ToDomain()
	}
	return result
}
