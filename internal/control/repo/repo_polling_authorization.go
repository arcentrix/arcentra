// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"fmt"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// PollingPrimaryRepositories 为状态、候选列表和提供方读取前检查创建独立无缓存主库视图
// 该视图不替代轮询写事务内的权限锁和到期复核
func (r *Repositories) PollingPrimaryRepositories() (*Repositories, error) {
	if r == nil || r.database == nil || r.database.Database() == nil {
		return nil, fmt.Errorf("polling primary repositories unavailable")
	}
	db := pollingPrimaryDatabase{db: r.database.Database()}
	return &Repositories{database: db, SCMPolling: NewSCMPollingRepo(db), Pipeline: NewPipelineRepo(db), Project: NewProjectRepo(db), User: NewUserRepo(db, nil), Team: NewTeamRepo(db), TeamMember: NewTeamMemberRepo(db), Role: NewRoleRepo(db), RoleGrant: NewRoleGrantRepo(db), Permission: NewPermissionRepo(db)}, nil
}

type pollingPrimaryDatabase struct{ db *gorm.DB }

// Database 为每次查询创建新的主库会话，防止条件与副本路由污染
func (d pollingPrimaryDatabase) Database() *gorm.DB {
	return d.db.Session(&gorm.Session{NewDB: true}).Clauses(database.Write())
}
