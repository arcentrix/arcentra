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

-- name: UserCreate :exec
-- UserCreate 创建用户（AddUserReq -> t_user）
INSERT INTO t_user (user_id, username, full_name, avatar, email, phone, password, is_enabled, is_superadmin, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UserGetByUserId :one
-- UserGetByUserId 按 user_id 获取用户
SELECT * FROM t_user WHERE user_id = ? LIMIT 1;

-- name: UserGetUserIdByUsername :one
-- UserGetUserIdByUsername 按 username 获取 user_id
SELECT user_id FROM t_user WHERE username = ? LIMIT 1;

-- name: UserLogin :one
-- UserLogin 登录（username 或 email + is_enabled=1）
SELECT user_id, username, full_name, avatar, email, phone, password FROM t_user WHERE (username = ? OR email = ?) AND is_enabled = 1 LIMIT 1;

-- name: UserUpdateByUserId :exec
-- UserUpdateByUserId 按 user_id 更新（动态字段，不含 user_id/username/created_at）
UPDATE t_user SET full_name = ?, avatar = ?, email = ?, phone = ?, is_enabled = ?, is_superadmin = ?, updated_at = ? WHERE user_id = ?;

-- name: UserFetchInfoByUserId :one
-- UserFetchInfoByUserId 获取用户基本信息（不含 password）
SELECT user_id, username, full_name, avatar, email, phone FROM t_user WHERE user_id = ? LIMIT 1;

-- name: UserCount :one
-- UserCount 用户总数
SELECT COUNT(*) FROM t_user;

-- name: UserListWithExtAndRole :many
-- UserListWithExtAndRole 用户列表（含扩展表与角色名，分页）
SELECT u.user_id, u.username, u.full_name, u.avatar, u.email, u.phone, u.is_enabled, u.is_superadmin, ue.last_login_at, COALESCE(ue.invitation_status, 'accepted') AS invitation_status, role.role_name
FROM t_user u
LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id
LEFT JOIN (
  SELECT user_id, name AS role_name FROM (
    SELECT urb.user_id, r.name, ROW_NUMBER() OVER (PARTITION BY urb.user_id ORDER BY urb.create_time ASC) rn
    FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1
  ) t WHERE rn = 1
) role ON role.user_id = u.user_id
ORDER BY u.created_at DESC LIMIT ? OFFSET ?;

-- name: UserListWithExtAndRoleByRoleId :many
-- UserListWithExtAndRoleByRoleId 按 role_id 过滤的用户列表（含扩展与角色名）
SELECT u.user_id, u.username, u.full_name, u.avatar, u.email, u.phone, u.is_enabled, u.is_superadmin, ue.last_login_at, COALESCE(ue.invitation_status, 'accepted') AS invitation_status, role.role_name
FROM t_user u
LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id
INNER JOIN (SELECT DISTINCT urb.user_id, r.name AS role_name FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1 AND urb.role_id = ?) role ON role.user_id = u.user_id
ORDER BY u.created_at DESC LIMIT ? OFFSET ?;

-- name: UserListWithExtAndRoleByRoleName :many
-- UserListWithExtAndRoleByRoleName 按角色名过滤的用户列表（含扩展与角色名）
SELECT u.user_id, u.username, u.full_name, u.avatar, u.email, u.phone, u.is_enabled, u.is_superadmin, ue.last_login_at, COALESCE(ue.invitation_status, 'accepted') AS invitation_status, role.role_name
FROM t_user u
LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id
INNER JOIN (
  SELECT user_id, name AS role_name FROM (
    SELECT urb.user_id, r.name, ROW_NUMBER() OVER (PARTITION BY urb.user_id ORDER BY urb.create_time ASC) rn
    FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1 AND r.name = ?
  ) t WHERE rn = 1
) role ON role.user_id = u.user_id
ORDER BY u.created_at DESC LIMIT ? OFFSET ?;

-- name: UserListWithExtAndRoleCount :one
-- UserListWithExtAndRoleCount 用户列表总数（无角色过滤，与 UserListWithExtAndRole 对应）
SELECT COUNT(*) FROM t_user u
LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id
LEFT JOIN (
  SELECT user_id FROM (SELECT urb.user_id, ROW_NUMBER() OVER (PARTITION BY urb.user_id ORDER BY urb.create_time ASC) rn FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1) t WHERE rn = 1
) role ON role.user_id = u.user_id;

-- name: UserListWithExtAndRoleByRoleIdCount :one
-- UserListWithExtAndRoleByRoleIdCount 按 role_id 过滤的用户总数
SELECT COUNT(*) FROM t_user u
LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id
INNER JOIN (SELECT DISTINCT urb.user_id FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1 AND urb.role_id = ?) role ON role.user_id = u.user_id;

-- name: UserListWithExtAndRoleByRoleNameCount :one
-- UserListWithExtAndRoleByRoleNameCount 按角色名过滤的用户总数
SELECT COUNT(*) FROM t_user u
LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id
INNER JOIN (SELECT user_id FROM (SELECT urb.user_id, ROW_NUMBER() OVER (PARTITION BY urb.user_id ORDER BY urb.create_time ASC) rn FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1 AND r.name = ?) t WHERE rn = 1) role ON role.user_id = u.user_id;
