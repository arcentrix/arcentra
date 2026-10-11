// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrGatewayEventSequence 表示事件没有接续已经保存的连续序号
var ErrGatewayEventSequence = errors.New("gateway event sequence is not contiguous")

// ErrGatewayEventConflict 表示相同序号或事件标识携带不同的事件内容
var ErrGatewayEventConflict = errors.New("gateway event conflicts with an existing event")

// IGatewayEventRepository 定义持久化事件和连续接收游标的接口
type IGatewayEventRepository interface {
	LastAcceptedSeq(ctx context.Context, agentID string) (uint64, error)
	StoreBatch(ctx context.Context, agentID string, events []*model.GatewayEvent) (uint64, error)
}

// GatewayEventRepository 使用数据库事务保存原始事件及接收游标
type GatewayEventRepository struct{ db *gorm.DB }

// NewGatewayEventRepository 创建事件仓储，不自动创建或迁移数据库表
func NewGatewayEventRepository(db *gorm.DB) IGatewayEventRepository {
	return &GatewayEventRepository{db: db}
}

// LastAcceptedSeq 读取数据库确认的连续序号，不接受客户端声明作为确认依据
func (r *GatewayEventRepository) LastAcceptedSeq(ctx context.Context, agentID string) (uint64, error) {
	if r.db == nil {
		return 0, errors.New("gateway event database is not configured")
	}
	if agentID == "" {
		return 0, gorm.ErrInvalidData
	}
	var cursor model.GatewayEventCursor
	err := r.db.WithContext(ctx).Select("agent_id", "last_accepted_seq").Where("agent_id = ?", agentID).Take(&cursor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return cursor.LastAcceptedSeq, err
}

// StoreBatch 原子保存一个有序批次，只有事务提交后才返回可确认的序号
// 重放必须匹配已保存内容，任何缺口或冲突都会回滚整个批次
func (r *GatewayEventRepository) StoreBatch(ctx context.Context, agentID string, events []*model.GatewayEvent) (uint64, error) {
	if r.db == nil {
		return 0, errors.New("gateway event database is not configured")
	}
	if agentID == "" || len(events) == 0 {
		return 0, gorm.ErrInvalidData
	}
	for _, event := range events {
		if event == nil || event.AgentID != agentID || event.Seq == 0 || event.Seq > math.MaxInt64 ||
			event.EventID == "" || event.EventType == "" || event.Payload == "" || event.Fingerprint == "" {
			return 0, gorm.ErrInvalidData
		}
	}
	var lastSeq uint64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cursor := model.GatewayEventCursor{AgentID: agentID}
		// The upsert obtains a write lock on SQLite; FOR UPDATE serializes writers on MySQL.
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}}, DoNothing: true}).
			Select("agent_id", "last_accepted_seq", "created_at", "updated_at").Create(&cursor).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("agent_id", "last_accepted_seq").
			Where("agent_id = ?", agentID).Take(&cursor).Error; err != nil {
			return err
		}
		originalSeq := cursor.LastAcceptedSeq
		lastSeq = originalSeq
		for _, event := range events {
			if event.Seq <= lastSeq {
				var existing model.GatewayEvent
				err := tx.Select("event_id", "fingerprint").Where("agent_id = ? AND seq = ?", agentID, event.Seq).Take(&existing).Error
				if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (existing.EventID != event.EventID || existing.Fingerprint != event.Fingerprint)) {
					return ErrGatewayEventConflict
				}
				if err != nil {
					return err
				}
				continue
			}
			if event.Seq != lastSeq+1 {
				return fmt.Errorf("%w: expected %d", ErrGatewayEventSequence, lastSeq+1)
			}
			var existing model.GatewayEvent
			err := tx.Select("seq").Where("agent_id = ? AND event_id = ?", agentID, event.EventID).Take(&existing).Error
			if err == nil {
				return ErrGatewayEventConflict
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err := tx.Select("agent_id", "seq", "event_id", "event_type", "pipeline_id", "step_id", "timestamp", "payload", "fingerprint", "created_at", "updated_at").Create(event).Error; err != nil {
				return err
			}
			lastSeq = event.Seq
		}
		if lastSeq == originalSeq {
			return nil
		}
		result := tx.Model(&model.GatewayEventCursor{}).Where("agent_id = ? AND last_accepted_seq = ?", agentID, originalSeq).
			Update("last_accepted_seq", lastSeq)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("gateway event cursor changed concurrently")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return lastSeq, nil
}
