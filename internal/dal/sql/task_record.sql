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

-- name: TaskRecordUpsert :exec
-- TaskRecordUpsert 插入或更新任务记录（MySQL: ON DUPLICATE KEY UPDATE）
INSERT INTO l_task_records (task_id, task_type, task_payload, status, queue, priority, created_at, queued_at, process_at, started_at, completed_at, failed_at, error, retry_count, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE task_type = VALUES(task_type), task_payload = VALUES(task_payload), status = VALUES(status), queue = VALUES(queue), priority = VALUES(priority), queued_at = VALUES(queued_at), process_at = VALUES(process_at), started_at = VALUES(started_at), completed_at = VALUES(completed_at), failed_at = VALUES(failed_at), error = VALUES(error), retry_count = VALUES(retry_count), metadata = VALUES(metadata);

-- name: TaskRecordUpdateStatus :exec
-- TaskRecordUpdateStatus 更新任务状态及时间戳（按 status 写 started_at/completed_at/failed_at/error）
UPDATE l_task_records SET status = ?, started_at = COALESCE(?, started_at), completed_at = COALESCE(?, completed_at), failed_at = COALESCE(?, failed_at), error = COALESCE(?, error) WHERE task_id = ?;

-- name: TaskRecordGetByTaskId :one
-- TaskRecordGetByTaskId 按 task_id 获取任务记录
SELECT * FROM l_task_records WHERE task_id = ? LIMIT 1;

-- name: TaskRecordList :many
-- TaskRecordList 任务记录列表（可选 status IN, queue, priority, created_at 范围，分页）
SELECT * FROM l_task_records
WHERE (? = 0 OR status IN (?)) AND (? = '' OR queue = ?) AND (? IS NULL OR priority = ?)
  AND (? IS NULL OR created_at >= ?) AND (? IS NULL OR created_at <= ?)
ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: TaskRecordDeleteByTaskId :exec
-- TaskRecordDeleteByTaskId 按 task_id 删除任务记录
DELETE FROM l_task_records WHERE task_id = ?;
