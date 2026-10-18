// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func organizationSchemaFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "old.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	return db
}
func TestOrganizationMigrationFailsBeforeMutationOnAmbiguousLegacyRows(t *testing.T) {
	for _, duplicate := range []string{"organization-id", "organization-name", "organization-member", "project-id"} {
		t.Run(duplicate, func(t *testing.T) {
			db := organizationSchemaFixture(t)
			switch duplicate {
			case "organization-id", "organization-name":
				require.NoError(t, db.AutoMigrate(&model.Organization{}))
				require.NoError(t, db.Migrator().DropIndex(&model.Organization{}, "idx_organization_org_id"))
				require.NoError(t, db.Migrator().DropIndex(&model.Organization{}, "idx_organization_name"))
				first := model.Organization{OrgID: "one", Name: "one"}
				second := model.Organization{OrgID: "two", Name: "two"}
				if duplicate == "organization-id" {
					second.OrgID = first.OrgID
				} else {
					second.Name = first.Name
				}
				require.NoError(t, db.Create(&first).Error)
				require.NoError(t, db.Create(&second).Error)
			case "organization-member":
				require.NoError(t, db.AutoMigrate(&model.OrganizationMember{}))
				require.NoError(t, db.Migrator().DropIndex(&model.OrganizationMember{}, "idx_org_member_user"))
				for range 2 {
					require.NoError(t, db.Create(&model.OrganizationMember{OrgID: "one", UserID: "alice"}).Error)
				}
			case "project-id":
				require.NoError(t, db.AutoMigrate(&model.Project{}))
				require.NoError(t, db.Migrator().DropIndex(&model.Project{}, "idx_project_project_id"))
				for range 2 {
					require.NoError(t, db.Create(&model.Project{ProjectID: "duplicate"}).Error)
				}
			}
			require.ErrorContains(t, MigrateSchema(context.Background(), db), "preflight")
			require.False(t, db.Migrator().HasTable(&model.RoleGrant{}), "preflight must precede every migration/seed write")
		})
	}
}
func TestOrganizationMigrationPreservesLegacyAssociationsWithoutGrants(t *testing.T) {
	db := organizationSchemaFixture(t)
	require.NoError(t, db.AutoMigrate(&model.Project{}, &model.ProjectMember{}))
	require.NoError(t, db.Create(&model.Project{ProjectID: "legacy", OrgID: "scope-id-only", Name: "legacy"}).Error)
	require.NoError(t, db.Create(&model.ProjectMember{ProjectID: "legacy", UserID: "alice"}).Error)
	require.NoError(t, MigrateSchema(context.Background(), db))
	require.NoError(t, MigrateSchema(context.Background(), db))
	var member model.ProjectMember
	require.NoError(t, db.Take(&member).Error)
	require.Equal(t, uint64(1), member.Version)
	require.Empty(t, member.ManagedGrantID)
	require.Empty(t, member.ManagedRoleID)
	require.Equal(t, 1, member.IsEnabled)
	for _, value := range []any{&model.Organization{}, &model.OrganizationMember{}, &model.RoleGrant{}} {
		var count int64
		require.NoError(t, db.Model(value).Count(&count).Error)
		require.Zero(t, count)
	}
}

// These structs preserve the exact baseline persistence tags before migration 010.
type legacyOrganizationShape struct {
	model.BaseModel
	OrgID       string         `gorm:"column:org_id" json:"orgId"`                // 组织唯一标识
	Name        string         `gorm:"column:name" json:"name"`                   // 组织名称
	DisplayName string         `gorm:"column:display_name" json:"displayName"`    // 组织显示名称
	Description string         `gorm:"column:description" json:"description"`     // 组织描述
	Logo        string         `gorm:"column:logo" json:"logo"`                   // 组织Logo URL
	Website     string         `gorm:"column:website" json:"website"`             // 组织官网
	Email       string         `gorm:"column:email" json:"email"`                 // 组织联系邮箱
	Phone       string         `gorm:"column:phone" json:"phone"`                 // 组织联系电话
	Address     string         `gorm:"column:address" json:"address"`             // 组织地址
	Settings    datatypes.JSON `gorm:"column:settings;type:json" json:"settings"` // 组织设置
	Plan        string         `gorm:"column:plan" json:"plan"`                   // 订阅计划(free/pro/enterprise)
	Status      int            `gorm:"column:status" json:"status"`               // 状态: 0-未激活, 1-正常, 2-冻结, 3-已删除
	OwnerUserID string         `gorm:"column:owner_user_id" json:"ownerUserId"`   // 组织所有者用户ID
	IsEnabled   int            `gorm:"column:is_enabled" json:"isEnabled"`        // 0: disabled, 1: enabled

	// 统计字段
	TotalMembers  int `gorm:"column:total_members" json:"totalMembers"`   // 成员总数
	TotalTeams    int `gorm:"column:total_teams" json:"totalTeams"`       // 团队总数
	TotalProjects int `gorm:"column:total_projects" json:"totalProjects"` // 项目总数
}

func (legacyOrganizationShape) TableName() string { return "organization" }

type legacyOrganizationMemberShape struct {
	model.BaseModel
	OrgID     string `gorm:"column:org_id" json:"orgId"`         // 组织ID
	UserID    string `gorm:"column:user_id" json:"userId"`       // 用户ID
	Username  string `gorm:"column:username" json:"username"`    // 用户名(冗余)
	Email     string `gorm:"column:email" json:"email"`          // 邮箱(冗余)
	InvitedBy string `gorm:"column:invited_by" json:"invitedBy"` // 邀请人用户ID
	Status    int    `gorm:"column:status" json:"status"`        // 状态: 0-待接受, 1-正常, 2-禁用
}

func (legacyOrganizationMemberShape) TableName() string { return "organization_member" }

type legacyProjectShape struct {
	model.BaseModel
	ProjectID     string         `gorm:"column:project_id" json:"projectId"`               // 项目唯一标识
	OrgID         string         `gorm:"column:org_id" json:"orgId"`                       // 所属组织ID
	Name          string         `gorm:"column:name" json:"name"`                          // 项目名称
	DisplayName   string         `gorm:"column:display_name" json:"displayName"`           // 项目显示名称
	Namespace     string         `gorm:"column:namespace" json:"namespace"`                // 项目命名空间(org_name/project_name)
	Description   string         `gorm:"column:description" json:"description"`            // 项目描述
	RepoURL       string         `gorm:"column:repo_url" json:"repoUrl"`                   // 代码仓库URL
	RepoType      string         `gorm:"column:repo_type" json:"repoType"`                 // 仓库类型(git/svn/gitlab/github/gitee)
	DefaultBranch string         `gorm:"column:default_branch" json:"defaultBranch"`       // 默认分支
	AuthType      int            `gorm:"column:auth_type" json:"authType"`                 // 认证类型: 0-无, 1-用户名密码, 2-Token, 3-SSH密钥
	Credential    string         `gorm:"column:credential" json:"-"`                       // 认证凭证(加密存储)
	TriggerMode   int            `gorm:"column:trigger_mode" json:"triggerMode"`           // 触发模式: 1-手动, 2-Webhook, 4-定时, 8-Push触发(可组合)
	WebhookSecret string         `gorm:"column:webhook_secret" json:"-"`                   // Webhook密钥
	CronExpr      string         `gorm:"column:cron_expr" json:"cronExpr"`                 // 定时任务Cron表达式
	BuildConfig   datatypes.JSON `gorm:"column:build_config;type:json" json:"buildConfig"` // 构建配置
	EnvVars       datatypes.JSON `gorm:"column:env_vars;type:json" json:"envVars"`         // 环境变量
	Settings      datatypes.JSON `gorm:"column:settings;type:json" json:"settings"`        // 项目设置
	Tags          string         `gorm:"column:tags" json:"tags"`                          // 项目标签(逗号分隔)
	Language      string         `gorm:"column:language" json:"language"`                  // 主要编程语言
	Framework     string         `gorm:"column:framework" json:"framework"`                // 使用的框架
	Status        int            `gorm:"column:status" json:"status"`                      // 项目状态: 0-未激活, 1-正常, 2-归档, 3-禁用
	Visibility    int            `gorm:"column:visibility" json:"visibility"`              // 可见性: 0-私有, 1-内部, 2-公开
	AccessLevel   string         `gorm:"column:access_level" json:"accessLevel"`           // 默认访问级别(owner/team/org)
	CreatedBy     string         `gorm:"column:created_by" json:"createdBy"`               // 创建者用户ID
	IsEnabled     int            `gorm:"column:is_enabled" json:"isEnabled"`               // 0: disabled, 1: enabled
	Icon          string         `gorm:"column:icon" json:"icon"`                          // 项目图标URL
	Homepage      string         `gorm:"column:homepage" json:"homepage"`                  // 项目主页

	// 统计字段
	TotalPipelines int `gorm:"column:total_pipelines" json:"totalPipelines"` // 流水线总数
	TotalBuilds    int `gorm:"column:total_builds" json:"totalBuilds"`       // 构建总次数
	SuccessBuilds  int `gorm:"column:success_builds" json:"successBuilds"`   // 成功构建次数
	FailedBuilds   int `gorm:"column:failed_builds" json:"failedBuilds"`     // 失败构建次数
}

func (legacyProjectShape) TableName() string { return "project" }

type legacyProjectMemberShape struct {
	model.BaseModel
	ProjectID string `gorm:"column:project_id;not null;index:idx_project_user,unique;type:varchar(64)" json:"projectId"`
	UserID    string `gorm:"column:user_id;not null;index:idx_project_user,unique;index:idx_project_member_user;type:varchar(64)" json:"userId"`
}

func (legacyProjectMemberShape) TableName() string { return "project_member" }

func seedOldOrganizationShape(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&legacyOrganizationShape{}, &legacyOrganizationMemberShape{}, &legacyProjectShape{}, &legacyProjectMemberShape{}))
	require.False(t, db.Migrator().HasColumn(&model.Organization{}, "Version"))
	require.False(t, db.Migrator().HasColumn(&model.OrganizationMember{}, "ManagedGrantID"))
	require.False(t, db.Migrator().HasColumn(&model.ProjectMember{}, "IsEnabled"))
	require.False(t, db.Migrator().HasColumn(&model.Project{}, "SettingsVersion"))
	require.NoError(t, db.Create(&legacyOrganizationShape{OrgID: "existing-org", Name: "existing", Status: model.OrgStatusActive, IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&legacyOrganizationMemberShape{OrgID: "existing-org", UserID: "old-user", Status: model.OrgMemberStatusActive}).Error)
	require.NoError(t, db.Create(&legacyProjectShape{ProjectID: "legacy-project", OrgID: "scope-only", Name: "legacy", Status: model.ProjectStatusActive, IsEnabled: 1, Settings: datatypes.JSON(`{"preserve":"opaque"}`)}).Error)
	require.NoError(t, db.Create(&legacyProjectMemberShape{ProjectID: "legacy-project", UserID: "old-user"}).Error)
}
func verifyOldOrganizationUpgrade(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, MigrateSchema(context.Background(), db))
	require.NoError(t, MigrateSchema(context.Background(), db))
	var org model.Organization
	require.NoError(t, db.Where("org_id = ?", "existing-org").Take(&org).Error)
	require.Equal(t, uint64(1), org.Version)
	require.Equal(t, model.OrgStatusActive, org.Status)
	var member model.OrganizationMember
	require.NoError(t, db.Where("org_id = ?", "existing-org").Take(&member).Error)
	require.Equal(t, uint64(1), member.Version)
	require.Empty(t, member.ManagedGrantID)
	require.Empty(t, member.ManagedRoleID)
	var project model.Project
	require.NoError(t, db.Where("project_id = ?", "legacy-project").Take(&project).Error)
	require.Equal(t, "scope-only", project.OrgID)
	require.Equal(t, uint64(1), project.SettingsVersion)
	require.JSONEq(t, `{"preserve":"opaque"}`, string(project.Settings))
	var pm model.ProjectMember
	require.NoError(t, db.Where("project_id = ?", "legacy-project").Take(&pm).Error)
	require.Equal(t, uint64(1), pm.Version)
	require.Equal(t, 1, pm.IsEnabled)
	require.Empty(t, pm.ManagedGrantID)
	require.Empty(t, pm.ManagedRoleID)
	var grants, organizations int64
	require.NoError(t, db.Model(&model.RoleGrant{}).Count(&grants).Error)
	require.Zero(t, grants)
	require.NoError(t, db.Model(&model.Organization{}).Count(&organizations).Error)
	require.Equal(t, int64(1), organizations)
	for _, idx := range []struct {
		table any
		name  string
	}{{&model.Organization{}, "idx_organization_org_id"}, {&model.Organization{}, "idx_organization_name"}, {&model.OrganizationMember{}, "idx_org_member_user"}, {&model.Project{}, "idx_project_project_id"}, {&model.ProjectMember{}, "idx_project_user"}} {
		require.True(t, db.Migrator().HasIndex(idx.table, idx.name), idx.name)
	}
}
func TestOrganizationMigrationUpgradesOldShapeWithoutAdoptingLegacyScope(t *testing.T) {
	db := organizationSchemaFixture(t)
	seedOldOrganizationShape(t, db)
	verifyOldOrganizationUpgrade(t, db)
}
