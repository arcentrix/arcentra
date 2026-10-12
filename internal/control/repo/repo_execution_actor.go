// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
)

// IExecutionActorReader 只读取执行鉴权所需的身份与启用状态，不读取密码等资料。
type IExecutionActorReader interface {
	GetExecutionActor(context.Context, string) (*model.User, error)
}

// GetExecutionActor 使用当前数据库状态和调用方上下文校验执行用户。
func (r *UserRepo) GetExecutionActor(ctx context.Context, userID string) (*model.User, error) {
	var user model.User
	err := r.Database().WithContext(ctx).Select("user_id", "is_enabled").Where("user_id = ?", userID).First(&user).Error
	return &user, err
}
