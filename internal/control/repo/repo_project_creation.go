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

// IProjectCreationRepository 在明确创建项目操作内重建实时授权
// 仅创建项目，不从任意旧组织作用域生成组织或成员关系
type IProjectCreationRepository interface {
	CreateAuthorized(context.Context, *model.Project, ScopedAuthorize) error
}

// CreateAuthorized 原子验证组织生命周期、创建者授权并写入项目及审计
func (r *ProjectRepo) CreateAuthorized(ctx context.Context, p *model.Project, authorize ScopedAuthorize) error {
	if p == nil || p.CreatedBy == "" || p.ProjectID == "" || p.OrgID == "" {
		return ErrOrganizationInvalid
	}
	return r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		o, err := organizationRow(ctx, tx, p.OrgID)
		if err != nil && !errors.Is(err, ErrOrganizationNotFound) {
			return err
		}
		if err == nil && !organizationActive(o) {
			return ErrOrganizationConflict
		}
		var sameName []model.Project
		if err := scopedQuery(ctx, tx).Select("project_id").Where("org_id = ? AND name = ?", p.OrgID, p.Name).Limit(1).Find(&sameName).Error; err != nil {
			return err
		}
		if len(sameName) > 0 {
			return ErrOrganizationConflict
		}
		access, current, err := scopedAuth(ctx, tx, p.CreatedBy, authorize)
		if err != nil {
			return err
		}
		capabilities, err := access("org", p.OrgID, p.OrgID)
		if err != nil {
			return err
		}
		if !capabilities.CreateProject {
			return ErrOrganizationForbidden
		}
		if err := tx.WithContext(ctx).Create(p).Error; err != nil {
			return err
		}
		if err := scopedAudit(ctx, tx, p.CreatedBy, "project:create", "project", p.ProjectID, "", ""); err != nil {
			return err
		}
		return current(ctx)
	})
}
