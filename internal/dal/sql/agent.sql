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

-- name: AgentCreate :exec
-- AgentCreate 创建 Agent
INSERT INTO t_agent (agent_id, agent_name, address, port, os, arch, version, status, labels, metrics, is_enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AgentGetByAgentId :one
-- AgentGetByAgentId 按 agent_id 获取 Agent
SELECT id, agent_id, agent_name, address, port, os, arch, version, status, labels, metrics, is_enabled, created_at, updated_at
FROM t_agent WHERE agent_id = ? LIMIT 1;

-- name: AgentUpdateByAgentId :exec
-- AgentUpdateByAgentId 按 agent_id 全量更新 Agent
UPDATE t_agent SET agent_name = ?, address = ?, port = ?, os = ?, arch = ?, version = ?, status = ?, labels = ?, metrics = ?, is_enabled = ?, updated_at = ? WHERE agent_id = ?;

-- name: AgentPatchByAgentId :exec
-- AgentPatchByAgentId 按 agent_id 部分更新（如 status, last_heartbeat）
UPDATE t_agent SET status = COALESCE(?, status), updated_at = ? WHERE agent_id = ?;

-- name: AgentDeleteByAgentId :exec
-- AgentDeleteByAgentId 按 agent_id 删除 Agent
DELETE FROM t_agent WHERE agent_id = ?;

-- name: AgentCount :one
-- AgentCount Agent 总数
SELECT COUNT(*) FROM t_agent;

-- name: AgentList :many
-- AgentList Agent 列表分页
SELECT id, agent_id, agent_name, address, port, os, arch, version, status, labels, metrics, is_enabled
FROM t_agent ORDER BY id LIMIT ? OFFSET ?;

-- name: AgentCountByStatusOnline :one
-- AgentCountByStatusOnline 在线 Agent 数量（status=1）
SELECT COUNT(*) FROM t_agent WHERE status = 1;

-- name: AgentCountByStatusOffline :one
-- AgentCountByStatusOffline 离线 Agent 数量（status=2）
SELECT COUNT(*) FROM t_agent WHERE status = 2;
