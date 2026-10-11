package repo

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BootstrapAdmin 仅在没有启用的平台超级管理员授权时显式引导现有用户
func BootstrapAdmin(ctx context.Context, db *gorm.DB, username string) error {
	if db == nil || username == "" {
		return fmt.Errorf("database and username are required")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var role model.Role
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("role_id", "is_enabled").Where("role_id = ?", consts.RolePlatformSuperAdmin).Take(&role).Error; err != nil {
			return fmt.Errorf("run migrate before administrator bootstrap: %w", err)
		}
		if role.IsEnabled != 1 {
			return fmt.Errorf("built-in administrator role is disabled")
		}
		var count int64
		if err := tx.Model(&model.RoleGrant{}).Where("role_id = ? AND scope_type = ? AND is_enabled = ?", role.RoleID, string(consts.ScopeTypePlatform), 1).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("administrator already configured; use authorized role management")
		}
		var user model.User
		if err := tx.Select("user_id", "is_enabled").Where("username = ?", username).Take(&user).Error; err != nil {
			return fmt.Errorf("registered user not found: %w", err)
		}
		if user.IsEnabled != 1 {
			return fmt.Errorf("user is disabled")
		}
		return tx.Create(&model.RoleGrant{GrantID: uuid.NewString(), SubjectType: string(consts.SubjectTypeUser), SubjectID: user.UserID, RoleID: role.RoleID, ScopeType: string(consts.ScopeTypePlatform), ScopeID: "", GrantedBy: "operator-bootstrap", IsEnabled: 1}).Error
	})
}
