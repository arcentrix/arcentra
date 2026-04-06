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

-- name: IdentityGetByName :one
-- IdentityGetByName 按 name 获取身份提供商
SELECT id, provider_id, provider_type, name, description, config, priority, is_enabled, created_at, updated_at FROM t_identity WHERE name = ? LIMIT 1;

-- name: IdentityListByProviderType :many
-- IdentityListByProviderType 按 provider_type 列出
SELECT id, provider_id, provider_type, name, description, config, priority, is_enabled, created_at, updated_at FROM t_identity WHERE provider_type = ? ORDER BY priority ASC;

-- name: IdentityListAll :many
-- IdentityListAll 列出所有身份提供商
SELECT id, provider_id, provider_type, name, description, config, priority, is_enabled, created_at, updated_at FROM t_identity ORDER BY priority ASC;

-- name: IdentityGetProviderTypes :many
-- IdentityGetProviderTypes 获取所有不重复的 provider_type
SELECT DISTINCT provider_type FROM t_identity;

-- name: IdentityCreate :exec
-- IdentityCreate 创建身份提供商
INSERT INTO t_identity (provider_id, provider_type, name, description, config, priority, is_enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: IdentityUpdateByName :exec
-- IdentityUpdateByName 按 name 更新（不含 name, provider_id, provider_type, created_at）
UPDATE t_identity SET description = ?, config = ?, priority = ?, is_enabled = ?, updated_at = ? WHERE name = ?;

-- name: IdentityDeleteByName :exec
-- IdentityDeleteByName 按 name 删除
DELETE FROM t_identity WHERE name = ?;

-- name: IdentityExistsByName :one
-- IdentityExistsByName 判断 name 是否存在
SELECT COUNT(*) > 0 FROM t_identity WHERE name = ?;

-- name: IdentityGetIsEnabledByName :one
-- IdentityGetIsEnabledByName 获取当前 is_enabled 用于切换
SELECT is_enabled FROM t_identity WHERE name = ? LIMIT 1;

-- name: IdentityToggleByName :exec
-- IdentityToggleByName 切换 is_enabled（0/1）
UPDATE t_identity SET is_enabled = ?, updated_at = ? WHERE name = ?;