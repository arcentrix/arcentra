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

-- name: PipelineCreate :exec
-- PipelineCreate 创建流水线
INSERT INTO t_pipeline (pipeline_id, project_id, name, description, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: PipelineUpdateByPipelineId :exec
-- PipelineUpdateByPipelineId 按 pipeline_id 更新流水线（动态字段）
UPDATE t_pipeline SET name = COALESCE(?, name), description = COALESCE(?, description), status = COALESCE(?, status), updated_at = ? WHERE pipeline_id = ?;

-- name: PipelineGetByPipelineId :one
-- PipelineGetByPipelineId 按 pipeline_id 获取流水线
SELECT * FROM t_pipeline WHERE pipeline_id = ? LIMIT 1;

-- name: PipelineListCount :one
-- PipelineListCount 流水线列表总数（可选：project_id, name LIKE, status）
SELECT COUNT(*) FROM t_pipeline
WHERE (? = '' OR project_id = ?) AND (? = '' OR name LIKE ?) AND (? <= 0 OR status = ?);

-- name: PipelineList :many
-- PipelineList 流水线列表分页
SELECT * FROM t_pipeline
WHERE (? = '' OR project_id = ?) AND (? = '' OR name LIKE ?) AND (? <= 0 OR status = ?)
ORDER BY created_at DESC LIMIT ? OFFSET ?;
