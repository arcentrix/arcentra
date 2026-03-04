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

-- name: ProjectCreate :exec
-- ProjectCreate 创建项目
INSERT INTO t_project (project_id, org_id, name, description, language, status, visibility, tags, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ProjectUpdateByProjectId :exec
-- ProjectUpdateByProjectId 按 project_id 更新项目（动态字段）
UPDATE t_project SET name = COALESCE(?, name), description = COALESCE(?, description), status = COALESCE(?, status), updated_at = ? WHERE project_id = ?;

-- name: ProjectSoftDeleteByProjectId :exec
-- ProjectSoftDeleteByProjectId 软删除项目（置为禁用状态）
UPDATE t_project SET status = ?, updated_at = ? WHERE project_id = ?;

-- name: ProjectGetByProjectId :one
-- ProjectGetByProjectId 按 project_id 获取项目
SELECT * FROM t_project WHERE project_id = ? LIMIT 1;

-- name: ProjectGetByOrgIdAndName :one
-- ProjectGetByOrgIdAndName 按 org_id 与 name 获取项目
SELECT * FROM t_project WHERE org_id = ? AND name = ? LIMIT 1;

-- name: ProjectListCount :one
-- ProjectListCount 项目列表总数（可选：org_id, name LIKE, language, status, visibility, tags）
SELECT COUNT(*) FROM t_project
WHERE (? = '' OR org_id = ?) AND (? = '' OR name LIKE ?) AND (? = '' OR language = ?)
  AND (? IS NULL OR status = ?) AND (? IS NULL OR visibility = ?);

-- name: ProjectList :many
-- ProjectList 项目列表分页
SELECT * FROM t_project
WHERE (? = '' OR org_id = ?) AND (? = '' OR name LIKE ?) AND (? = '' OR language = ?) AND (? IS NULL OR status = ?) AND (? IS NULL OR visibility = ?)
ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: ProjectListByOrgCount :one
-- ProjectListByOrgCount 按组织分页总数（可选 status）
SELECT COUNT(*) FROM t_project WHERE org_id = ? AND (? IS NULL OR status = ?);

-- name: ProjectListByOrg :many
-- ProjectListByOrg 按组织分页列表
SELECT * FROM t_project WHERE org_id = ? AND (? IS NULL OR status = ?) ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: ProjectListByUserCount :one
-- ProjectListByUserCount 按用户与可选 org_id/role_id 统计项目数
SELECT COUNT(*) FROM t_project p
INNER JOIN t_project_member pm ON p.project_id = pm.project_id
WHERE pm.user_id = ? AND (? = '' OR p.org_id = ?) AND (? = '' OR pm.role = ?);

-- name: ProjectListByUser :many
-- ProjectListByUser 按用户获取项目列表（JOIN t_project_member）
SELECT p.* FROM t_project p
INNER JOIN t_project_member pm ON p.project_id = pm.project_id
WHERE pm.user_id = ? AND (? = '' OR p.org_id = ?) AND (? = '' OR pm.role = ?)
ORDER BY p.created_at DESC LIMIT ? OFFSET ?;

-- name: ProjectExistsByProjectId :one
-- ProjectExistsByProjectId 判断项目是否存在
SELECT 1 FROM t_project WHERE project_id = ? LIMIT 1;

-- name: ProjectNameExists :one
-- ProjectNameExists 判断组织下项目名称是否存在（可排除指定 project_id）
SELECT COUNT(*) > 0 FROM t_project WHERE org_id = ? AND name = ? AND (? = '' OR project_id != ?);

-- name: ProjectUpdateStatistics :exec
-- ProjectUpdateStatistics 更新项目统计字段
UPDATE t_project SET total_pipelines = COALESCE(?, total_pipelines), total_builds = COALESCE(?, total_builds), success_builds = COALESCE(?, success_builds), updated_at = ? WHERE project_id = ?;

-- name: ProjectSetStatus :exec
-- ProjectSetStatus 设置项目状态（启用/禁用）
UPDATE t_project SET status = ?, updated_at = ? WHERE project_id = ?;
