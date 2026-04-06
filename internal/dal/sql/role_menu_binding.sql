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

-- name: RoleMenuBindingListByRoleId :many
-- RoleMenuBindingListByRoleId 按 role_id 列出角色菜单绑定（is_accessible=1）
SELECT id, role_menu_id, role_id, menu_id, resource_id, is_visible, is_accessible, created_at, updated_at FROM t_role_menu_binding WHERE role_id = ? AND is_accessible = 1;

-- name: RoleMenuBindingListByRoleIdAndResourceId :many
-- RoleMenuBindingListByRoleIdAndResourceId 按 role_id 与 resource_id 列出（resource_id 空则 IS NULL OR = ''）
SELECT id, role_menu_id, role_id, menu_id, resource_id, is_visible, is_accessible, created_at, updated_at FROM t_role_menu_binding
WHERE role_id = ? AND is_accessible = 1 AND ((? = '') AND (resource_id IS NULL OR resource_id = '') OR resource_id = ?);

-- name: RoleMenuBindingListByRoleIdsAndResourceId :many
-- RoleMenuBindingListByRoleIdsAndResourceId 按 role_id 列表与 resource_id 列出
SELECT id, role_menu_id, role_id, menu_id, resource_id, is_visible, is_accessible, created_at, updated_at FROM t_role_menu_binding
WHERE role_id IN (?) AND is_accessible = 1 AND ((? = '') AND (resource_id IS NULL OR resource_id = '') OR resource_id = ?);

-- name: RoleMenuBindingCreate :exec
-- RoleMenuBindingCreate 创建角色菜单绑定
INSERT INTO t_role_menu_binding (role_menu_id, role_id, menu_id, resource_id, is_visible, is_accessible, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: RoleMenuBindingDeleteByRoleMenuId :exec
-- RoleMenuBindingDeleteByRoleMenuId 按 role_menu_id 删除
DELETE FROM t_role_menu_binding WHERE role_menu_id = ?;
