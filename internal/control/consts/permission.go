// Copyright 2026 Arcentra Authors.
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

package consts

// SubjectType 表示权限主体类型
type SubjectType string

const (
	// SubjectTypeUser 表示用户主体
	SubjectTypeUser SubjectType = "user"
	// SubjectTypeTeam 表示团队主体
	SubjectTypeTeam SubjectType = "team"
)

// ScopeType 表示权限作用域类型
type ScopeType string

const (
	// ScopeTypePlatform 表示平台级作用域
	ScopeTypePlatform ScopeType = "platform"
	// ScopeTypeOrganization 表示组织级作用域
	ScopeTypeOrganization ScopeType = "org"
	// ScopeTypeProject 表示项目级作用域
	ScopeTypeProject ScopeType = "project"
	// ScopeTypeTeam 表示团队级作用域
	ScopeTypeTeam ScopeType = "team"
	// ScopeTypePipeline 表示流水线级作用域
	ScopeTypePipeline ScopeType = "pipeline"
)

const (
	// RolePlatformSuperAdmin 表示平台超级管理员
	RolePlatformSuperAdmin = "platform_super_admin"
	// RolePlatformAuditor 表示平台审计员
	RolePlatformAuditor = "platform_auditor"
	// RoleOrgOwner 表示组织所有者
	RoleOrgOwner = "org_owner"
	// RoleOrgAdmin 表示组织管理员
	RoleOrgAdmin = "org_admin"
	// RoleOrgMember 表示组织成员
	RoleOrgMember = "org_member"
	// RoleProjectOwner 表示项目所有者
	RoleProjectOwner = "project_owner"
	// RoleProjectMaintainer 表示项目维护者
	RoleProjectMaintainer = "project_maintainer"
	// RoleProjectDeveloper 表示项目开发者
	RoleProjectDeveloper = "project_developer"
	// RoleProjectReporter 表示项目观察者
	RoleProjectReporter = "project_reporter"
	// RoleProjectGuest 表示项目访客
	RoleProjectGuest = "project_guest"
	// RoleTeamOwner 表示团队所有者
	RoleTeamOwner = "team_owner"
	// RoleTeamMaintainer 表示团队维护者
	RoleTeamMaintainer = "team_maintainer"
	// RoleTeamDeveloper 表示团队开发者
	RoleTeamDeveloper = "team_developer"
	// RoleTeamReporter 表示团队观察者
	RoleTeamReporter = "team_reporter"
	// RoleTeamGuest 表示团队访客
	RoleTeamGuest = "team_guest"
	// RoleAgentRuntime 表示 Agent 运行时角色
	RoleAgentRuntime = "agent_runtime"
)

const (
	// PermOrgRead 表示读取组织信息权限
	PermOrgRead = "org:read"
	// PermOrgUpdate 表示更新组织信息权限
	PermOrgUpdate = "org:update"
	// PermOrgManageMember 表示管理组织成员权限
	PermOrgManageMember = "org:manage_member"
	// PermOrgCreateProject 表示创建项目权限
	PermOrgCreateProject = "org:create_project"
	// PermOrgCreateTeam 表示创建团队权限
	PermOrgCreateTeam = "org:create_team"

	// PermProjectRead 表示读取项目信息权限
	PermProjectRead = "project:read"
	// PermProjectUpdate 表示更新项目权限
	PermProjectUpdate = "project:update"
	// PermProjectManageMember 表示管理项目成员权限
	PermProjectManageMember = "project:manage_member"
	// PermProjectManageSecret 表示管理项目密钥权限
	PermProjectManageSecret = "project:manage_secret"
	// PermProjectManageTemplate 表示管理项目模板权限
	PermProjectManageTemplate = "project:manage_template"
	// PermProjectManageTeamAccess 表示管理项目团队访问权限
	PermProjectManageTeamAccess = "project:manage_team_access"

	// PermPipelineCreate 表示创建流水线权限
	PermPipelineCreate = "pipeline:create"
	// PermPipelineRead 表示查看流水线权限
	PermPipelineRead = "pipeline:read"
	// PermPipelineUpdate 表示更新流水线权限
	PermPipelineUpdate = "pipeline:update"
	// PermPipelineDelete 表示删除流水线权限
	PermPipelineDelete = "pipeline:delete"
	// PermPipelineTrigger 表示触发流水线权限
	PermPipelineTrigger = "pipeline:trigger"
	// PermPipelineCancel 表示取消流水线权限
	PermPipelineCancel = "pipeline:cancel"
	// PermPipelinePause 表示暂停流水线权限
	PermPipelinePause = "pipeline:pause"
	// PermPipelineResume 表示恢复流水线权限
	PermPipelineResume = "pipeline:resume"
	// PermPipelineViewLog 表示查看流水线日志权限
	PermPipelineViewLog = "pipeline:view_log"

	// PermApprovalRead 表示读取审批信息权限
	PermApprovalRead = "approval:read"
	// PermApprovalApprove 表示审批通过权限
	PermApprovalApprove = "approval:approve"
	// PermApprovalReject 表示审批拒绝权限
	PermApprovalReject = "approval:reject"
	// PermApprovalConfigurePolicy 表示配置审批策略权限
	PermApprovalConfigurePolicy = "approval:configure_policy"

	// PermReleaseDeployDev 表示部署到开发环境权限
	PermReleaseDeployDev = "release:deploy_dev"
	// PermReleaseDeployStaging 表示部署到预发环境权限
	PermReleaseDeployStaging = "release:deploy_staging"
	// PermReleaseDeployProd 表示部署到生产环境权限
	PermReleaseDeployProd = "release:deploy_prod"

	// PermSecretRead 表示读取密钥权限
	PermSecretRead = "secret:read"
	// PermSecretWrite 表示写入密钥权限
	PermSecretWrite = "secret:write"
	// PermSecretDelete 表示删除密钥权限
	PermSecretDelete = "secret:delete"

	// PermAgentRead 表示读取 Agent 信息权限
	PermAgentRead = "agent:read"
	// PermAgentRegister 表示注册 Agent 权限
	PermAgentRegister = "agent:register"
	// PermAgentApprove 表示审批 Agent 权限
	PermAgentApprove = "agent:approve"
	// PermAgentDelete 表示删除 Agent 权限
	PermAgentDelete = "agent:delete"
	// PermAgentUpdateConfig 表示更新 Agent 配置权限
	PermAgentUpdateConfig = "agent:update_config"

	// PermAuditRead 表示读取审计日志权限
	PermAuditRead = "audit:read"
)

// PermissionSeed 表示内置权限种子定义
type PermissionSeed struct {
	PermissionID string
	ResourceType string
	Action       string
	ScopeType    ScopeType
	Description  string
}

// RoleSeed 表示内置角色种子定义
type RoleSeed struct {
	RoleID      string
	Name        string
	DisplayName string
	Description string
	ScopeType   ScopeType
	IsSystem    bool
	IsEnabled   bool
}

// MenuSeed 表示内置菜单种子定义
type MenuSeed struct {
	MenuID       string
	ParentID     string
	Name         string
	Title        string
	Path         string
	Component    string
	Icon         string
	Order        int
	ScopeType    ScopeType
	PermissionID string
	IsVisible    bool
	IsEnabled    bool
	Description  string
}

// SeedBuiltinPermissions 返回内置权限种子
func SeedBuiltinPermissions() []PermissionSeed {
	return []PermissionSeed{
		{
			PermissionID: PermOrgRead,
			ResourceType: "org", Action: "read",
			ScopeType: ScopeTypeOrganization, Description: "读取组织信息",
		},
		{
			PermissionID: PermOrgUpdate,
			ResourceType: "org", Action: "update",
			ScopeType: ScopeTypeOrganization, Description: "更新组织信息",
		},
		{
			PermissionID: PermOrgManageMember,
			ResourceType: "org", Action: "manage_member",
			ScopeType: ScopeTypeOrganization, Description: "管理组织成员",
		},
		{
			PermissionID: PermOrgCreateProject,
			ResourceType: "org", Action: "create_project",
			ScopeType: ScopeTypeOrganization, Description: "在组织内创建项目",
		},
		{
			PermissionID: PermOrgCreateTeam,
			ResourceType: "org", Action: "create_team",
			ScopeType: ScopeTypeOrganization, Description: "在组织内创建团队",
		},
		{
			PermissionID: PermProjectRead,
			ResourceType: "project", Action: "read",
			ScopeType: ScopeTypeProject, Description: "读取项目",
		},
		{
			PermissionID: PermProjectUpdate,
			ResourceType: "project", Action: "update",
			ScopeType: ScopeTypeProject, Description: "更新项目",
		},
		{
			PermissionID: PermProjectManageMember,
			ResourceType: "project", Action: "manage_member",
			ScopeType: ScopeTypeProject, Description: "管理项目成员",
		},
		{
			PermissionID: PermProjectManageSecret,
			ResourceType: "project", Action: "manage_secret",
			ScopeType: ScopeTypeProject, Description: "管理项目密钥",
		},
		{
			PermissionID: PermProjectManageTemplate,
			ResourceType: "project", Action: "manage_template",
			ScopeType: ScopeTypeProject, Description: "管理项目模板",
		},
		{
			PermissionID: PermProjectManageTeamAccess,
			ResourceType: "project", Action: "manage_team_access",
			ScopeType: ScopeTypeProject, Description: "管理项目团队权限",
		},
		{
			PermissionID: PermPipelineCreate,
			ResourceType: "pipeline", Action: "create",
			ScopeType: ScopeTypeProject, Description: "创建流水线",
		},
		{
			PermissionID: PermPipelineRead,
			ResourceType: "pipeline", Action: "read",
			ScopeType: ScopeTypeProject, Description: "读取流水线",
		},
		{
			PermissionID: PermPipelineUpdate,
			ResourceType: "pipeline", Action: "update",
			ScopeType: ScopeTypeProject, Description: "更新流水线",
		},
		{
			PermissionID: PermPipelineDelete,
			ResourceType: "pipeline", Action: "delete",
			ScopeType: ScopeTypeProject, Description: "删除流水线",
		},
		{
			PermissionID: PermPipelineTrigger,
			ResourceType: "pipeline", Action: "trigger",
			ScopeType: ScopeTypeProject, Description: "触发流水线执行",
		},
		{
			PermissionID: PermPipelineCancel,
			ResourceType: "pipeline", Action: "cancel",
			ScopeType: ScopeTypeProject, Description: "取消流水线执行",
		},
		{
			PermissionID: PermPipelinePause,
			ResourceType: "pipeline", Action: "pause",
			ScopeType: ScopeTypeProject, Description: "暂停流水线",
		},
		{
			PermissionID: PermPipelineResume,
			ResourceType: "pipeline", Action: "resume",
			ScopeType: ScopeTypeProject, Description: "恢复流水线",
		},
		{
			PermissionID: PermPipelineViewLog,
			ResourceType: "pipeline", Action: "view_log",
			ScopeType: ScopeTypeProject, Description: "查看流水线日志",
		},
		{
			PermissionID: PermApprovalRead,
			ResourceType: "approval", Action: "read",
			ScopeType: ScopeTypeProject, Description: "读取审批单",
		},
		{
			PermissionID: PermApprovalApprove,
			ResourceType: "approval", Action: "approve",
			ScopeType: ScopeTypeProject, Description: "审批通过",
		},
		{
			PermissionID: PermApprovalReject,
			ResourceType: "approval", Action: "reject",
			ScopeType: ScopeTypeProject, Description: "审批拒绝",
		},
		{
			PermissionID: PermApprovalConfigurePolicy,
			ResourceType: "approval", Action: "configure_policy",
			ScopeType: ScopeTypeProject, Description: "配置审批策略",
		},
		{
			PermissionID: PermReleaseDeployDev,
			ResourceType: "release", Action: "deploy_dev",
			ScopeType: ScopeTypeProject, Description: "部署到开发环境",
		},
		{
			PermissionID: PermReleaseDeployStaging,
			ResourceType: "release", Action: "deploy_staging",
			ScopeType: ScopeTypeProject, Description: "部署到预发环境",
		},
		{
			PermissionID: PermReleaseDeployProd,
			ResourceType: "release", Action: "deploy_prod",
			ScopeType: ScopeTypeProject, Description: "部署到生产环境",
		},
		{
			PermissionID: PermSecretRead,
			ResourceType: "secret", Action: "read",
			ScopeType: ScopeTypeProject, Description: "读取密钥",
		},
		{
			PermissionID: PermSecretWrite,
			ResourceType: "secret", Action: "write",
			ScopeType: ScopeTypeProject, Description: "写入密钥",
		},
		{
			PermissionID: PermSecretDelete,
			ResourceType: "secret", Action: "delete",
			ScopeType: ScopeTypeProject, Description: "删除密钥",
		},
		{
			PermissionID: PermAgentRead,
			ResourceType: "agent", Action: "read",
			ScopeType: ScopeTypePlatform, Description: "读取 Agent",
		},
		{
			PermissionID: PermAgentRegister,
			ResourceType: "agent", Action: "register",
			ScopeType: ScopeTypePlatform, Description: "注册 Agent",
		},
		{
			PermissionID: PermAgentApprove,
			ResourceType: "agent", Action: "approve",
			ScopeType: ScopeTypePlatform, Description: "审批 Agent",
		},
		{
			PermissionID: PermAgentDelete,
			ResourceType: "agent", Action: "delete",
			ScopeType: ScopeTypePlatform, Description: "删除 Agent",
		},
		{
			PermissionID: PermAgentUpdateConfig,
			ResourceType: "agent", Action: "update_config",
			ScopeType: ScopeTypePlatform, Description: "更新 Agent 配置",
		},
		{
			PermissionID: PermAuditRead,
			ResourceType: "audit", Action: "read",
			ScopeType: ScopeTypePlatform, Description: "读取审计日志",
		},
	}
}

// SeedBuiltinRoles 返回内置角色种子
func SeedBuiltinRoles() []RoleSeed {
	return []RoleSeed{
		{
			RoleID: RolePlatformSuperAdmin, Name: "PlatformSuperAdmin",
			DisplayName: "平台超级管理员", Description: "平台最高权限",
			ScopeType: ScopeTypePlatform, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RolePlatformAuditor, Name: "PlatformAuditor",
			DisplayName: "平台审计员", Description: "平台只读与审计权限",
			ScopeType: ScopeTypePlatform, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleOrgOwner, Name: "OrgOwner",
			DisplayName: "组织所有者", Description: "组织最高权限",
			ScopeType: ScopeTypeOrganization, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleOrgAdmin, Name: "OrgAdmin",
			DisplayName: "组织管理员", Description: "组织管理权限",
			ScopeType: ScopeTypeOrganization, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleOrgMember, Name: "OrgMember",
			DisplayName: "组织成员", Description: "组织基本权限",
			ScopeType: ScopeTypeOrganization, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleProjectOwner, Name: "ProjectOwner",
			DisplayName: "项目所有者", Description: "项目最高权限",
			ScopeType: ScopeTypeProject, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleProjectMaintainer, Name: "ProjectMaintainer",
			DisplayName: "项目维护者", Description: "项目维护权限",
			ScopeType: ScopeTypeProject, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleProjectDeveloper, Name: "ProjectDeveloper",
			DisplayName: "项目开发者", Description: "项目开发权限",
			ScopeType: ScopeTypeProject, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleProjectReporter, Name: "ProjectReporter",
			DisplayName: "项目观察者", Description: "项目观察权限",
			ScopeType: ScopeTypeProject, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleProjectGuest, Name: "ProjectGuest",
			DisplayName: "项目访客", Description: "项目访客权限",
			ScopeType: ScopeTypeProject, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleTeamOwner, Name: "TeamOwner",
			DisplayName: "团队所有者", Description: "团队最高权限",
			ScopeType: ScopeTypeTeam, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleTeamMaintainer, Name: "TeamMaintainer",
			DisplayName: "团队维护者", Description: "团队维护权限",
			ScopeType: ScopeTypeTeam, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleTeamDeveloper, Name: "TeamDeveloper",
			DisplayName: "团队开发者", Description: "团队开发权限",
			ScopeType: ScopeTypeTeam, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleTeamReporter, Name: "TeamReporter",
			DisplayName: "团队观察者", Description: "团队观察权限",
			ScopeType: ScopeTypeTeam, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleTeamGuest, Name: "TeamGuest",
			DisplayName: "团队访客", Description: "团队访客权限",
			ScopeType: ScopeTypeTeam, IsSystem: true, IsEnabled: true,
		},
		{
			RoleID: RoleAgentRuntime, Name: "AgentRuntime",
			DisplayName: "Agent 运行时", Description: "Agent 进程内置权限",
			ScopeType: ScopeTypePlatform, IsSystem: true, IsEnabled: true,
		},
	}
}

// SeedBuiltinRolePermissions 返回内置角色权限映射
func SeedBuiltinRolePermissions() map[string][]string {
	allProject := []string{
		PermProjectRead, PermProjectUpdate, PermProjectManageMember,
		PermProjectManageSecret, PermProjectManageTemplate, PermProjectManageTeamAccess,
		PermPipelineCreate, PermPipelineRead, PermPipelineUpdate, PermPipelineDelete,
		PermPipelineTrigger, PermPipelineCancel, PermPipelinePause, PermPipelineResume,
		PermPipelineViewLog,
		PermApprovalRead, PermApprovalApprove, PermApprovalReject, PermApprovalConfigurePolicy,
		PermReleaseDeployDev, PermReleaseDeployStaging, PermReleaseDeployProd,
		PermSecretRead, PermSecretWrite, PermSecretDelete,
	}
	return map[string][]string{
		RolePlatformSuperAdmin: append(append([]string{}, allProject...),
			PermOrgRead, PermOrgUpdate, PermOrgManageMember,
			PermOrgCreateProject, PermOrgCreateTeam,
			PermAgentRead, PermAgentRegister, PermAgentApprove,
			PermAgentDelete, PermAgentUpdateConfig, PermAuditRead,
		),
		RolePlatformAuditor: {
			PermAuditRead, PermAgentRead, PermOrgRead,
			PermProjectRead, PermPipelineRead, PermPipelineViewLog, PermApprovalRead,
		},
		RoleOrgOwner: {
			PermOrgRead, PermOrgUpdate, PermOrgManageMember,
			PermOrgCreateProject, PermOrgCreateTeam, PermProjectRead, PermProjectUpdate,
		},
		RoleOrgAdmin: {
			PermOrgRead, PermOrgUpdate, PermOrgManageMember,
			PermOrgCreateProject, PermProjectRead, PermProjectUpdate,
		},
		RoleOrgMember:    {PermOrgRead, PermProjectRead},
		RoleProjectOwner: allProject,
		RoleProjectMaintainer: {
			PermProjectRead, PermProjectUpdate, PermProjectManageMember,
			PermProjectManageSecret, PermProjectManageTemplate, PermProjectManageTeamAccess,
			PermPipelineCreate, PermPipelineRead, PermPipelineUpdate, PermPipelineDelete,
			PermPipelineTrigger, PermPipelineCancel, PermPipelinePause, PermPipelineResume,
			PermPipelineViewLog,
			PermApprovalRead, PermApprovalApprove, PermApprovalReject, PermApprovalConfigurePolicy,
			PermReleaseDeployDev, PermReleaseDeployStaging, PermReleaseDeployProd,
			PermSecretRead, PermSecretWrite, PermSecretDelete,
		},
		RoleProjectDeveloper: {
			PermProjectRead, PermPipelineRead, PermPipelineTrigger,
			PermPipelineCancel, PermPipelineViewLog, PermApprovalRead,
			PermReleaseDeployDev, PermReleaseDeployStaging, PermSecretRead,
		},
		RoleProjectReporter: {PermProjectRead, PermPipelineRead, PermPipelineViewLog, PermApprovalRead},
		RoleProjectGuest:    {PermProjectRead, PermPipelineRead},
		RoleTeamOwner:       {PermProjectRead, PermPipelineRead, PermPipelineTrigger, PermPipelineViewLog},
		RoleTeamMaintainer:  {PermProjectRead, PermPipelineRead, PermPipelineTrigger, PermPipelineViewLog},
		RoleTeamDeveloper:   {PermProjectRead, PermPipelineRead, PermPipelineTrigger, PermPipelineViewLog},
		RoleTeamReporter:    {PermProjectRead, PermPipelineRead, PermPipelineViewLog},
		RoleTeamGuest:       {PermProjectRead, PermPipelineRead},
		RoleAgentRuntime:    {PermAgentRegister, PermAgentRead},
	}
}

// SeedBuiltinMenus 返回内置菜单种子
func SeedBuiltinMenus() []MenuSeed {
	return []MenuSeed{
		{
			MenuID: "menu_dashboard", ParentID: "", Name: "Dashboard",
			Title: "Overview", Path: "/", Component: "Dashboard",
			Icon: "LayoutDashboard", Order: 1, ScopeType: ScopeTypePlatform,
			PermissionID: PermProjectRead, IsVisible: true, IsEnabled: true,
			Description: "首页",
		},
		{
			MenuID: "menu_projects", ParentID: "", Name: "Projects",
			Title: "Projects", Path: "/projects", Component: "Projects",
			Icon: "Frame", Order: 2, ScopeType: ScopeTypePlatform,
			PermissionID: PermProjectRead, IsVisible: true, IsEnabled: true,
			Description: "项目列表",
		},
		{
			MenuID: "menu_build", ParentID: "", Name: "Build",
			Title: "Build", Path: "/build", Component: "Build",
			Icon: "Hammer", Order: 3, ScopeType: ScopeTypePlatform,
			PermissionID: PermPipelineRead, IsVisible: true, IsEnabled: true,
			Description: "构建中心",
		},
		{
			MenuID: "menu_deploy", ParentID: "", Name: "Deploy",
			Title: "Deploy", Path: "/deploy", Component: "Deploy",
			Icon: "Rocket", Order: 4, ScopeType: ScopeTypePlatform,
			PermissionID: PermReleaseDeployDev, IsVisible: true, IsEnabled: true,
			Description: "部署中心",
		},
		{
			MenuID: "menu_agents", ParentID: "", Name: "Agents",
			Title: "Agents", Path: "/agents", Component: "Models/Agents/Overview",
			Icon: "Bot", Order: 5, ScopeType: ScopeTypePlatform,
			PermissionID: PermAgentRead, IsVisible: true, IsEnabled: true,
			Description: "Agent 列表",
		},
		{
			MenuID: "menu_observe", ParentID: "", Name: "Observe",
			Title: "Observe", Path: "/observe", Component: "Observe",
			Icon: "Activity", Order: 6, ScopeType: ScopeTypePlatform,
			PermissionID: PermPipelineViewLog, IsVisible: true, IsEnabled: true,
			Description: "观测中心",
		},
		{
			MenuID: "menu_secure", ParentID: "", Name: "Secure",
			Title: "Secure", Path: "/secure", Component: "Secure",
			Icon: "Shield", Order: 7, ScopeType: ScopeTypePlatform,
			PermissionID: PermSecretRead, IsVisible: true, IsEnabled: true,
			Description: "安全中心",
		},
		{
			MenuID: "menu_settings", ParentID: "", Name: "Settings",
			Title: "Settings", Path: "/settings", Component: "Settings",
			Icon: "Settings2", Order: 8, ScopeType: ScopeTypePlatform,
			PermissionID: PermOrgRead, IsVisible: true, IsEnabled: true,
			Description: "设置",
		},
		{
			MenuID: "menu_users", ParentID: "", Name: "Users",
			Title: "Users", Path: "/users", Component: "Users",
			Icon: "Users", Order: 9, ScopeType: ScopeTypePlatform,
			PermissionID: PermOrgManageMember, IsVisible: false, IsEnabled: true,
			Description: "用户管理",
		},
		{
			MenuID: "menu_roles", ParentID: "", Name: "Roles",
			Title: "Roles", Path: "/roles", Component: "Roles",
			Icon: "Shield", Order: 10, ScopeType: ScopeTypePlatform,
			PermissionID: PermOrgManageMember, IsVisible: false, IsEnabled: true,
			Description: "角色管理",
		},
		{
			MenuID: "menu_permissions", ParentID: "", Name: "Permissions",
			Title: "Permissions", Path: "/permissions", Component: "Permissions",
			Icon: "Shield", Order: 11, ScopeType: ScopeTypePlatform,
			PermissionID: PermOrgManageMember, IsVisible: false, IsEnabled: true,
			Description: "权限字典",
		},
	}
}
