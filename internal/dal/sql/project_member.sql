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

-- name: ProjectMemberGet :one
-- ProjectMemberGet 按 project_id 与 user_id 获取项目成员
SELECT id, project_id, user_id, role, created_at, updated_at FROM t_project_member WHERE project_id = ? AND user_id = ? LIMIT 1;

-- name: ProjectMemberListByProjectId :many
-- ProjectMemberListByProjectId 按 project_id 列出成员
SELECT id, project_id, user_id, role, created_at, updated_at FROM t_project_member WHERE project_id = ?;

-- name: ProjectMemberListByUserId :many
-- ProjectMemberListByUserId 按 user_id 列出其项目成员关系
SELECT id, project_id, user_id, role, created_at, updated_at FROM t_project_member WHERE user_id = ?;

-- name: ProjectMemberCreate :exec
-- ProjectMemberCreate 添加项目成员
INSERT INTO t_project_member (project_id, user_id, role, created_at, updated_at)
VALUES (?, ?, ?, ?, ?);

-- name: ProjectMemberUpdateRole :exec
-- ProjectMemberUpdateRole 更新项目成员角色
UPDATE t_project_member SET role = ?, updated_at = ? WHERE project_id = ? AND user_id = ?;

-- name: ProjectMemberRemove :exec
-- ProjectMemberRemove 移除项目成员
DELETE FROM t_project_member WHERE project_id = ? AND user_id = ?;
