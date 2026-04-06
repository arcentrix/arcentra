-- Copyright 2026 Arcentra Authors.
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--      http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- name: MenuGetByMenuId :one
-- MenuGetByMenuId 按 menu_id 获取菜单（仅启用）
SELECT id, menu_id, parent_id, name, path, component, icon, `order`, is_visible, is_enabled, description, meta, created_at, updated_at FROM t_menu WHERE menu_id = ? AND is_enabled = 1 LIMIT 1;

-- name: MenuBatchGetByMenuIds :many
-- MenuBatchGetByMenuIds 按 menu_id 列表批量获取（仅启用）
SELECT id, menu_id, parent_id, name, path, component, icon, `order`, is_visible, is_enabled, description, meta, created_at, updated_at FROM t_menu WHERE menu_id IN (?) AND is_enabled = 1 ORDER BY `order` ASC;

-- name: MenuList :many
-- MenuList 列出所有启用菜单
SELECT id, menu_id, parent_id, name, path, component, icon, `order`, is_visible, is_enabled, description, meta, created_at, updated_at FROM t_menu WHERE is_enabled = 1 ORDER BY `order` ASC;

-- name: MenuListByParentId :many
-- MenuListByParentId 按 parent_id 列出子菜单（仅启用）
SELECT id, menu_id, parent_id, name, path, component, icon, `order`, is_visible, is_enabled, description, meta, created_at, updated_at FROM t_menu WHERE parent_id = ? AND is_enabled = 1 ORDER BY `order` ASC;
