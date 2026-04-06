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

-- name: NotificationChannelCreate :exec
-- NotificationChannelCreate 创建通知渠道
INSERT INTO t_notification_channels (channel_id, name, type, config, is_active, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: NotificationChannelGetByChannelId :one
-- NotificationChannelGetByChannelId 按 channel_id 获取
SELECT * FROM t_notification_channels WHERE channel_id = ? LIMIT 1;

-- name: NotificationChannelGetByName :one
-- NotificationChannelGetByName 按 name 获取
SELECT * FROM t_notification_channels WHERE name = ? LIMIT 1;

-- name: NotificationChannelList :many
-- NotificationChannelList 列出所有渠道
SELECT * FROM t_notification_channels;

-- name: NotificationChannelListActive :many
-- NotificationChannelListActive 列出启用渠道
SELECT * FROM t_notification_channels WHERE is_active = 1;

-- name: NotificationChannelUpdateByChannelId :exec
-- NotificationChannelUpdateByChannelId 按 channel_id 更新（不含 id, channel_id, created_at）
UPDATE t_notification_channels SET name = ?, type = ?, config = ?, is_active = ?, updated_at = ? WHERE channel_id = ?;

-- name: NotificationChannelSoftDeleteByChannelId :exec
-- NotificationChannelSoftDeleteByChannelId 软删除渠道（is_active=0）
UPDATE t_notification_channels SET is_active = 0, updated_at = ? WHERE channel_id = ?;
