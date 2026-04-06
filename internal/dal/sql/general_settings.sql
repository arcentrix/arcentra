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

-- name: GeneralSettingsUpdateBySettingsId :exec
-- GeneralSettingsUpdateBySettingsId 按 settings_id 更新（不含 id, settings_id, category, name）
UPDATE t_general_settings SET display_name = ?, data = ?, `schema` = ?, description = ?, updated_at = ? WHERE settings_id = ?;

-- name: GeneralSettingsGetBySettingsId :one
-- GeneralSettingsGetBySettingsId 按 settings_id 仅取 name, category（用于再查 GetByName）
SELECT name, category FROM t_general_settings WHERE settings_id = ? LIMIT 1;

-- name: GeneralSettingsGetByCategoryAndName :one
-- GeneralSettingsGetByCategoryAndName 按 category 与 name 获取完整配置
SELECT id, settings_id, category, name, display_name, data, `schema`, description, created_at, updated_at FROM t_general_settings WHERE category = ? AND name = ? LIMIT 1;

-- name: GeneralSettingsListCount :one
-- GeneralSettingsListCount 配置列表总数（可选 category）
SELECT COUNT(*) FROM t_general_settings WHERE (? = '' OR category = ?);

-- name: GeneralSettingsList :many
-- GeneralSettingsList 配置列表分页
SELECT id, settings_id, category, name, display_name, data, `schema`, description
FROM t_general_settings WHERE (? = '' OR category = ?) ORDER BY id DESC LIMIT ? OFFSET ?;

-- name: GeneralSettingsGetCategories :many
-- GeneralSettingsGetCategories 获取所有不重复的 category
SELECT DISTINCT category FROM t_general_settings;
