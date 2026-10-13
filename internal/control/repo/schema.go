package repo

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MigrateSchema 为明确指定的数据库创建或更新控制面表结构
func MigrateSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	models := schemaModels()
	if err := db.WithContext(ctx).AutoMigrate(models...); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	return seedAuthorization(ctx, db)
}

func schemaModels() []any {
	return []any{
		&model.Agent{},
		&model.DeploymentEnvironment{},
		&model.DeploymentRelease{},
		&model.ApprovalDecision{},
		&model.ApprovalPolicy{},
		&model.ApprovalRequest{},
		&model.AuditLog{},
		&model.GatewayEvent{},
		&model.GatewayEventCursor{},
		&model.Identity{},
		&model.JobRun{},
		&model.Menu{},
		&model.NotificationChannel{},
		&model.NotificationLog{},
		&model.NotificationTemplate{},
		&model.Organization{},
		&model.OrganizationInvitation{},
		&model.OrganizationMember{},
		&model.Permission{},
		&model.Pipeline{},
		&model.PipelineRun{},
		&model.PipelineStage{},
		&model.PipelineTemplate{},
		&model.PipelineTemplateLibrary{},
		&model.Project{},
		&model.ProjectArtifact{},
		&model.ProjectMember{},
		&model.ProjectVariable{},
		&model.ProjectWebhook{},
		&model.RegistrationToken{},
		&model.Role{},
		&model.RoleGrant{},
		&model.RolePermissionBinding{},
		&model.Secret{},
		&model.Setting{},
		&model.StepRun{},
		&model.StepRunArtifact{},
		&model.StepRunLog{},
		&model.StepRunRecord{},
		&model.StorageConfig{},
		&model.SystemEvent{},
		&model.Team{},
		&model.TeamMember{},
		&model.TeamVariable{},
		&model.TerminalOutputRecord{},
		&model.User{},
		&model.UserExt{},
	}
}

func seedAuthorization(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, seed := range consts.SeedBuiltinPermissions() {
			value := model.Permission{PermissionID: seed.PermissionID, ResourceType: seed.ResourceType, Action: seed.Action, ScopeType: string(seed.ScopeType), Description: seed.Description, IsSystem: 1}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&value).Error; err != nil {
				return err
			}
		}
		for _, seed := range consts.SeedBuiltinRoles() {
			value := model.Role{RoleID: seed.RoleID, Name: seed.Name, DisplayName: seed.DisplayName, Description: seed.Description, ScopeType: string(seed.ScopeType), IsSystem: 1, IsEnabled: 1}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&value).Error; err != nil {
				return err
			}
		}
		for _, seed := range consts.SeedBuiltinMenus() {
			visible, enabled := 0, 0
			if seed.IsVisible {
				visible = 1
			}
			if seed.IsEnabled {
				enabled = 1
			}
			value := model.Menu{Meta: "{}", MenuID: seed.MenuID, ParentID: seed.ParentID, Name: seed.Name, Title: seed.Title, Path: seed.Path, Component: seed.Component, Icon: seed.Icon, Order: seed.Order, ScopeType: string(seed.ScopeType), PermissionID: seed.PermissionID, Description: seed.Description, IsVisible: visible, IsEnabled: enabled}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&value).Error; err != nil {
				return err
			}
		}

		for roleID, permissions := range consts.SeedBuiltinRolePermissions() {
			for _, permissionID := range permissions {
				value := model.RolePermissionBinding{RoleID: roleID, PermissionID: permissionID}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&value).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
