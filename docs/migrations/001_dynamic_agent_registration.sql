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

-- ============================================
-- 动态 Agent 注册 — 数据库迁移
-- ============================================
-- 创建 registration_token 注册令牌表，修改 agent 表，插入默认设置。
-- 注册令牌用于 Agent 自注册：管理员创建令牌后分发给 Agent 节点，
-- Agent 使用令牌调用 gRPC Register 自动创建自身记录。

-- 1. 注册令牌表
--    存储 Agent 动态注册所需的共享令牌（bcrypt 哈希），支持过期时间和使用次数限制。
--    明文令牌（art_ 前缀）仅在创建时返回一次，后续不可获取。
CREATE TABLE IF NOT EXISTS registration_token (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    token_hash  VARCHAR(255) NOT NULL COMMENT 'bcrypt 哈希后的令牌值',
    description VARCHAR(500) DEFAULT '' COMMENT '令牌描述（用途/环境等）',
    created_by  VARCHAR(100) DEFAULT '' COMMENT '创建者',
    expires_at  DATETIME DEFAULT NULL COMMENT '过期时间，NULL=永不过期',
    max_uses    INT DEFAULT 0 COMMENT '最大使用次数，0=无限制',
    use_count   INT DEFAULT 0 COMMENT '已使用次数',
    is_active   TINYINT DEFAULT 1 COMMENT '0=已吊销 1=启用中',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE INDEX uk_token_hash (token_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Agent 注册令牌表 — 用于 Agent 动态自注册';

-- 2. agent 表新增 registered_by 字段，标识 Agent 的注册方式
ALTER TABLE agent ADD COLUMN registered_by VARCHAR(100) DEFAULT 'admin' COMMENT '注册方式：admin=管理员创建 dynamic=Agent 自注册' AFTER is_enabled;

-- 2b. agent 表新增 last_heartbeat 字段，记录 Agent 最后一次心跳时间（用于超时判离线）
ALTER TABLE agent ADD COLUMN last_heartbeat DATETIME DEFAULT NULL COMMENT '最后一次心跳时间' AFTER metrics;

-- 3. 插入 AGENT_AUTO_APPROVE 默认设置，控制动态注册的 Agent 是否自动启用
INSERT INTO setting (name, value, created_at, updated_at)
VALUES ('AGENT_AUTO_APPROVE', '{"auto_approve": true}', NOW(), NOW())
ON DUPLICATE KEY UPDATE updated_at = NOW();
