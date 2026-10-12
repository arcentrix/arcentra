package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"gorm.io/gorm"
)

func liveDeploymentAuthorizationFixture(t *testing.T, team bool) (PipelineDeploymentAuthorizer, *gorm.DB, string, *repo.Repositories) {
	t.Helper()
	service, db, input, _ := releaseFixture(t)
	if err := db.AutoMigrate(&model.User{}, &model.Team{}, &model.TeamMember{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}); err != nil {
		t.Fatal(err)
	}
	for _, object := range []any{&model.User{UserID: "actor", IsEnabled: 1}, &model.Role{RoleID: "deploy-role", Name: "deploy", IsEnabled: 1}, &model.Team{TeamID: "team", IsEnabled: 1}} {
		if err := db.Create(object).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.Project{}).Where("project_id = ?", "project").Updates(map[string]any{"is_enabled": 1, "status": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipe").Update("is_enabled", 1).Error; err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"project:read", "project:update", "pipeline:read", "pipeline:trigger", "release:deploy_prod"} {
		parts := strings.SplitN(action, ":", 2)
		if err := db.Create(&model.Permission{PermissionID: action, ResourceType: parts[0], Action: parts[1]}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: action}).Error; err != nil {
			t.Fatal(err)
		}
	}
	grant := &model.RoleGrant{GrantID: "grant", SubjectType: "user", SubjectID: "actor", RoleID: "deploy-role", ScopeType: "project", ScopeID: "project", IsEnabled: 1}
	if team {
		grant.SubjectType = "team"
		grant.SubjectID = "team"
		if err := db.Create(&model.TeamMember{TeamID: "team", UserID: "actor"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(grant).Error; err != nil {
		t.Fatal(err)
	}
	repositories := repo.NewRepositories(artifactServiceDatabase{db}, nil)
	return NewPipelineDeploymentAuthorizer(repositories, service), db, input.EnvironmentID, repositories
}

func TestDeploymentLiveAuthorizationRechecksCurrentState(t *testing.T) {
	tests := []struct {
		name   string
		team   bool
		revoke func(*gorm.DB) error
	}{
		{"disabled-user", false, func(db *gorm.DB) error {
			return db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error
		}},
		{"deleted-user", false, func(db *gorm.DB) error { return db.Where("user_id = ?", "actor").Delete(&model.User{}).Error }},
		{"disabled-project", false, func(db *gorm.DB) error {
			return db.Model(&model.Project{}).Where("project_id = ?", "project").Update("is_enabled", 0).Error
		}},
		{"inactive-project", false, func(db *gorm.DB) error {
			return db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusInactive).Error
		}},
		{"archived-project", false, func(db *gorm.DB) error {
			return db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error
		}},
		{"disabled-pipeline", false, func(db *gorm.DB) error {
			return db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipe").Update("is_enabled", 0).Error
		}},
		{"revoked-grant", false, func(db *gorm.DB) error {
			return db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error
		}},
		{"expired-grant", false, func(db *gorm.DB) error {
			return db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("expires_at", time.Now().Add(-time.Second)).Error
		}},
		{"disabled-role", false, func(db *gorm.DB) error {
			return db.Model(&model.Role{}).Where("role_id = ?", "deploy-role").Update("is_enabled", 0).Error
		}},
		{"removed-tier-permission", false, func(db *gorm.DB) error {
			return db.Where("role_id = ? AND permission_id = ?", "deploy-role", "release:deploy_prod").Delete(&model.RolePermissionBinding{}).Error
		}},
		{"removed-team-membership", true, func(db *gorm.DB) error {
			return db.Where("team_id = ? AND user_id = ?", "team", "actor").Delete(&model.TeamMember{}).Error
		}},
		{"disabled-team", true, func(db *gorm.DB) error {
			return db.Model(&model.Team{}).Where("team_id = ?", "team").Update("is_enabled", 0).Error
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorize, db, environment, repositories := liveDeploymentAuthorizationFixture(t, test.team)
			ctx := context.Background()
			stale, err := authz.NewAuthorizer(repositories, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := authorize(ctx, "actor", "project", "pipe", environment); err != nil {
				t.Fatal(err)
			}
			if err := test.revoke(db); err != nil {
				t.Fatal(err)
			}
			// 保留旧鉴权器不重新加载，模拟其它副本或长时间运行构建的旧快照。
			subject := authz.Subject{UserID: "actor"}
			if test.team {
				subject.TeamIDs = []string{"team"}
			}
			allowed, err := stale.Check(ctx, subject, "release:deploy_prod", authz.ResourceRef{Type: "project", ID: "project", ProjectID: "project"})
			if err != nil || !allowed {
				t.Fatal("fixture did not retain stale permission")
			}
			if err := authorize(ctx, "actor", "project", "pipe", environment); !errors.Is(err, ErrDeploymentAuthorization) {
				t.Fatalf("revoked state accepted: %v", err)
			}
		})
	}
}

func TestDeploymentLiveAuthorizationFailsClosed(t *testing.T) {
	authorize, _, environment, _ := liveDeploymentAuthorizationFixture(t, false)
	for _, args := range [][4]string{{"other", "project", "pipe", environment}, {"actor", "other", "pipe", environment}, {"actor", "project", "other", environment}, {"actor", "project", "pipe", "other"}, {" actor", "project", "pipe", environment}} {
		if err := authorize(context.Background(), args[0], args[1], args[2], args[3]); err == nil {
			t.Fatalf("invalid scope accepted: %v", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := authorize(ctx, "actor", "project", "pipe", environment); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authorization: %v", err)
	}
	if err := NewPipelineDeploymentAuthorizer(nil, nil)(context.Background(), "actor", "project", "pipe", environment); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}
