// Copyright 2025 Arcentra Authors.
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
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/sso/util"
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
	Setting           *SettingService
	Project           *ProjectService
	Scm               *ScmService
	UserExt           *UserExt
	Menu              *MenuService
	Me                *MeService
	Role              *RoleService
	ProjectMemberRepo repo.IProjectMemberRepository
	TeamMemberRepo    repo.ITeamMemberRepository
	StepRunRepo       repo.IStepRunRepository
	ProjectRepo       repo.IProjectRepository
	PipelineRepo      repo.IPipelineRepository
	StorageRepo       repo.IStorageRepository
	LogAggregator     *LogAggregator
	Approval          *ApprovalService
	ApprovalPolicy    *ApprovalPolicyService
	RoleGrant         *RoleGrantService
	AuditLog          *AuditLogService
	PipelineTemplate  *PipelineTemplateService
	RegistrationToken *RegistrationTokenService
	Authorizer        authz.IAuthorizer
	PipelineEngine    IPipelineEngine // set after process initialization
	repos             *repo.Repositories
}

// Repos 返回 repositories 聚合（仅供 router 偶尔直接访问通用仓储使用）
func (s *Services) Repos() *repo.Repositories {
	return s.repos
}

// NewServices 初始化所有 service
func NewServices(
	db database.IDatabase,
	cacheStore cache.ICache,
	repos *repo.Repositories,
) *Services {
	// 基础服务
	menuService := NewMenuService(repos.Menu)
	userService := NewUserService(
		cacheStore,
		repos.User,
		repos.UserExt,
		repos.RoleGrant,
		repos.Permission,
		repos.Menu,
		repos.Role,
		menuService,
	)
	settingService := NewSettingService(repos.Setting)
	agentService := NewAgentService(repos.Agent, repos.StepRun, settingService, repos.JobRun)
	stateStore := util.NewRedisStateStore(cacheStore)
	identityService := NewIdentityService(repos.Identity, repos.User, repos.UserExt, stateStore)
	teamService := NewTeamService(repos.Team)
	storageService := NewStorageService(repos.Storage)
	uploadService := NewUploadService(repos.Storage)
	secretService := NewSecretService(repos.Secret)
	projectService := NewProjectService(repos.Project)
	scmService := NewScmService(repos.Project, repos.Pipeline)
	userExt := NewUserExt(repos.UserExt)
	roleService := NewRoleService(repos.Role)
	logAggregator := NewLogAggregator(nil, db.Database())
	approvalService := NewApprovalService(repos.Approval, repos.ApprovalDecision, repos.ApprovalPolicy)
	approvalPolicyService := NewApprovalPolicyService(repos.ApprovalPolicy)
	roleGrantService := NewRoleGrantService(repos.RoleGrant)
	auditLogService := NewAuditLogService(repos.AuditLog)
	pipelineTemplateService := NewPipelineTemplateService(repos.PipelineTemplate, repos.Secret)
	registrationTokenService := NewRegistrationTokenService(repos.RegistrationToken)
	agentService.SetRegistrationTokenService(registrationTokenService)
	authorizer, err := authz.NewAuthorizer(repos, cacheStore)
	if err != nil {
		log.Errorw("initialize authorizer failed", "error", err)
	}
	meService := NewMeService(repos.Menu, repos.TeamMember, menuService, authorizer)

	return &Services{
		User:              userService,
		Agent:             agentService,
		Identity:          identityService,
		Team:              teamService,
		Storage:           storageService,
		Upload:            uploadService,
		Secret:            secretService,
		Setting:           settingService,
		Project:           projectService,
		Scm:               scmService,
		UserExt:           userExt,
		Menu:              menuService,
		Me:                meService,
		Role:              roleService,
		ProjectMemberRepo: repos.ProjectMember,
		TeamMemberRepo:    repos.TeamMember,
		StepRunRepo:       repos.StepRun,
		ProjectRepo:       repos.Project,
		PipelineRepo:      repos.Pipeline,
		StorageRepo:       repos.Storage,
		LogAggregator:     logAggregator,
		Approval:          approvalService,
		ApprovalPolicy:    approvalPolicyService,
		RoleGrant:         roleGrantService,
		AuditLog:          auditLogService,
		PipelineTemplate:  pipelineTemplateService,
		RegistrationToken: registrationTokenService,
		Authorizer:        authorizer,
		repos:             repos,
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
