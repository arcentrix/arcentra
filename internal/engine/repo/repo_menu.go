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

package repo

import (
	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/database"
)

type IMenuRepository interface {
	GetMenu(menuId string) (*model.Menu, error)
	GetMenusByMenuIds(menuIds []string) ([]model.Menu, error)
	GetAllMenus() ([]model.Menu, error)
	GetMenusByParentId(parentId string) ([]model.Menu, error)
}

type MenuRepo struct {
	database.IDatabase
}

func NewMenuRepo(db database.IDatabase) IMenuRepository {
	return &MenuRepo{
		IDatabase: db,
	}
}

// GetMenu 获取菜单
func (r *MenuRepo) GetMenu(menuId string) (*model.Menu, error) {
	var menu model.Menu
	err := r.Database().Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("menu_id = ? AND is_enabled = ?", menuId, model.MenuEnabled).First(&menu).Error
	if err != nil {
		return nil, err
	}
	return &menu, nil
}

// GetMenusByMenuIds 根据菜单ID列表获取菜单
func (r *MenuRepo) GetMenusByMenuIds(menuIds []string) ([]model.Menu, error) {
	if len(menuIds) == 0 {
		return []model.Menu{}, nil
	}
	var menus []model.Menu
	err := r.Database().Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("menu_id IN ? AND is_enabled = ?", menuIds, model.MenuEnabled).
		Order("`order` ASC").Find(&menus).Error
	return menus, err
}

// GetAllMenus 获取所有启用的菜单
func (r *MenuRepo) GetAllMenus() ([]model.Menu, error) {
	var menus []model.Menu
	err := r.Database().Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("is_enabled = ?", model.MenuEnabled).
		Order("`order` ASC").Find(&menus).Error
	return menus, err
}

// GetMenusByParentId 根据父菜单ID获取子菜单
func (r *MenuRepo) GetMenusByParentId(parentId string) ([]model.Menu, error) {
	var menus []model.Menu
	err := r.Database().Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("parent_id = ? AND is_enabled = ?", parentId, model.MenuEnabled).
		Order("`order` ASC").Find(&menus).Error
	return menus, err
}
