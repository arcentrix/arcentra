// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package authz

import (
	"context"
	"errors"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

// NewLiveAuthorizer 从数据库创建独立权限快照，过滤失效角色和过期授权且不复用缓存。
func NewLiveAuthorizer(ctx context.Context, repositories *repo.Repositories) (IAuthorizer, error) {
	if repositories == nil || repositories.Permission == nil || repositories.RoleGrant == nil || repositories.Role == nil {
		return nil, errors.New("live authorization repositories unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := *repositories
	snapshot.Permission = &livePermissions{IPermissionRepository: repositories.Permission, roles: repositories.Role}
	snapshot.RoleGrant = &liveGrants{IRoleGrantRepository: repositories.RoleGrant}
	return newAuthorizer(ctx, &snapshot, nil)
}

type liveGrants struct {
	repo.IRoleGrantRepository
}

func (r *liveGrants) ListAllEnabled(ctx context.Context) ([]model.RoleGrant, error) {
	grants, err := r.IRoleGrantRepository.ListAllEnabled(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	result := make([]model.RoleGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.IsEnabled == 1 && (grant.ExpiresAt == nil || grant.ExpiresAt.After(now)) {
			result = append(result, grant)
		}
	}
	return result, nil
}

type livePermissions struct {
	repo.IPermissionRepository
	roles repo.IRoleRepository
}

func (r *livePermissions) ListAllRoleBindings(ctx context.Context) ([]model.RolePermissionBinding, error) {
	bindings, err := r.IPermissionRepository.ListAllRoleBindings(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(bindings))
	seen := map[string]bool{}
	for _, binding := range bindings {
		if binding.RoleID != "" && !seen[binding.RoleID] {
			ids = append(ids, binding.RoleID)
			seen[binding.RoleID] = true
		}
	}
	if len(ids) == 0 {
		return []model.RolePermissionBinding{}, nil
	}
	roles, err := r.roles.BatchGet(ctx, ids)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, role := range roles {
		if role.IsEnabled == 1 {
			enabled[role.RoleID] = true
		}
	}
	result := make([]model.RolePermissionBinding, 0, len(bindings))
	for _, binding := range bindings {
		if enabled[binding.RoleID] {
			result = append(result, binding)
		}
	}
	return result, nil
}
