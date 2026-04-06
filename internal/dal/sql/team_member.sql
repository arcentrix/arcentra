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

-- name: TeamMemberGet :one
-- TeamMemberGet 按 team_id 与 user_id 获取团队成员
SELECT id, team_id, user_id, role, created_at, updated_at FROM t_team_member WHERE team_id = ? AND user_id = ? LIMIT 1;

-- name: TeamMemberListByTeamId :many
-- TeamMemberListByTeamId 按 team_id 列出成员
SELECT id, team_id, user_id, role, created_at, updated_at FROM t_team_member WHERE team_id = ?;

-- name: TeamMemberListByUserId :many
-- TeamMemberListByUserId 按 user_id 列出其团队成员关系
SELECT id, team_id, user_id, role, created_at, updated_at FROM t_team_member WHERE user_id = ?;

-- name: TeamMemberAdd :exec
-- TeamMemberAdd 添加团队成员
INSERT INTO t_team_member (team_id, user_id, role, created_at, updated_at)
VALUES (?, ?, ?, ?, ?);

-- name: TeamMemberUpdateRole :exec
-- TeamMemberUpdateRole 更新团队成员角色
UPDATE t_team_member SET role = ?, updated_at = ? WHERE team_id = ? AND user_id = ?;

-- name: TeamMemberRemove :exec
-- TeamMemberRemove 移除团队成员
DELETE FROM t_team_member WHERE team_id = ? AND user_id = ?;
