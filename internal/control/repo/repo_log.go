// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ILogRepository 定义步骤日志持久化接口
type ILogRepository interface {
	EnsureSchema(ctx context.Context) error
	CreateBatch(ctx context.Context, entries []*model.StepRunLog) error
	ListByStepRunIDs(ctx context.Context, stepRunIDs []string, fromLine int32, limit int) ([]*model.StepRunLog, error)
	ListByStepRunID(ctx context.Context, stepRunID string, fromLine int32, limit int) ([]*model.StepRunLog, error)
	LastLineNumber(ctx context.Context, stepRunID string) (int32, error)
}

// ILogIdentityRepository 可选地查询已持久化的日志标识，防止重放再次分配行号或推送订阅
type ILogIdentityRepository interface {
	HasLogID(ctx context.Context, logID string) (bool, error)
}

// LogRepository 使用 GORM 读写步骤日志
type LogRepository struct{ db *gorm.DB }

// NewLogRepository 创建日志仓储
func NewLogRepository(db *gorm.DB) ILogRepository { return &LogRepository{db: db} }

// EnsureSchema 初始化日志表及重试去重索引
func (r *LogRepository) EnsureSchema(ctx context.Context) error {
	if r.db == nil {
		return errors.New("log database is not configured")
	}
	return r.db.WithContext(ctx).AutoMigrate(&model.StepRunLog{})
}

// HasLogID 查询指定业务标识的日志是否已经持久化
func (r *LogRepository) HasLogID(ctx context.Context, logID string) (bool, error) {
	if r.db == nil {
		return false, errors.New("log database is not configured")
	}
	if logID == "" {
		return false, gorm.ErrInvalidData
	}
	var entry model.StepRunLog
	err := r.db.WithContext(ctx).Select("log_id").Where("log_id = ?", logID).Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// CreateBatch 原子保存日志批次，重复投递的 LogID 不会重复落库
func (r *LogRepository) CreateBatch(ctx context.Context, entries []*model.StepRunLog) error {
	if len(entries) == 0 {
		return nil
	}
	if r.db == nil {
		return errors.New("log database is not configured")
	}
	for _, entry := range entries {
		if entry == nil || entry.LogID == "" || entry.StepRunID == "" {
			return gorm.ErrInvalidData
		}
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "log_id"}}, DoNothing: true}).
			Select("log_id", "step_run_id", "timestamp", "line_number", "level", "content", "stream", "plugin_name", "agent_id", "created_at", "updated_at").
			CreateInBatches(entries, 100).Error
	})
}

// ListByStepRunIDs 返回限定步骤范围内的有序日志，空范围不会退化为全表查询
func (r *LogRepository) ListByStepRunIDs(ctx context.Context, stepRunIDs []string, fromLine int32, limit int) ([]*model.StepRunLog, error) {
	return r.listLogs(ctx, stepRunIDs, fromLine, limit, false)
}

// ListByStepRunID 按行号读取步骤历史，保证按行号分页不会遗漏时钟乱序的日志
func (r *LogRepository) ListByStepRunID(ctx context.Context, stepRunID string, fromLine int32, limit int) ([]*model.StepRunLog, error) {
	if stepRunID == "" {
		return []*model.StepRunLog{}, nil
	}
	return r.listLogs(ctx, []string{stepRunID}, fromLine, limit, true)
}

func (r *LogRepository) listLogs(ctx context.Context, stepRunIDs []string, fromLine int32, limit int, byLine bool) ([]*model.StepRunLog, error) {
	entries := make([]*model.StepRunLog, 0)
	if len(stepRunIDs) == 0 {
		return entries, nil
	}
	if r.db == nil {
		return nil, errors.New("log database is not configured")
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 5000 {
		limit = 5000
	}
	query := r.db.WithContext(ctx).Model(&model.StepRunLog{}).
		Select("log_id", "step_run_id", "timestamp", "line_number", "level", "content", "stream", "plugin_name", "agent_id").
		Where("step_run_id IN ?", stepRunIDs)
	if fromLine > 0 {
		query = query.Where("line_number >= ?", fromLine)
	}
	if byLine {
		query = query.Order("line_number ASC")
	}
	err := query.Order("timestamp ASC").Order("step_run_id ASC").Order("line_number ASC").Order("log_id ASC").Limit(limit).Find(&entries).Error
	return entries, err
}

// LastLineNumber 获取步骤已持久化的最后行号
func (r *LogRepository) LastLineNumber(ctx context.Context, stepRunID string) (int32, error) {
	if r.db == nil {
		return 0, errors.New("log database is not configured")
	}
	var entry model.StepRunLog
	err := r.db.WithContext(ctx).Select("line_number").Where("step_run_id = ?", stepRunID).
		Order("line_number DESC").Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return entry.LineNumber, err
}
