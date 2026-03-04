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

-- name: UserExtCreate :exec
-- UserExtCreate 创建用户扩展
INSERT INTO t_user_ext (user_id, timezone, last_login_at, invitation_status, invited_by, invited_at, accepted_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UserExtGetByUserId :one
-- UserExtGetByUserId 按 user_id 获取用户扩展
SELECT id, user_id, timezone, last_login_at, invitation_status, invited_by, invited_at, accepted_at, created_at, updated_at FROM t_user_ext WHERE user_id = ? LIMIT 1;

-- name: UserExtUpdateByUserId :exec
-- UserExtUpdateByUserId 按 user_id 更新用户扩展
UPDATE t_user_ext SET timezone = ?, last_login_at = ?, invitation_status = ?, invited_by = ?, invited_at = ?, accepted_at = ?, updated_at = ? WHERE user_id = ?;

-- name: UserExtUpdateLastLogin :exec
-- UserExtUpdateLastLogin 更新最后登录时间
UPDATE t_user_ext SET last_login_at = ?, updated_at = ? WHERE user_id = ?;

-- name: UserExtUpdateTimezone :exec
-- UserExtUpdateTimezone 更新时区
UPDATE t_user_ext SET timezone = ?, updated_at = ? WHERE user_id = ?;

-- name: UserExtUpdateInvitationStatus :exec
-- UserExtUpdateInvitationStatus 更新邀请状态（accepted 时写 accepted_at）
UPDATE t_user_ext SET invitation_status = ?, accepted_at = CASE WHEN ? = 'accepted' THEN ? ELSE accepted_at END, updated_at = ? WHERE user_id = ?;

-- name: UserExtDeleteByUserId :exec
-- UserExtDeleteByUserId 按 user_id 删除用户扩展
DELETE FROM t_user_ext WHERE user_id = ?;

-- name: UserExtExistsByUserId :one
-- UserExtExistsByUserId 判断用户扩展是否存在
SELECT COUNT(*) > 0 FROM t_user_ext WHERE user_id = ?;
