// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
)

// preflightOrganizationSchema 拒绝有歧义的旧标识和成员关系，不静默清理或补授权限
// 必须在任何 AutoMigrate 写入前执行；GROUP BY 使用目标数据库自身排序规则
func preflightOrganizationSchema(ctx context.Context, db *gorm.DB) error {
	for _, check := range []struct {
		value   any
		columns []string
	}{
		{&model.Organization{}, []string{"org_id"}},
		{&model.Organization{}, []string{"name"}},
		{&model.OrganizationMember{}, []string{"org_id", "user_id"}},
		{&model.Project{}, []string{"project_id"}},
		{&model.ProjectMember{}, []string{"project_id", "user_id"}},
	} {
		if !db.WithContext(ctx).Migrator().HasTable(check.value) {
			continue
		}
		for _, column := range check.columns {
			var count int64
			if err := db.WithContext(ctx).Model(check.value).Where(column+" IS NULL OR TRIM("+column+") = ? OR LENGTH("+column+") > ?", "", 64).Count(&count).Error; err != nil {
				return fmt.Errorf("organization migration preflight unavailable: %w", err)
			}
			if count > 0 {
				return fmt.Errorf("organization migration preflight: invalid %T %s; review legacy rows before retrying", check.value, column)
			}
		}
		var duplicates []struct{ DuplicateCount int64 }
		if err := db.WithContext(ctx).Model(check.value).Select("COUNT(*) AS duplicate_count").Group(strings.Join(check.columns, ", ")).Having("COUNT(*) > ?", 1).Limit(1).Scan(&duplicates).Error; err != nil {
			return fmt.Errorf("organization migration preflight unavailable: %w", err)
		}
		if len(duplicates) > 0 {
			return fmt.Errorf("organization migration preflight: duplicate %T %s under database collation; manual review required", check.value, strings.Join(check.columns, ", "))
		}
	}
	return nil
}
