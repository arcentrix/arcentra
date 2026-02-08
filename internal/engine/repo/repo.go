// Copyright 2025 Arcentra Team
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
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// Repositories 统一管理所有 repository
type Repositories struct {
	User                 IUserRepository
	Agent                IAgentRepository
	Storage              IStorageRepository
	Team                 ITeamRepository
	Identity             IIdentityRepository
	GeneralSettings      IGeneralSettingsRepository
	Project              IProjectRepository
	ProjectMember        IProjectMemberRepository
	ProjectTeamAccess    IProjectTeamAccessRepository
	TeamMember           ITeamMemberRepository
	UserExt              IUserExtRepository
	Secret               ISecretRepository
	UserRoleBinding      IUserRoleBindingRepository
	RoleMenuBinding      IRoleMenuBindingRepository
	Menu                 IMenuRepository
	Role                 IRoleRepository
	NotificationTemplate INotificationTemplateRepository
	NotificationChannel  INotificationChannelRepository
	Plugin               IPluginRepository
	StepRun              IStepRunRepository
}

// NewRepositories 初始化所有 repository
func NewRepositories(db database.IDatabase, cache cache.ICache) *Repositories {
	return &Repositories{
		User:                 NewUserRepo(db, cache),
		Agent:                NewAgentRepo(db, cache),
		Storage:              NewStorageRepo(db, cache),
		Team:                 NewTeamRepo(db),
		Identity:             NewIdentityRepo(db),
		GeneralSettings:      NewGeneralSettingsRepo(db, cache),
		Project:              NewProjectRepo(db),
		ProjectMember:        NewProjectMemberRepo(db),
		ProjectTeamAccess:    NewProjectTeamAccessRepo(db),
		TeamMember:           NewTeamMemberRepo(db),
		UserExt:              NewUserExtRepo(db),
		Secret:               NewSecretRepo(db),
		UserRoleBinding:      NewUserRoleBindingRepo(db),
		RoleMenuBinding:      NewRoleMenuBindingRepo(db),
		Menu:                 NewMenuRepo(db),
		Role:                 NewRoleRepo(db),
		NotificationTemplate: NewNotificationTemplateRepo(db),
		NotificationChannel:  NewNotificationChannelRepo(db),
		Plugin:               NewPluginRepo(db),
		StepRun:              NewStepRunRepo(db),
	}
}

// GetDB 返回数据库实例（供插件适配器使用）
func (r *Repositories) GetDB() database.IDatabase {
	return r.Storage.(*StorageRepo).GetDB()
}

func Count(tx *gorm.DB) (int64, error) {
	var count int64
	if err := tx.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func Exist(tx *gorm.DB, where interface{}) bool {
	var one interface{}
	if err := tx.Where(where).First(&one).Error; err != nil {
		return false
	}
	return true
}
