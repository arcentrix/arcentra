// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package authz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/stretchr/testify/require"
)

// TestAuthorizerReloadRebuildsRoleLinks 验证重新加载权限时删除已撤销或已迁移的角色关系
func TestAuthorizerReloadRebuildsRoleLinks(t *testing.T) {
	for _, mutation := range []string{"disabled", "deleted", "subject-moved", "scope-moved"} {
		t.Run(mutation, func(t *testing.T) {
			f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "project-read", ResourceType: "project", Action: "read"})
			require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Updates(map[string]any{"scope_type": "project", "scope_id": "project-a"}).Error)
			ctx := context.Background()
			authorizer := f.authorizer(t, nil)
			original := ResourceRef{Type: "project", ID: "project-a", ProjectID: "project-a"}
			allowed, err := authorizer.Check(ctx, f.subject, "project:read", original)
			require.NoError(t, err)
			require.True(t, allowed)
			switch mutation {
			case "disabled":
				require.NoError(t, f.repos.RoleGrant.DisableGrant(ctx, "grant-1"))
			case "deleted":
				require.NoError(t, f.db.Where("grant_id = ?", "grant-1").Delete(&model.RoleGrant{}).Error)
			case "subject-moved":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Update("subject_id", "user-2").Error)
			case "scope-moved":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Update("scope_id", "project-b").Error)
			}
			// Repeated reloads must neither restore old authority nor discard the
			// current binding. Exercise the same published authorizer instance.
			for i := 0; i < 2; i++ {
				require.NoError(t, authorizer.Reload(ctx))
				allowed, err = authorizer.Check(ctx, f.subject, "project:read", original)
				require.NoError(t, err)
				require.False(t, allowed, "reloaded policy retained a revoked role-manager link")
				if mutation == "subject-moved" {
					allowed, err = authorizer.Check(ctx, Subject{UserID: "user-2"}, "project:read", original)
					require.NoError(t, err)
					require.True(t, allowed)
				}
				if mutation == "scope-moved" {
					allowed, err = authorizer.Check(ctx, f.subject, "project:read", ResourceRef{Type: "project", ID: "project-b", ProjectID: "project-b"})
					require.NoError(t, err)
					require.True(t, allowed)
				}
			}
		})
	}
}

// TestAuthorizerReloadDropsInheritedTeamGrant 验证团队角色继承不会保留撤销的授权关系
func TestAuthorizerReloadDropsInheritedTeamGrant(t *testing.T) {
	f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "project-read", ResourceType: "project", Action: "read"})
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Updates(map[string]any{"subject_type": "team", "subject_id": "team-a", "scope_type": "project", "scope_id": "project-a"}).Error)
	ctx := context.Background()
	authorizer := f.authorizer(t, nil)
	member := Subject{UserID: f.subject.UserID, TeamIDs: []string{"team-a"}}
	scope := ResourceRef{Type: "project", ID: "project-a", ProjectID: "project-a"}
	allowed, err := authorizer.Check(ctx, member, "project:read", scope)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, f.repos.RoleGrant.DisableGrant(ctx, "grant-1"))
	require.NoError(t, authorizer.Reload(ctx))
	allowed, err = authorizer.Check(ctx, member, "project:read", scope)
	require.NoError(t, err)
	require.False(t, allowed)
}

// TestLiveAuthorizerReloadRetainsRoleAndExpiryFilters 验证重载仍保留现有实时角色及到期过滤
func TestLiveAuthorizerReloadRetainsRoleAndExpiryFilters(t *testing.T) {
	for _, mutation := range []string{"disabled-role", "expired-grant"} {
		t.Run(mutation, func(t *testing.T) {
			f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "project-read", ResourceType: "project", Action: "read"})
			ctx := context.Background()
			authorizer, err := NewLiveAuthorizer(ctx, f.repos)
			require.NoError(t, err)
			allowed, err := authorizer.Check(ctx, f.subject, "project:read", ResourceRef{})
			require.NoError(t, err)
			require.True(t, allowed)
			if mutation == "disabled-role" {
				require.NoError(t, f.db.Model(&model.Role{}).Where("role_id = ?", "role-1").Update("is_enabled", 0).Error)
			} else {
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Update("expires_at", time.Now().Add(-time.Minute)).Error)
			}
			require.NoError(t, authorizer.Reload(ctx))
			allowed, err = authorizer.Check(ctx, f.subject, "project:read", ResourceRef{})
			require.NoError(t, err)
			require.False(t, allowed)
		})
	}
}

type reloadPermissions struct {
	repo.IPermissionRepository
	fail    string
	started chan struct{}
	release chan struct{}
}

// ListPermissions 保留实际仓储读取并提供有界错误及阻塞测试点
func (r *reloadPermissions) ListPermissions(ctx context.Context) ([]model.Permission, error) {
	if r.started != nil {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.fail == "permissions" {
		return nil, errors.New("synthetic permission read failure")
	}
	return r.IPermissionRepository.ListPermissions(ctx)
}

// ListAllRoleBindings 在策略绑定查询边界注入可识别错误
func (r *reloadPermissions) ListAllRoleBindings(ctx context.Context) ([]model.RolePermissionBinding, error) {
	if r.fail == "bindings" {
		return nil, errors.New("synthetic binding read failure")
	}
	return r.IPermissionRepository.ListAllRoleBindings(ctx)
}

type reloadGrants struct {
	repo.IRoleGrantRepository
}

// ListAllEnabled 在角色授权查询边界注入可识别错误
func (*reloadGrants) ListAllEnabled(context.Context) ([]model.RoleGrant, error) {
	return nil, errors.New("synthetic grant read failure")
}

// TestAuthorizerReloadReadErrorFailsClosed 验证任何权限快照读取失败均不保留旧授权
func TestAuthorizerReloadReadErrorFailsClosed(t *testing.T) {
	for _, failure := range []string{"permissions", "bindings", "grants"} {
		t.Run(failure, func(t *testing.T) {
			f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "project-read", ResourceType: "project", Action: "read"})
			ctx := context.Background()
			authorizer := f.authorizer(t, nil).(*Authorizer)
			snapshot := *f.repos
			snapshot.Permission = &reloadPermissions{IPermissionRepository: f.repos.Permission, fail: failure}
			if failure == "grants" {
				snapshot.RoleGrant = &reloadGrants{IRoleGrantRepository: f.repos.RoleGrant}
			}
			authorizer.repos = &snapshot
			require.Error(t, authorizer.Reload(ctx))
			allowed, err := authorizer.Check(ctx, f.subject, "project:read", ResourceRef{})
			require.NoError(t, err)
			require.False(t, allowed)
			// Recovery is explicit and rebuilds the same object's current policy.
			authorizer.repos = f.repos
			require.NoError(t, authorizer.Reload(ctx))
			allowed, err = authorizer.Check(ctx, f.subject, "project:read", ResourceRef{})
			require.NoError(t, err)
			require.True(t, allowed)
		})
	}
}

// TestAuthorizerReloadSerializesConcurrentCheck 验证读取中的撤销重载不会暴露瞬时旧授权
func TestAuthorizerReloadSerializesConcurrentCheck(t *testing.T) {
	f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "project-read", ResourceType: "project", Action: "read"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authorizer := f.authorizer(t, nil).(*Authorizer)
	require.NoError(t, f.repos.RoleGrant.DisableGrant(ctx, "grant-1"))
	snapshot := *f.repos
	blocked := &reloadPermissions{IPermissionRepository: f.repos.Permission, started: make(chan struct{}), release: make(chan struct{})}
	snapshot.Permission = blocked
	authorizer.repos = &snapshot
	reloaded := make(chan error, 1)
	safe.Go(func() { reloaded <- authorizer.Reload(ctx) })
	select {
	case <-blocked.started:
	case <-ctx.Done():
		t.Fatal("reload did not reach its bounded read barrier")
	}
	// The barrier is inside the repository read, so TryRLock deterministically
	// proves the entire refresh is protected, not just the final policy writes.
	if authorizer.mu.TryRLock() {
		authorizer.mu.RUnlock()
		close(blocked.release)
		t.Fatal("permission checks can observe stale policy during reload")
	}
	type checkResult struct {
		allowed bool
		err     error
	}
	checked := make(chan checkResult, 1)
	safe.Go(func() {
		allowed, err := authorizer.Check(ctx, f.subject, "project:read", ResourceRef{})
		checked <- checkResult{allowed, err}
	})
	close(blocked.release)
	select {
	case err := <-reloaded:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("reload did not finish within its original deadline")
	}
	select {
	case result := <-checked:
		require.NoError(t, result.err)
		require.False(t, result.allowed)
	case <-ctx.Done():
		t.Fatal("concurrent permission check did not finish")
	}
}
