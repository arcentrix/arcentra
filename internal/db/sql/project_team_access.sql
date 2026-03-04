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

-- name: ProjectTeamAccessGet :one
-- ProjectTeamAccessGet 按 project_id 与 team_id 获取访问配置
SELECT id, project_id, team_id, access_level, created_at, updated_at FROM t_project_team_access WHERE project_id = ? AND team_id = ? LIMIT 1;

-- name: ProjectTeamAccessListByProjectId :many
-- ProjectTeamAccessListByProjectId 按 project_id 列出
SELECT id, project_id, team_id, access_level, created_at, updated_at FROM t_project_team_access WHERE project_id = ?;

-- name: ProjectTeamAccessListByTeamId :many
-- ProjectTeamAccessListByTeamId 按 team_id 列出
SELECT id, project_id, team_id, access_level, created_at, updated_at FROM t_project_team_access WHERE team_id = ?;

-- name: ProjectTeamAccessCreate :exec
-- ProjectTeamAccessCreate 创建项目-团队访问
INSERT INTO t_project_team_access (project_id, team_id, access_level, created_at, updated_at)
VALUES (?, ?, ?, ?, ?);
