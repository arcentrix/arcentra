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

-- name: StorageConfigGetDefault :one
-- StorageConfigGetDefault 获取默认存储配置（is_default=1 且 is_enabled=1）
SELECT id, storage_id, name, storage_type, config, description, is_default, is_enabled, created_at, updated_at FROM t_storage_config WHERE is_default = 1 AND is_enabled = 1 LIMIT 1;

-- name: StorageConfigGetByStorageId :one
-- StorageConfigGetByStorageId 按 storage_id 获取（仅启用）
SELECT id, storage_id, name, storage_type, config, description, is_default, is_enabled, created_at, updated_at FROM t_storage_config WHERE storage_id = ? AND is_enabled = 1 LIMIT 1;

-- name: StorageConfigListEnabled :many
-- StorageConfigListEnabled 列出所有启用配置
SELECT storage_id, name, storage_type, config, description, is_default, is_enabled FROM t_storage_config WHERE is_enabled = 1 ORDER BY is_default DESC, storage_id ASC;

-- name: StorageConfigListByType :many
-- StorageConfigListByType 按 storage_type 列出启用配置
SELECT storage_id, name, storage_type, config, description, is_default, is_enabled FROM t_storage_config WHERE storage_type = ? AND is_enabled = 1 ORDER BY is_default DESC, storage_id ASC;

-- name: StorageConfigCreate :exec
-- StorageConfigCreate 创建存储配置
INSERT INTO t_storage_config (storage_id, name, storage_type, config, description, is_default, is_enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: StorageConfigUpdateByStorageId :exec
-- StorageConfigUpdateByStorageId 按 storage_id 更新
UPDATE t_storage_config SET name = ?, storage_type = ?, config = ?, description = ?, is_default = ?, is_enabled = ?, updated_at = ? WHERE storage_id = ?;

-- name: StorageConfigDeleteByStorageId :exec
-- StorageConfigDeleteByStorageId 按 storage_id 删除
DELETE FROM t_storage_config WHERE storage_id = ?;

-- name: StorageConfigClearDefault :exec
-- StorageConfigClearDefault 清除当前默认（将所有 is_default=1 置为 0）
UPDATE t_storage_config SET is_default = 0, updated_at = ? WHERE is_default = 1;

-- name: StorageConfigSetDefault :exec
-- StorageConfigSetDefault 将指定 storage_id 设为默认
UPDATE t_storage_config SET is_default = 1, updated_at = ? WHERE storage_id = ?;
