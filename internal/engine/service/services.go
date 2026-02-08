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

package service

import (
	"github.com/arcentrix/arcentra/internal/engine/repo"
	storagepkg "github.com/arcentrix/arcentra/internal/pkg/storage"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	pluginpkg "github.com/arcentrix/arcentra/pkg/plugin"
	"golang.org/x/crypto/bcrypt"
)

// Services 统一管理所有 service
type Services struct {
	User              *UserService
	Agent             *AgentService
	Identity          *IdentityService
	Team              *TeamService
	Storage           *StorageService
	Upload            *UploadService
	Secret            *SecretService
	GeneralSettings   *GeneralSettingsService
	Project           *ProjectService
	UserExt           *UserExt
	Menu              *MenuService
	Role              *RoleService
	Plugin            *PluginService
	ProjectMemberRepo repo.IProjectMemberRepository
	StepRunRepo       repo.IStepRunRepository
	LogAggregator     *LogAggregator
}

// NewServices 初始化所有 service
func NewServices(
	db database.IDatabase,
	cache cache.ICache,
	repos *repo.Repositories,
	pluginManager *pluginpkg.Manager,
	storageProvider storagepkg.StorageProvider,
) *Services {
	// 基础服务
	menuService := NewMenuService(repos.Menu)
	userService := NewUserService(cache, repos.User, repos.UserExt, repos.UserRoleBinding, repos.RoleMenuBinding, repos.Menu, repos.Role, menuService)
	generalSettingsService := NewGeneralSettingsService(repos.GeneralSettings)
	agentService := NewAgentService(repos.Agent, generalSettingsService)
	identityService := NewIdentityService(repos.Identity, repos.User, repos.UserExt)
	teamService := NewTeamService(repos.Team)
	storageService := NewStorageService(repos.Storage)
	uploadService := NewUploadService(repos.Storage)
	secretService := NewSecretService(repos.Secret)
	projectService := NewProjectService(repos.Project)
	userExt := NewUserExt(repos.UserExt)
	roleService := NewRoleService(repos.Role)
	pluginService := NewPluginService(repos.Plugin)
	logAggregator := NewLogAggregator(nil, db.Database())

	return &Services{
		User:              userService,
		Agent:             agentService,
		Identity:          identityService,
		Team:              teamService,
		Storage:           storageService,
		Upload:            uploadService,
		Secret:            secretService,
		GeneralSettings:   generalSettingsService,
		Project:           projectService,
		UserExt:           userExt,
		Menu:              menuService,
		Role:              roleService,
		Plugin:            pluginService,
		ProjectMemberRepo: repos.ProjectMember,
		StepRunRepo:       repos.StepRun,
		LogAggregator:     logAggregator,
	}
}

// getPassword generates a bcrypt hash for a password
func getPassword(password string) ([]byte, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return hash, err
}
