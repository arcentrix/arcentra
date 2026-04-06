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

-- name: PipelineRunCreate :exec
-- PipelineRunCreate 创建流水线运行
INSERT INTO t_pipeline_run (run_id, pipeline_id, request_id, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: PipelineRunGetByRunId :one
-- PipelineRunGetByRunId 按 run_id 获取流水线运行
SELECT * FROM t_pipeline_run WHERE run_id = ? LIMIT 1;

-- name: PipelineRunUpdateByRunId :exec
-- PipelineRunUpdateByRunId 按 run_id 更新流水线运行（动态字段）
UPDATE t_pipeline_run SET status = COALESCE(?, status), updated_at = ? WHERE run_id = ?;

-- name: PipelineRunGetByPipelineIdAndRequestId :one
-- PipelineRunGetByPipelineIdAndRequestId 按 pipeline_id 与 request_id 获取运行
SELECT * FROM t_pipeline_run WHERE pipeline_id = ? AND request_id = ? LIMIT 1;

-- name: PipelineRunListCount :one
-- PipelineRunListCount 流水线运行列表总数（可选：pipeline_id, status）
SELECT COUNT(*) FROM t_pipeline_run WHERE (? = '' OR pipeline_id = ?) AND (? <= 0 OR status = ?);

-- name: PipelineRunList :many
-- PipelineRunList 流水线运行列表分页
SELECT * FROM t_pipeline_run
WHERE (? = '' OR pipeline_id = ?) AND (? <= 0 OR status = ?)
ORDER BY created_at DESC LIMIT ? OFFSET ?;
