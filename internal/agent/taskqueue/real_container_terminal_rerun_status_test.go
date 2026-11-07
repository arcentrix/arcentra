//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/id"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/redis/go-redis/v9"
)

// TestContainerTerminalRerunProductionStatus 验证夹具播种后真实登录及 Copilot 状态路由使用同一权限快照
// SQLite and the bounded Redis transport are local substitutes, not hosted
// MySQL, Redis wire, containerd or browser acceptance evidence.
func TestContainerTerminalRerunProductionStatus(t *testing.T) {
	commands := &terminalSessionCommands{store: cache.NewFastCache(cache.FastCacheConfig{MaxBytes: 1024 * 1024})}
	t.Cleanup(commands.store.Clear)
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	client.AddHook(commands)
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisCache(client)
	var services *controlservice.Services
	options := controllerWorkerFixtureOptions{repositoryCache: store, timeout: 30 * time.Second}
	options.configureRepositories = func(repositories *repo.Repositories) {
		adapter := repositories.GetDB()
		if err := adapter.Database().WithContext(context.Background()).AutoMigrate(&model.User{}, &model.UserExt{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Menu{}, &model.Team{}, &model.TeamMember{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.InboxDelivery{}, &model.InboxCursor{}, &model.InboxWork{}, &model.AuditLog{}); err != nil {
			t.Fatal(err)
		}
		var err error
		services, err = controlservice.ProvideServices(adapter, store, repositories, taskpayload.OfflineRuntimePolicy{})
		if err != nil || services.Authorizer == nil {
			t.Fatal("production service dependency construction failed", err)
		}
	}
	f := newControllerWorkerFixture(t, false, options)
	actor := "terminal-owner-" + id.GetUild()
	grants := seedContainerApprovalActors(t, f, actor, nil)
	subject := authz.Subject{UserID: actor}
	scope := authz.ResourceRef{Type: "project", ID: f.projectID, ProjectID: f.projectID}
	// The production Services is created before direct fixture seeding. A
	// transaction-local reader sees the grant; its pre-seed snapshot does not.
	if allowed, err := services.Authorizer.Check(f.ctx, subject, "project:read", scope); err != nil || allowed {
		t.Fatal("regression requires the original pre-seed authorization snapshot", err)
	}
	live, err := authz.NewLiveAuthorizer(f.ctx, f.repos)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := live.Check(f.ctx, subject, "project:read", scope); err != nil || !allowed {
		t.Fatal("owner is missing its existing exact project read grant", err)
	}
	originalAuthorizer := services.Authorizer
	h := newTerminalRerunHTTP(t, f, services, store, &controlconfig.AppConfig{}, []string{actor})
	if services.Authorizer != originalAuthorizer {
		t.Fatal("fixture replaced the runtime owner's shared authorizer")
	}
	var user model.UserInfo
	h.request(t, f.ctx, actor, http.MethodGet, "/users/fetch", nil, &user)
	if user.UserID != actor {
		t.Fatal("status request did not retain the original real login owner")
	}
	path := "/projects/" + f.projectID + "/copilot/status"
	var status controlservice.CopilotStatus
	h.request(t, f.ctx, actor, http.MethodGet, path, nil, &status)
	if status.Configured || len(status.Models) != 0 {
		t.Fatal("fixture must not configure a provider to obtain read authorization")
	}
	terminalSessionResponse(t, h, f.ctx, http.MethodGet, path, "", nil, http.StatusUnauthorized)
	foreign := &model.Project{ProjectID: "foreign-" + id.GetUild(), IsEnabled: 1, Status: model.ProjectStatusActive}
	if err := f.db.WithContext(f.ctx).Create(foreign).Error; err != nil {
		t.Fatal(err)
	}
	terminalSessionResponse(t, h, f.ctx, http.MethodGet, "/projects/"+foreign.ProjectID+"/copilot/status", h.tokens[actor], nil, http.StatusForbidden)
	if allowed, err := services.Authorizer.Check(f.ctx, subject, "approval:approve", scope); err != nil || allowed {
		t.Fatal("requester acquired independent reviewer authority", err)
	}
	var grant model.RoleGrant
	var role model.Role
	if err := f.db.WithContext(f.ctx).Where("grant_id = ?", grants[0]).First(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Where("role_id = ?", grant.RoleID).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"disabled-role", "deleted-role", "expired-grant", "revoked-without-reload", "disabled-user"} {
		t.Run(mutation, func(t *testing.T) {
			var restore func() error
			switch mutation {
			case "disabled-role":
				disabled := 0
				if err := services.Role.UpdateRoleByRoleID(f.ctx, role.RoleID, &model.UpdateRoleReq{IsEnabled: &disabled}); err != nil {
					t.Fatal(err)
				}
				restore = func() error {
					return f.db.WithContext(f.ctx).Model(&model.Role{}).Where("role_id = ?", role.RoleID).Update("is_enabled", 1).Error
				}
			case "deleted-role":
				if err := services.Role.DeleteRoleByRoleID(f.ctx, role.RoleID); err != nil {
					t.Fatal(err)
				}
				restore = func() error {
					return f.db.WithContext(f.ctx).Model(&model.Role{}).Where("role_id = ?", role.RoleID).Update("is_enabled", 1).Error
				}
			case "expired-grant":
				if err := f.db.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", grant.GrantID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
				restore = func() error {
					return f.db.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", grant.GrantID).Update("expires_at", nil).Error
				}
			case "revoked-without-reload":
				if err := services.RoleGrant.RevokeByGrantID(f.ctx, grant.GrantID); err != nil {
					t.Fatal(err)
				}
				restore = func() error {
					return f.db.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", grant.GrantID).Update("is_enabled", 1).Error
				}
			case "disabled-user":
				if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", actor).Update("is_enabled", 0).Error; err != nil {
					t.Fatal(err)
				}
				restore = func() error {
					return f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", actor).Update("is_enabled", 1).Error
				}
			}
			defer func() {
				if err := restore(); err != nil {
					t.Error(err)
				}
			}()
			// The unchanged startup snapshot still allows this same actor.
			// HTTP must independently consult current primary authority.
			if allowed, err := services.Authorizer.Check(f.ctx, subject, "project:read", scope); err != nil || !allowed {
				t.Fatal("regression requires a stale generic allow", err)
			}
			terminalSessionResponse(t, h, f.ctx, http.MethodGet, path, h.tokens[actor], nil, http.StatusForbidden)
		})
		h.request(t, f.ctx, actor, http.MethodGet, path, nil, &status)
	}
	if err := f.db.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", grants[0]).Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := services.Authorizer.Reload(f.ctx); err != nil {
		t.Fatal(err)
	}
	terminalSessionResponse(t, h, f.ctx, http.MethodGet, path, h.tokens[actor], nil, http.StatusForbidden)
	t.Log("real login and Copilot status read preserve exact project authority, anonymous/foreign/revoked denials and requester separation; local transport/database substitutes only")
}
