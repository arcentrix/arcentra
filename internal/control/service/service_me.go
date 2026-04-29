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

package service

import (
	"context"
	"sort"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

const wildcardActionID = "*:*"

// MeService 提供当前用户权限与路由信息查询
type MeService struct {
	menuRepo   repo.IMenuRepository
	teamRepo   repo.ITeamMemberRepository
	menuSvc    *MenuService
	authorizer authz.IAuthorizer
}

// NewMeService 创建当前用户服务
func NewMeService(
	menuRepo repo.IMenuRepository,
	teamRepo repo.ITeamMemberRepository,
	menuSvc *MenuService,
	authorizer authz.IAuthorizer,
) *MeService {
	return &MeService{
		menuRepo:   menuRepo,
		teamRepo:   teamRepo,
		menuSvc:    menuSvc,
		authorizer: authorizer,
	}
}

// GetEffectiveActions 返回当前用户在指定作用域下的 action
func (s *MeService) GetEffectiveActions(ctx context.Context, userID, scopeType, scopeID string) ([]string, error) {
	subject, err := s.buildSubject(ctx, userID)
	if err != nil {
		return nil, err
	}
	if s.authorizer == nil {
		return []string{}, nil
	}
	return s.authorizer.EffectiveActions(ctx, subject, scopeType, scopeID)
}

// GetMenus 返回过滤后的菜单树（包含隐藏项，由前端按 isVisible 自行决定是否在导航中显示）
func (s *MeService) GetMenus(ctx context.Context, userID, scopeType, scopeID string) ([]model.MenuDTO, error) {
	menus, err := s.filterMenus(ctx, userID, scopeType, scopeID, false)
	if err != nil {
		return nil, err
	}
	return s.menuSvc.BuildMenuTree(menus), nil
}

// GetRoutes 返回过滤后的路由路径列表
func (s *MeService) GetRoutes(ctx context.Context, userID, scopeType, scopeID string) ([]string, error) {
	menus, err := s.filterMenus(ctx, userID, scopeType, scopeID, false)
	if err != nil {
		return nil, err
	}
	dtos := s.menuSvc.BuildMenuTree(menus)
	routes := s.menuSvc.ExtractRoutes(dtos)
	sort.Strings(routes)
	return routes, nil
}

func (s *MeService) filterMenus(
	ctx context.Context,
	userID, scopeType, scopeID string,
	visibleOnly bool,
) ([]model.Menu, error) {
	allMenus, err := s.menuRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	effectiveActions, err := s.GetEffectiveActions(ctx, userID, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	actionSet := make(map[string]struct{}, len(effectiveActions))
	for _, action := range effectiveActions {
		actionSet[action] = struct{}{}
	}

	result := make([]model.Menu, 0, len(allMenus))
	for _, menu := range allMenus {
		if menu.IsEnabled != model.MenuEnabled {
			continue
		}
		if visibleOnly && menu.IsVisible != model.MenuVisible {
			continue
		}
		if menu.PermissionID == "" {
			result = append(result, menu)
			continue
		}
		if _, ok := actionSet[wildcardActionID]; ok {
			result = append(result, menu)
			continue
		}
		if _, ok := actionSet[menu.PermissionID]; ok {
			result = append(result, menu)
		}
	}
	return result, nil
}

func (s *MeService) buildSubject(ctx context.Context, userID string) (authz.Subject, error) {
	subject := authz.Subject{UserID: userID}
	if s.teamRepo == nil {
		return subject, nil
	}
	teams, err := s.teamRepo.ListUserTeams(ctx, userID)
	if err != nil {
		return subject, err
	}
	teamIDs := make([]string, 0, len(teams))
	for _, team := range teams {
		if team.TeamID != "" {
			teamIDs = append(teamIDs, team.TeamID)
		}
	}
	subject.TeamIDs = teamIDs
	return subject, nil
}
