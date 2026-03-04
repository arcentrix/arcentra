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

-- name: NotificationTemplateCreate :exec
-- NotificationTemplateCreate 创建通知模板
INSERT INTO t_notification_templates (template_id, name, type, channel, content, is_active, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: NotificationTemplateGetByTemplateId :one
-- NotificationTemplateGetByTemplateId 按 template_id 获取（仅 is_active=1）
SELECT * FROM t_notification_templates WHERE template_id = ? AND is_active = 1 LIMIT 1;

-- name: NotificationTemplateGetByNameAndType :one
-- NotificationTemplateGetByNameAndType 按 name 与 type 获取（仅 is_active=1）
SELECT * FROM t_notification_templates WHERE name = ? AND type = ? AND is_active = 1 LIMIT 1;

-- name: NotificationTemplateList :many
-- NotificationTemplateList 模板列表（可选 type, channel, name LIKE, limit, offset）
SELECT * FROM t_notification_templates
WHERE is_active = 1 AND (? = '' OR type = ?) AND (? = '' OR channel = ?) AND (? = '' OR name LIKE ?)
ORDER BY id LIMIT ? OFFSET ?;

-- name: NotificationTemplateUpdateByTemplateId :exec
-- NotificationTemplateUpdateByTemplateId 按 template_id 更新（不含 id, template_id, created_at）
UPDATE t_notification_templates SET name = ?, type = ?, channel = ?, content = ?, is_active = ?, updated_at = ? WHERE template_id = ?;

-- name: NotificationTemplateSoftDeleteByTemplateId :exec
-- NotificationTemplateSoftDeleteByTemplateId 软删除模板（is_active=0）
UPDATE t_notification_templates SET is_active = 0, updated_at = ? WHERE template_id = ?;
