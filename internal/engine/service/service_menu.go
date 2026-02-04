// Copyright 2025 Arcade Team
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
	"github.com/arcentrix/arcade/internal/engine/model"
	userrepo "github.com/arcentrix/arcade/internal/engine/repo"
	"github.com/arcentrix/arcade/pkg/log"
)

// MenuService 菜单服务
type MenuService struct {
	menuRepo userrepo.IMenuRepository
}

func NewMenuService(menuRepo userrepo.IMenuRepository) *MenuService {
	return &MenuService{
		menuRepo: menuRepo,
	}
}

// BuildMenuTree 构建菜单树
func (s *MenuService) BuildMenuTree(menus []model.Menu) []model.MenuDTO {
	// 创建菜单映射
	menuMap := make(map[string]*model.MenuDTO)
	var rootMenus []model.MenuDTO

	// 第一遍：创建所有菜单节点
	for _, menu := range menus {
		if menu.IsEnabled != model.MenuEnabled {
			continue
		}
		menuDTO := &model.MenuDTO{
			MenuId:      menu.MenuId,
			ParentId:    menu.ParentId,
			Name:        menu.Name,
			Path:        menu.Path,
			Component:   menu.Component,
			Icon:        menu.Icon,
			Order:       menu.Order,
			IsVisible:   menu.IsVisible == model.MenuVisible,
			IsEnabled:   menu.IsEnabled == model.MenuEnabled,
			Description: menu.Description,
			Children:    []model.MenuDTO{},
		}
		menuMap[menu.MenuId] = menuDTO
	}

	// 第二遍：构建父子关系
	for menuId, menuDTO := range menuMap {
		if menuDTO.ParentId == "" {
			// 根菜单
			rootMenus = append(rootMenus, *menuDTO)
		} else {
			// 子菜单，添加到父菜单的children中
			if parent, exists := menuMap[menuDTO.ParentId]; exists {
				parent.Children = append(parent.Children, *menuDTO)
			} else {
				// 父菜单不存在，当作根菜单处理
				log.Warnw("parent menu not found", "menuId", menuId, "parentId", menuDTO.ParentId)
				rootMenus = append(rootMenus, *menuDTO)
			}
		}
	}

	return rootMenus
}

// ExtractRoutes 从菜单树中提取所有路由路径
func (s *MenuService) ExtractRoutes(menus []model.MenuDTO) []string {
	var routes []string
	for _, menu := range menus {
		if menu.Path != "" {
			routes = append(routes, menu.Path)
		}
		if len(menu.Children) > 0 {
			childRoutes := s.ExtractRoutes(menu.Children)
			routes = append(routes, childRoutes...)
		}
	}
	return routes
}
