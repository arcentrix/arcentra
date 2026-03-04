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

-- name: UserRoleBindingListByUserId :many
-- UserRoleBindingListByUserId 按 user_id 列出用户角色绑定
SELECT binding_id, user_id, role_id, granted_by, create_time, update_time FROM t_user_role_binding WHERE user_id = ?;

-- name: UserRoleBindingGetByUserIdAndRoleId :one
-- UserRoleBindingGetByUserIdAndRoleId 按 user_id 与 role_id 获取绑定
SELECT binding_id, user_id, role_id, granted_by, create_time, update_time FROM t_user_role_binding WHERE user_id = ? AND role_id = ? LIMIT 1;

-- name: UserRoleBindingCreate :exec
-- UserRoleBindingCreate 创建用户角色绑定
INSERT INTO t_user_role_binding (binding_id, user_id, role_id, granted_by, create_time, update_time)
VALUES (?, ?, ?, ?, ?, ?);

-- name: UserRoleBindingDeleteByBindingId :exec
-- UserRoleBindingDeleteByBindingId 按 binding_id 删除
DELETE FROM t_user_role_binding WHERE binding_id = ?;

-- name: UserRoleBindingDeleteByUserId :exec
-- UserRoleBindingDeleteByUserId 按 user_id 删除该用户全部绑定
DELETE FROM t_user_role_binding WHERE user_id = ?;
