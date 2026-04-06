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

-- name: RoleCreate :exec
-- RoleCreate 创建角色
INSERT INTO t_role (role_id, name, display_name, description, is_enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: RoleGetByRoleId :one
-- RoleGetByRoleId 按 role_id 获取角色
SELECT id, role_id, name, display_name, description, is_enabled, created_at, updated_at FROM t_role WHERE role_id = ? LIMIT 1;

-- name: RoleBatchGetByRoleIds :many
-- RoleBatchGetByRoleIds 按 role_id 列表批量获取
SELECT id, role_id, name, display_name, description, is_enabled, created_at, updated_at FROM t_role WHERE role_id IN (?);

-- name: RoleDeleteByRoleId :exec
-- RoleDeleteByRoleId 按 role_id 硬删除角色
DELETE FROM t_role WHERE role_id = ?;

-- name: RoleCount :one
-- RoleCount 角色总数
SELECT COUNT(*) FROM t_role;

-- name: RoleList :many
-- RoleList 角色列表分页
SELECT id, role_id, name, display_name, description, is_enabled, created_at, updated_at FROM t_role ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: RoleUpdateByRoleId :exec
-- RoleUpdateByRoleId 按 role_id 更新（动态字段）
UPDATE t_role SET name = COALESCE(?, name), display_name = COALESCE(?, display_name), description = COALESCE(?, description), is_enabled = COALESCE(?, is_enabled), updated_at = ? WHERE role_id = ?;

-- name: RoleSoftDeleteByRoleId :exec
-- RoleSoftDeleteByRoleId 软删除角色（is_enabled=0）
UPDATE t_role SET is_enabled = 0, updated_at = ? WHERE role_id = ?;
