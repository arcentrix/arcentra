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

-- name: SecretCreate :exec
-- SecretCreate 创建密钥
INSERT INTO t_secret (secret_id, name, secret_type, secret_value, description, scope, scope_id, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SecretUpdateBySecretId :exec
-- SecretUpdateBySecretId 按 secret_id 更新密钥（不含 id, secret_id, created_at）
UPDATE t_secret SET name = ?, secret_type = ?, secret_value = ?, description = ?, scope = ?, scope_id = ?, created_by = ?, updated_at = ? WHERE secret_id = ?;

-- name: SecretGetBySecretId :one
-- SecretGetBySecretId 按 secret_id 获取密钥（含 secret_value）
SELECT id, secret_id, name, secret_type, secret_value, description, scope, scope_id, created_by, created_at, updated_at FROM t_secret WHERE secret_id = ? LIMIT 1;

-- name: SecretListCount :one
-- SecretListCount 密钥列表总数（可选：secret_type, scope, scope_id, created_by）
SELECT COUNT(*) FROM t_secret
WHERE (? = '' OR secret_type = ?) AND (? = '' OR scope = ?) AND (? = '' OR scope_id = ?) AND (? = '' OR created_by = ?);

-- name: SecretList :many
-- SecretList 密钥列表分页（不含 secret_value）
SELECT id, secret_id, name, secret_type, description, scope, scope_id, created_by
FROM t_secret
WHERE (? = '' OR secret_type = ?) AND (? = '' OR scope = ?) AND (? = '' OR scope_id = ?) AND (? = '' OR created_by = ?)
ORDER BY id DESC LIMIT ? OFFSET ?;

-- name: SecretListByScope :many
-- SecretListByScope 按 scope 与 scope_id 列出密钥（不含 value）
SELECT id, secret_id, name, secret_type, description, scope, scope_id, created_by FROM t_secret WHERE scope = ? AND scope_id = ?;

-- name: SecretGetValueBySecretId :one
-- SecretGetValueBySecretId 仅获取密钥值
SELECT secret_value FROM t_secret WHERE secret_id = ? LIMIT 1;

-- name: SecretDeleteBySecretId :exec
-- SecretDeleteBySecretId 按 secret_id 删除密钥
DELETE FROM t_secret WHERE secret_id = ?;
