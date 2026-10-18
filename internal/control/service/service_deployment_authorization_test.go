package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

func liveDeploymentAuthorizationFixture(t *testing.T, team bool) (PipelineDeploymentAuthorizer, *gorm.DB, string, *repo.Repositories) {
	t.Helper()
	service, db, input, _ := releaseFixture(t)
	repositories := liveDeploymentRepositories(t, db, team)
	return NewPipelineDeploymentAuthorizer(repositories, service), db, input.EnvironmentID, repositories
}

func liveDeploymentRepositories(t *testing.T, db *gorm.DB, team bool) *repo.Repositories {
	t.Helper()
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
	return repo.NewRepositories(artifactServiceDatabase{db}, nil)
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

func TestDeploymentLiveAuthorizationRejectsLaggingReplica(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*gorm.DB) error
	}{
		{"archived-project", func(db *gorm.DB) error {
			return db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error
		}},
		{"disabled-actor", func(db *gorm.DB) error {
			return db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error
		}},
		{"revoked-grant", func(db *gorm.DB) error {
			return db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			deployment, primary, input, _ := releaseFixture(t)
			repositories := liveDeploymentRepositories(t, primary, false)
			authorize := NewPipelineDeploymentAuthorizer(repositories, deployment)
			ctx := context.Background()
			if err := authorize(ctx, "actor", "project", "pipe", input.EnvironmentID); err != nil {
				t.Fatal(err)
			}
			// A real second SQLite database preserves all identities and the
			// immutable environment while the source receives a later revocation.
			replicaPath := filepath.Join(t.TempDir(), "lagging-replica.db")
			if err := primary.Exec("VACUUM INTO ?", replicaPath).Error; err != nil {
				t.Fatal(err)
			}
			replica, err := gorm.Open(sqlite.Open(replicaPath), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := replica.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			if err := primary.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(replicaPath)}})); err != nil {
				t.Fatal(err)
			}
			if err := authorize(ctx, "actor", "project", "pipe", input.EnvironmentID); err != nil {
				t.Fatal("primary-routed valid authority was denied", err)
			}
			if err := test.revoke(primary); err != nil {
				t.Fatal(err)
			}
			stale := NewPipelineDeploymentAuthorizer(repo.NewRepositories(artifactServiceDatabase{replica}, nil), deployment)
			if err := stale(ctx, "actor", "project", "pipe", input.EnvironmentID); err != nil {
				t.Fatal("replica did not preserve stale authority", err)
			}
			if err := authorize(ctx, "actor", "project", "pipe", input.EnvironmentID); !errors.Is(err, ErrDeploymentAuthorization) {
				t.Fatal("stale replica granted deployment authority", err)
			}
		})
	}
}

type deploymentMutablePrimaryDatabase struct{ db *gorm.DB }

func (d *deploymentMutablePrimaryDatabase) Database() *gorm.DB { return d.db }

func TestDeploymentLiveAuthorizationRequiresCurrentPrimary(t *testing.T) {
	deployment, db, input, _ := releaseFixture(t)
	current := liveDeploymentRepositories(t, db, false)
	// Fully populated public repositories without their source handle must not
	// silently fall back to the ordinary read-routed repository interfaces.
	missing := &repo.Repositories{User: current.User, Team: current.Team, TeamMember: current.TeamMember, Project: current.Project, Pipeline: current.Pipeline, Permission: current.Permission, Role: current.Role, RoleGrant: current.RoleGrant}
	ctx := context.Background()
	if err := NewPipelineDeploymentAuthorizer(missing, deployment)(ctx, "actor", "project", "pipe", input.EnvironmentID); !errors.Is(err, ErrDeploymentAuthorization) {
		t.Fatal("missing primary fell back to ordinary repositories", err)
	}
	mutable := &deploymentMutablePrimaryDatabase{db: db}
	authorize := NewPipelineDeploymentAuthorizer(repo.NewRepositories(mutable, nil), deployment)
	if err := authorize(ctx, "actor", "project", "pipe", input.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	mutable.db = nil
	if err := authorize(ctx, "actor", "project", "pipe", input.EnvironmentID); !errors.Is(err, ErrDeploymentAuthorization) {
		t.Fatal("unavailable primary reused prior authority", err)
	}
}

func TestDeploymentApprovalAuthorizationDoesNotGrantExecution(t *testing.T) {
	_, db, environment, repositories := liveDeploymentAuthorizationFixture(t, true)
	deployment := &DeploymentService{records: repo.NewDeploymentRepo(artifactServiceDatabase{db})}
	authorize := NewDeploymentApprovalAuthorizer(repositories, deployment)
	ctx := context.Background()
	if err := authorize(ctx, "actor", "project", "pipe", environment); !errors.Is(err, ErrDeploymentAuthorization) {
		t.Fatal("deployment permission alone allowed approval", err)
	}
	if err := db.Create(&model.Permission{PermissionID: "approval:approve", ResourceType: "approval", Action: "approve"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: "approval:approve"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("role_id = ? AND permission_id IN ?", "deploy-role", []string{"pipeline:trigger", "project:update"}).Delete(&model.RolePermissionBinding{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := authorize(ctx, "actor", "project", "pipe", environment); err != nil {
		t.Fatal("reviewer without execution authority denied", err)
	}
	if err := NewPipelineDeploymentAuthorizer(repositories, deployment)(ctx, "actor", "project", "pipe", environment); !errors.Is(err, ErrDeploymentAuthorization) {
		t.Fatal("review permission granted execution", err)
	}
	if err := db.Where("team_id = ? AND user_id = ?", "team", "actor").Delete(&model.TeamMember{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := authorize(ctx, "actor", "project", "pipe", environment); !errors.Is(err, ErrDeploymentAuthorization) {
		t.Fatal("revoked reviewer membership allowed approval", err)
	}
}

func TestDeploymentApprovalAuthorizationRechecksRoleAndScope(t *testing.T) {
	for _, permission := range []string{"approval:approve", "release:deploy_prod", "project:read", "pipeline:read"} {
		t.Run(permission, func(t *testing.T) {
			_, db, environment, repositories := liveDeploymentAuthorizationFixture(t, false)
			if err := db.Create(&model.Permission{PermissionID: "approval:approve", ResourceType: "approval", Action: "approve"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: "approval:approve"}).Error; err != nil {
				t.Fatal(err)
			}
			authorize := NewDeploymentApprovalAuthorizer(repositories, &DeploymentService{records: repo.NewDeploymentRepo(artifactServiceDatabase{db})})
			if err := authorize(context.Background(), "actor", "project", "pipe", environment); err != nil {
				t.Fatal(err)
			}
			if err := db.Where("role_id = ? AND permission_id = ?", "deploy-role", permission).Delete(&model.RolePermissionBinding{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := authorize(context.Background(), "actor", "project", "pipe", environment); !errors.Is(err, ErrDeploymentAuthorization) {
				t.Fatal("revoked permission accepted", err)
			}
		})
	}
}

func TestDeploymentRejectOnlyAuthorizationDoesNotApprove(t *testing.T) {
	_, db, environment, repositories := liveDeploymentAuthorizationFixture(t, false)
	deployment := &DeploymentService{records: repo.NewDeploymentRepo(artifactServiceDatabase{db})}
	if err := db.Create(&model.Permission{PermissionID: "approval:reject", ResourceType: "approval", Action: "reject"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: "approval:reject"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("role_id = ? AND permission_id IN ?", "deploy-role", []string{"pipeline:trigger", "project:update"}).Delete(&model.RolePermissionBinding{}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := NewDeploymentRejectionAuthorizer(repositories, deployment)(ctx, "actor", "project", "pipe", environment); err != nil {
		t.Fatal("reject-only reviewer denied", err)
	}
	if err := NewDeploymentApprovalAuthorizer(repositories, deployment)(ctx, "actor", "project", "pipe", environment); !errors.Is(err, ErrDeploymentAuthorization) {
		t.Fatal("reject-only reviewer can approve", err)
	}
	if err := db.Where("role_id = ? AND permission_id = ?", "deploy-role", "approval:reject").Delete(&model.RolePermissionBinding{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewDeploymentRejectionAuthorizer(repositories, deployment)(ctx, "actor", "project", "pipe", environment); !errors.Is(err, ErrDeploymentAuthorization) {
		t.Fatal("revoked rejection permission accepted", err)
	}
}
