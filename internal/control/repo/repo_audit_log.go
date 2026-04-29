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

package repo

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// IAuditLogRepository 定义审计日志仓储接口
type IAuditLogRepository interface {
	Create(ctx context.Context, entry *model.AuditLog) error
	List(ctx context.Context, action, actorUserID string, pageNum, pageSize int) ([]model.AuditLog, int64, error)
}

// AuditLogRepo 表示审计日志仓储实现
type AuditLogRepo struct {
	database.IDatabase
}

// NewAuditLogRepo 创建审计日志仓储
func NewAuditLogRepo(db database.IDatabase) IAuditLogRepository {
	return &AuditLogRepo{IDatabase: db}
}

// Create 创建审计日志
func (r *AuditLogRepo) Create(ctx context.Context, entry *model.AuditLog) error {
	return r.Database().WithContext(ctx).Create(entry).Error
}

// List 查询审计日志
func (r *AuditLogRepo) List(ctx context.Context, action, actorUserID string, pageNum, pageSize int) ([]model.AuditLog, int64, error) {
	var entries []model.AuditLog
	var total int64

	db := r.Database().WithContext(ctx).Model(&model.AuditLog{})
	if action != "" {
		db = db.Where("action = ?", action)
	}
	if actorUserID != "" {
		db = db.Where("actor_user_id = ?", actorUserID)
	}
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	offset := (pageNum - 1) * pageSize
	err := db.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&entries).Error
	return entries, total, err
}
