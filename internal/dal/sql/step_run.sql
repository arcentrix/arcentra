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

-- name: StepRunCreate :exec
-- StepRunCreate 创建步骤运行
INSERT INTO t_step_run (step_run_id, pipeline_id, pipeline_run_id, job_id, name, agent_id, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: StepRunGetByStepRunId :one
-- StepRunGetByStepRunId 按 step_run_id 获取步骤运行
SELECT * FROM t_step_run WHERE step_run_id = ? LIMIT 1;

-- name: StepRunGetByPipelineJobStep :one
-- StepRunGetByPipelineJobStep 按 pipeline_id + job_id + step_run_id 获取
SELECT * FROM t_step_run WHERE pipeline_id = ? AND job_id = ? AND step_run_id = ? LIMIT 1;

-- name: StepRunListCount :one
-- StepRunListCount 步骤运行列表总数（动态条件见应用层）
SELECT COUNT(*) FROM t_step_run
WHERE (? = '' OR pipeline_id = ?) AND (? = '' OR pipeline_run_id = ?) AND (? = '' OR job_id = ?) AND (? = '' OR name = ?) AND (? = '' OR agent_id = ?) AND (? <= 0 OR status = ?);

-- name: StepRunList :many
-- StepRunList 步骤运行列表分页
SELECT * FROM t_step_run
WHERE (? = '' OR pipeline_id = ?) AND (? = '' OR pipeline_run_id = ?) AND (? = '' OR job_id = ?) AND (? = '' OR name = ?) AND (? = '' OR agent_id = ?) AND (? <= 0 OR status = ?)
ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: StepRunPatchByStepRunId :exec
-- StepRunPatchByStepRunId 按 step_run_id 部分更新（含 updated_at）
UPDATE t_step_run SET status = COALESCE(?, status), updated_at = ? WHERE step_run_id = ?;

-- name: StepRunDeleteByStepRunId :exec
-- StepRunDeleteByStepRunId 按 step_run_id 删除步骤运行
DELETE FROM t_step_run WHERE step_run_id = ?;

-- name: StepRunArtifactListByStepRunId :many
-- StepRunArtifactListByStepRunId 按 step_run_id 获取产物列表
SELECT * FROM t_step_run_artifact WHERE step_run_id = ? ORDER BY created_at DESC;
