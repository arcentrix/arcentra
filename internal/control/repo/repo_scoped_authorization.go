// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// NewScopedAuthorizationView 返回事务中的当前锁定权限读取以及提交前过期复核
// 调用方须先锁定资源，再通过此视图读取用户、团队和权限，不能复用外部缓存
func NewScopedAuthorizationView(tx *gorm.DB) (*Repositories, func(context.Context) error) {
	view := &retryApprovalView{tx: tx}
	return view.LiveRepositories(), view.current
}

// AuthorizationProject 从主库验证唯一且字节完全相同的项目标识
// 历史数据库中的重复记录和排序规则别名不得作为权限作用域依据
func (r *Repositories) AuthorizationProject(ctx context.Context, id string) (*model.Project, error) {
	if r == nil || r.database == nil || r.database.Database() == nil {
		return nil, errors.New("authorization project source unavailable")
	}
	var rows []model.Project
	if err := r.database.Database().WithContext(ctx).Clauses(database.Write()).Select("project_id", "org_id").Where("project_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].ProjectID != id {
		return nil, ErrOrganizationConflict
	}
	return &rows[0], nil
}
