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

-- name: TeamCreate :exec
-- TeamCreate 创建团队
INSERT INTO t_team (team_id, org_id, name, display_name, description, avatar, parent_team_id, path, level, settings, visibility, is_enabled, total_members, total_projects, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: TeamUpdateByTeamId :exec
-- TeamUpdateByTeamId 按 team_id 更新团队（动态字段由调用方传入）
UPDATE t_team SET display_name = ?, description = ?, avatar = ?, settings = ?, visibility = ?, is_enabled = ?, updated_at = ? WHERE team_id = ?;

-- name: TeamDeleteByTeamId :exec
-- TeamDeleteByTeamId 按 team_id 删除团队
DELETE FROM t_team WHERE team_id = ?;

-- name: TeamGetByTeamId :one
-- TeamGetByTeamId 按 team_id 获取团队
SELECT id, team_id, org_id, name, display_name, description, avatar, parent_team_id, path, level, settings, visibility, is_enabled, total_members, total_projects, created_at, updated_at
FROM t_team WHERE team_id = ? LIMIT 1;

-- name: TeamGetByOrgIdAndName :one
-- TeamGetByOrgIdAndName 按 org_id 与 name 获取团队
SELECT id, team_id, org_id, name, display_name, description, avatar, parent_team_id, path, level, settings, visibility, is_enabled, total_members, total_projects, created_at, updated_at
FROM t_team WHERE org_id = ? AND name = ? LIMIT 1;

-- name: TeamListCount :one
-- TeamListCount 团队列表总数（可选条件：org_id, name LIKE, parent_team_id, visibility, is_enabled）
SELECT COUNT(*) FROM t_team
WHERE (? = '' OR org_id = ?)
  AND (? = '' OR name LIKE ?)
  AND (? = '' OR parent_team_id = ?)
  AND (? IS NULL OR visibility = ?)
  AND (? IS NULL OR is_enabled = ?);

-- name: TeamList :many
-- TeamList 团队列表分页
SELECT team_id, org_id, name, display_name, description, avatar, parent_team_id, path, level, settings, visibility, is_enabled, total_members, total_projects
FROM t_team
WHERE (? = '' OR org_id = ?) AND (? = '' OR name LIKE ?) AND (? = '' OR parent_team_id = ?) AND (? IS NULL OR visibility = ?) AND (? IS NULL OR is_enabled = ?)
ORDER BY team_id DESC LIMIT ? OFFSET ?;

-- name: TeamListByOrg :many
-- TeamListByOrg 按组织获取团队列表
SELECT team_id, org_id, name, display_name, description, avatar, parent_team_id, path, level, settings, visibility, is_enabled, total_members, total_projects
FROM t_team WHERE org_id = ? AND is_enabled = 1 ORDER BY level ASC, team_id DESC;

-- name: TeamListSubTeams :many
-- TeamListSubTeams 获取子团队列表
SELECT team_id, org_id, name, display_name, description, avatar, parent_team_id, path, level, settings, visibility, is_enabled, total_members, total_projects
FROM t_team WHERE parent_team_id = ?;

-- name: TeamExistsByTeamId :one
-- TeamExistsByTeamId 判断团队是否存在
SELECT 1 FROM t_team WHERE team_id = ? LIMIT 1;

-- name: TeamNameExists :one
-- TeamNameExists 判断组织下团队名称是否存在（可排除指定 team_id）
SELECT COUNT(*) > 0 FROM t_team WHERE org_id = ? AND name = ? AND (? = '' OR team_id != ?);

-- name: TeamUpdatePath :exec
-- TeamUpdatePath 更新团队 path 与 level
UPDATE t_team SET path = ?, level = ?, updated_at = ? WHERE team_id = ?;

-- name: TeamIncrementMembers :exec
-- TeamIncrementMembers 团队成员数 +delta
UPDATE t_team SET total_members = total_members + ?, updated_at = ? WHERE team_id = ?;

-- name: TeamIncrementProjects :exec
-- TeamIncrementProjects 团队项目数 +delta
UPDATE t_team SET total_projects = total_projects + ?, updated_at = ? WHERE team_id = ?;

-- name: TeamUpdateStatistics :exec
-- TeamUpdateStatistics 更新团队统计（total_members, total_projects）
UPDATE t_team SET total_members = ?, total_projects = ?, updated_at = ? WHERE team_id = ?;

-- name: TeamBatchGetByTeamIds :many
-- TeamBatchGetByTeamIds 按 team_id 列表批量获取团队
SELECT id, team_id, org_id, name, display_name, description, avatar, parent_team_id, path, level, settings, visibility, is_enabled, total_members, total_projects, created_at, updated_at
FROM t_team WHERE team_id IN (?);

-- name: TeamListByUser :many
-- TeamListByUser 按用户获取其所属团队列表（JOIN t_team_member）
SELECT t.team_id, t.org_id, t.name, t.display_name, t.description, t.avatar, t.parent_team_id, t.path, t.level, t.settings, t.visibility, t.is_enabled, t.total_members, t.total_projects
FROM t_team t
INNER JOIN t_team_member tm ON t.team_id = tm.team_id
WHERE tm.user_id = ? AND t.is_enabled = 1 ORDER BY t.team_id DESC;
