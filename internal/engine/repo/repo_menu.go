// Copyright 2025 Arcentra Authors.
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
	"context"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// IMenuRepository defines menu persistence with context support.
type IMenuRepository interface {
	Get(ctx context.Context, menuId string) (*model.Menu, error)
	BatchGet(ctx context.Context, menuIds []string) ([]model.Menu, error)
	List(ctx context.Context) ([]model.Menu, error)
	ListByParent(ctx context.Context, parentId string) ([]model.Menu, error)
}

type MenuRepo struct {
	database.IDatabase
}

func NewMenuRepo(db database.IDatabase) IMenuRepository {
	return &MenuRepo{
		IDatabase: db,
	}
}

// Get returns menu by menuId.
func (r *MenuRepo) Get(ctx context.Context, menuId string) (*model.Menu, error) {
	var menu model.Menu
	err := r.Database().WithContext(ctx).Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("menu_id = ? AND is_enabled = ?", menuId, model.MenuEnabled).First(&menu).Error
	if err != nil {
		return nil, err
	}
	return &menu, nil
}

// BatchGet returns menus by menuIds.
func (r *MenuRepo) BatchGet(ctx context.Context, menuIds []string) ([]model.Menu, error) {
	if len(menuIds) == 0 {
		return []model.Menu{}, nil
	}
	var menus []model.Menu
	err := r.Database().WithContext(ctx).Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("menu_id IN ? AND is_enabled = ?", menuIds, model.MenuEnabled).
		Order("`order` ASC").Find(&menus).Error
	return menus, err
}

// List returns all enabled menus.
func (r *MenuRepo) List(ctx context.Context) ([]model.Menu, error) {
	var menus []model.Menu
	err := r.Database().WithContext(ctx).Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("is_enabled = ?", model.MenuEnabled).
		Order("`order` ASC").Find(&menus).Error
	return menus, err
}

// ListByParent returns child menus by parentId.
func (r *MenuRepo) ListByParent(ctx context.Context, parentId string) ([]model.Menu, error) {
	var menus []model.Menu
	err := r.Database().WithContext(ctx).Select("id", "menu_id", "parent_id", "name", "path", "component", "icon", "order", "is_visible", "is_enabled", "description", "meta", "created_at", "updated_at").
		Where("parent_id = ? AND is_enabled = ?", parentId, model.MenuEnabled).
		Order("`order` ASC").Find(&menus).Error
	return menus, err
}
