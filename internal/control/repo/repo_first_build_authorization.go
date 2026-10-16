// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"errors"

	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// FirstBuildPrimaryRepositories 创建仅用于首次构建提示和项目创建鉴权的无缓存主库仓储视图
// 该视图使身份、团队、角色和权限快照与配置提示均绕过只读副本
// includeProjectCreateAudit 仅供项目创建启用主库审计写入，GET 必须传 false
// 不改变其他接口的仓储或鉴权行为
func (r *Repositories) FirstBuildPrimaryRepositories(includeProjectCreateAudit bool) (*Repositories, error) {
	if r == nil || r.database == nil {
		return nil, errors.New("first-build authorization repositories are unavailable")
	}
	base := r.database.Database()
	if base == nil {
		return nil, errors.New("first-build authorization repositories are unavailable")
	}
	db := firstBuildPrimaryDatabase{db: base}
	view := &Repositories{
		FirstBuild: NewFirstBuildRepo(db),
		User:       NewUserRepo(db, nil),
		Team:       NewTeamRepo(db),
		TeamMember: NewTeamMemberRepo(db),
		Role:       NewRoleRepo(db),
		RoleGrant:  NewRoleGrantRepo(db),
		Permission: NewPermissionRepo(db),
	}
	if includeProjectCreateAudit {
		view.AuditLog = NewAuditLogRepo(db)
	}
	return view, nil
}

type firstBuildPrimaryDatabase struct {
	db *gorm.DB
}

// Database 为每次查询创建独立主库会话，避免 GORM 条件或副本路由复用
func (db firstBuildPrimaryDatabase) Database() *gorm.DB {
	return db.db.Session(&gorm.Session{NewDB: true}).Clauses(database.Write())
}
