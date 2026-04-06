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

-- Arcentra 数据库 Schema (MySQL 8.0+)
-- 字符集: utf8mb4
-- 说明: 表按业务模块分组，执行前会 DROP IF EXISTS，仅供迁移/初始化使用

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- =============================================================================
-- 1. 日志与记录表 (l_*)
-- =============================================================================

-- -------- l_step_run_records 步骤执行记录 --------
DROP TABLE IF EXISTS `l_step_run_records`;
CREATE TABLE `l_step_run_records` (
  `step_run_id` varchar(64) NOT NULL,
  `step_run_type` varchar(64) NOT NULL,
  `status` varchar(32) NOT NULL,
  `queue` varchar(64) NOT NULL,
  `priority` int NOT NULL,
  `pipeline_id` varchar(64) DEFAULT NULL,
  `pipeline_run_id` varchar(64) DEFAULT NULL,
  `stage_id` varchar(64) DEFAULT NULL,
  `job_id` varchar(64) DEFAULT NULL,
  `job_run_id` varchar(64) DEFAULT NULL,
  `agent_id` varchar(64) DEFAULT NULL,
  `payload` json DEFAULT NULL,
  `create_time` datetime NOT NULL,
  `start_time` datetime DEFAULT NULL,
  `end_time` datetime DEFAULT NULL,
  `duration` bigint DEFAULT NULL,
  `retry_count` int NOT NULL,
  `current_retry` int NOT NULL,
  `error_message` text,
  PRIMARY KEY (`step_run_id`),
  KEY `idx_pipeline_run_id` (`pipeline_run_id`),
  KEY `idx_agent_id` (`agent_id`),
  KEY `idx_status` (`status`),
  KEY `idx_create_time` (`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='步骤执行记录表';

-- -------- l_task_records 任务队列记录 --------
DROP TABLE IF EXISTS `l_task_records`;
CREATE TABLE `l_task_records` (
  `task_id` varchar(64) NOT NULL,
  `task_type` varchar(64) NOT NULL,
  `task_payload` json NOT NULL,
  `status` varchar(32) NOT NULL,
  `queue` varchar(64) NOT NULL,
  `priority` int NOT NULL,
  `created_at` datetime NOT NULL,
  `queued_at` datetime DEFAULT NULL,
  `process_at` datetime DEFAULT NULL,
  `started_at` datetime DEFAULT NULL,
  `completed_at` datetime DEFAULT NULL,
  `failed_at` datetime DEFAULT NULL,
  `error` text,
  `retry_count` int NOT NULL,
  `metadata` json DEFAULT NULL,
  PRIMARY KEY (`task_id`),
  KEY `idx_status` (`status`),
  KEY `idx_queue` (`queue`),
  KEY `idx_priority` (`priority`),
  KEY `idx_created_at` (`created_at`),
  KEY `idx_queued_at` (`queued_at`),
  KEY `idx_completed_at` (`completed_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='任务队列记录表';

-- -------- l_terminal_output_records 终端输出 --------
DROP TABLE IF EXISTS `l_terminal_output_records`;
CREATE TABLE `l_terminal_output_records` (
  `session_id` varchar(64) NOT NULL,
  `session_type` varchar(32) NOT NULL,
  `environment` varchar(32) NOT NULL,
  `step_run_id` varchar(64) DEFAULT NULL,
  `pipeline_id` varchar(64) DEFAULT NULL,
  `pipeline_run_id` varchar(64) DEFAULT NULL,
  `user_id` varchar(64) NOT NULL,
  `hostname` varchar(255) NOT NULL,
  `working_directory` varchar(255) NOT NULL,
  `command` text NOT NULL,
  `exit_code` int DEFAULT NULL,
  `logs` json DEFAULT NULL,
  `metadata` json DEFAULT NULL,
  `status` varchar(32) NOT NULL,
  `start_time` datetime NOT NULL,
  `end_time` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  PRIMARY KEY (`session_id`),
  KEY `idx_pipeline_run_id` (`pipeline_run_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_status` (`status`),
  KEY `idx_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='终端输出表';

-- =============================================================================
-- 2. Agent
-- =============================================================================

-- -------- t_agent --------
DROP TABLE IF EXISTS `t_agent`;
CREATE TABLE `t_agent` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `agent_id` varchar(64) NOT NULL COMMENT 'Agent唯一标识',
  `agent_name` varchar(128) NOT NULL COMMENT 'Agent名称',
  `address` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci DEFAULT NULL COMMENT 'Agent地址',
  `port` varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci DEFAULT NULL COMMENT 'Agent端口',
  `os` varchar(32) DEFAULT NULL COMMENT '操作系统',
  `arch` varchar(32) DEFAULT NULL COMMENT '架构(amd64/arm64)',
  `version` varchar(32) DEFAULT NULL COMMENT 'Agent版本',
  `status` tinyint NOT NULL DEFAULT '0' COMMENT 'Agent状态: 0-未知,1-在线,2-离线,3-忙碌,4-空闲',
  `labels` json DEFAULT NULL COMMENT 'Agent标签',
  `metrics` varchar(100) DEFAULT NULL COMMENT 'Agent指标',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0:禁用,1:启用',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_agent_id` (`agent_id`),
  KEY `idx_status` (`status`),
  KEY `idx_is_enabled` (`is_enabled`)
) ENGINE=InnoDB AUTO_INCREMENT=18 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='Agent表';

-- -------- t_agent_ext Agent 配置 --------
DROP TABLE IF EXISTS `t_agent_ext`;
CREATE TABLE `t_agent_ext` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `agent_id` varchar(64) NOT NULL COMMENT 'Agent唯一标识',
  `heartbeat_interval` int NOT NULL DEFAULT '10' COMMENT '心跳间隔(秒)',
  `max_concurrent_jobs` int NOT NULL DEFAULT '1' COMMENT '最大并发任务数',
  `job_timeout` int NOT NULL DEFAULT '3600' COMMENT '任务超时时间(秒)',
  `workspace_dir` varchar(255) DEFAULT NULL COMMENT '工作目录',
  `temp_dir` varchar(255) DEFAULT NULL COMMENT '临时目录',
  `log_level` varchar(32) DEFAULT NULL COMMENT '日志级别',
  `enable_docker` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否启用Docker',
  `docker_network` varchar(64) DEFAULT NULL COMMENT 'Docker网络模式',
  `resource_limits` json DEFAULT NULL COMMENT '资源限制(JSON)',
  `denied_commands` json DEFAULT NULL COMMENT '禁止执行的命令(JSON数组)',
  `env_vars` json DEFAULT NULL COMMENT '环境变量(JSON对象)',
  `proxy_url` varchar(255) DEFAULT NULL COMMENT '代理地址',
  `cache_dir` varchar(255) DEFAULT NULL COMMENT '缓存目录',
  `cleanup_policy` json DEFAULT NULL COMMENT '清理策略(JSON)',
  `description` varchar(512) DEFAULT NULL COMMENT '配置描述',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_agent_id` (`agent_id`)
) ENGINE=InnoDB AUTO_INCREMENT=6 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='Agent 配置表';

-- =============================================================================
-- 3. 审计与事件
-- =============================================================================

-- -------- t_audit_log 操作审计日志 --------
DROP TABLE IF EXISTS `t_audit_log`;
CREATE TABLE `t_audit_log` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `user_id` varchar(64) NOT NULL COMMENT '操作用户ID',
  `username` varchar(64) NOT NULL COMMENT '操作用户名',
  `action` varchar(64) NOT NULL COMMENT '操作动作(create/update/delete/execute)',
  `resource_type` varchar(32) NOT NULL COMMENT '资源类型(pipeline/job/agent/user)',
  `resource_id` varchar(64) DEFAULT NULL COMMENT '资源ID',
  `resource_name` varchar(255) DEFAULT NULL COMMENT '资源名称',
  `ip_address` varchar(64) DEFAULT NULL COMMENT 'IP地址',
  `user_agent` varchar(512) DEFAULT NULL COMMENT 'User Agent',
  `request_params` json DEFAULT NULL COMMENT '请求参数(JSON格式)',
  `response_status` int DEFAULT NULL COMMENT '响应状态码',
  `error_message` text COMMENT '错误信息',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '操作时间',
  PRIMARY KEY (`id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_action` (`action`),
  KEY `idx_resource` (`resource_type`,`resource_id`),
  KEY `idx_create_time` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='操作审计日志表';

-- =============================================================================
-- 4. 系统配置
-- =============================================================================

-- -------- t_general_settings 结构化设置 --------
DROP TABLE IF EXISTS `t_general_settings`;
CREATE TABLE `t_general_settings` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `settings_id` varchar(128) NOT NULL COMMENT '设置ID',
  `category` varchar(64) NOT NULL COMMENT '配置类别',
  `name` varchar(128) NOT NULL COMMENT '配置名称（业务语义字段）',
  `display_name` varchar(128) NOT NULL COMMENT '展示名，如 JWT 密钥',
  `data` json NOT NULL COMMENT '配置内容，结构化 JSON',
  `schema` json DEFAULT NULL COMMENT '配置的结构定义（JSON Schema 格式，用于前端渲染与校验）',
  `description` varchar(255) DEFAULT NULL COMMENT '配置说明',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_category_name_env` (`category`,`name`),
  UNIQUE KEY `uk_settings_id` (`settings_id`)
) ENGINE=InnoDB AUTO_INCREMENT=324 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='系统配置（结构化设置表）';

-- =============================================================================
-- 5. 身份认证 (SSO)
-- =============================================================================

-- -------- t_identity SSO 认证提供者 --------
DROP TABLE IF EXISTS `t_identity`;
CREATE TABLE `t_identity` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `provider_id` varchar(64) NOT NULL COMMENT '提供者唯一标识',
  `name` varchar(128) NOT NULL COMMENT '提供者名称',
  `provider_type` varchar(32) NOT NULL COMMENT '提供者类型(oauth/ldap/oidc/saml)',
  `config` json NOT NULL COMMENT '配置内容(根据type不同,内容结构不同)',
  `description` varchar(512) DEFAULT NULL COMMENT '描述',
  `priority` int NOT NULL DEFAULT '0' COMMENT '优先级(数字越小优先级越高)',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0: disabled, 1: enabled',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_provider_id` (`provider_id`),
  KEY `idx_provider_type` (`provider_type`),
  KEY `idx_priority` (`priority`)
) ENGINE=InnoDB AUTO_INCREMENT=36 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='SSO认证提供者表';

-- =============================================================================
-- 6. 流水线 (Pipeline / Job / Stage)
-- =============================================================================

-- -------- t_job 任务 --------
DROP TABLE IF EXISTS `t_job`;
CREATE TABLE `t_job` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `job_id` varchar(64) NOT NULL COMMENT '任务唯一标识',
  `name` varchar(255) NOT NULL COMMENT '任务名称',
  `pipeline_id` varchar(64) DEFAULT NULL COMMENT '所属流水线ID',
  `pipeline_run_id` varchar(64) DEFAULT NULL COMMENT '所属流水线执行ID',
  `stage_id` varchar(64) DEFAULT NULL COMMENT '所属阶段ID',
  `stage` int NOT NULL DEFAULT '0' COMMENT '阶段序号',
  `agent_id` varchar(64) DEFAULT NULL COMMENT '执行的Agent ID',
  `status` tinyint NOT NULL DEFAULT '1' COMMENT '任务状态: 0-未知, 1-等待, 2-入队, 3-运行中, 4-成功, 5-失败, 6-已取消, 7-超时, 8-已跳过',
  `priority` int NOT NULL DEFAULT '5' COMMENT '优先级: 1-最高, 5-普通, 10-最低',
  `image` varchar(255) DEFAULT NULL COMMENT 'Docker镜像',
  `commands` text COMMENT '执行命令列表(JSON数组)',
  `workspace` varchar(512) DEFAULT NULL COMMENT '工作目录',
  `env` json DEFAULT NULL COMMENT '环境变量(JSON格式)',
  `secrets` json DEFAULT NULL COMMENT '密钥信息(JSON格式)',
  `timeout` int NOT NULL DEFAULT '3600' COMMENT '超时时间(秒)',
  `retry_count` int NOT NULL DEFAULT '0' COMMENT '重试次数',
  `current_retry` int NOT NULL DEFAULT '0' COMMENT '当前重试次数',
  `allow_failure` tinyint NOT NULL DEFAULT '0' COMMENT '是否允许失败: 0-否, 1-是',
  `label_selector` json DEFAULT NULL COMMENT '标签选择器(JSON格式)',
  `tags` varchar(512) DEFAULT NULL COMMENT '任务标签(逗号分隔,已废弃)',
  `depends_on` varchar(512) DEFAULT NULL COMMENT '依赖的任务ID列表(逗号分隔)',
  `exit_code` int DEFAULT NULL COMMENT '退出码',
  `error_message` text COMMENT '错误信息',
  `start_time` datetime DEFAULT NULL COMMENT '开始时间',
  `end_time` datetime DEFAULT NULL COMMENT '结束时间',
  `duration` bigint DEFAULT NULL COMMENT '执行时长(毫秒)',
  `created_by` varchar(64) DEFAULT NULL COMMENT '创建者用户ID',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_job_id` (`job_id`),
  KEY `idx_pipeline_id` (`pipeline_id`),
  KEY `idx_pipeline_run_id` (`pipeline_run_id`),
  KEY `idx_agent_id` (`agent_id`),
  KEY `idx_status` (`status`),
  KEY `idx_priority` (`priority`),
  KEY `idx_start_time` (`start_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='任务表';

-- -------- t_job_artifact 任务产物 --------
DROP TABLE IF EXISTS `t_job_artifact`;
CREATE TABLE `t_job_artifact` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `artifact_id` varchar(64) NOT NULL COMMENT '产物唯一标识',
  `job_id` varchar(64) NOT NULL COMMENT '任务ID',
  `pipeline_run_id` varchar(64) DEFAULT NULL COMMENT '流水线执行ID',
  `name` varchar(255) NOT NULL COMMENT '产物名称',
  `path` varchar(1024) NOT NULL COMMENT '产物路径(支持glob模式)',
  `destination` varchar(1024) DEFAULT NULL COMMENT '目标存储路径',
  `size` bigint DEFAULT NULL COMMENT '文件大小(字节)',
  `storage_type` varchar(32) DEFAULT 'minio' COMMENT '存储类型(minio/s3/oss/gcs/cos)',
  `storage_path` varchar(1024) DEFAULT NULL COMMENT '实际存储路径',
  `expire` tinyint NOT NULL DEFAULT '0' COMMENT '是否过期: 0-否, 1-是',
  `expire_days` int DEFAULT NULL COMMENT '过期天数',
  `expired_at` datetime DEFAULT NULL COMMENT '过期时间',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_artifact_id` (`artifact_id`),
  KEY `idx_job_id` (`job_id`),
  KEY `idx_pipeline_run_id` (`pipeline_run_id`),
  KEY `idx_expire` (`expire`,`expired_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='任务产物表';

-- -------- t_job_plugin 任务插件关联 --------
DROP TABLE IF EXISTS `t_job_plugin`;
CREATE TABLE `t_job_plugin` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `job_id` varchar(64) NOT NULL COMMENT '任务ID',
  `plugin_id` varchar(64) NOT NULL COMMENT '插件ID',
  `plugin_config_id` varchar(64) DEFAULT NULL COMMENT '插件配置ID',
  `params` json DEFAULT NULL COMMENT '任务特定的插件参数',
  `execution_order` int NOT NULL DEFAULT '0' COMMENT '执行顺序',
  `execution_stage` varchar(32) NOT NULL COMMENT '执行阶段(before/after/on_success/on_failure)',
  `status` tinyint DEFAULT '0' COMMENT '执行状态: 0-未执行, 1-执行中, 2-成功, 3-失败',
  `result` text COMMENT '执行结果',
  `error_message` text COMMENT '错误信息',
  `started_at` datetime DEFAULT NULL COMMENT '开始执行时间',
  `completed_at` datetime DEFAULT NULL COMMENT '完成时间',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  KEY `idx_job_id` (`job_id`),
  KEY `idx_plugin_id` (`plugin_id`),
  KEY `idx_execution_order` (`execution_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='任务插件关联表';

-- =============================================================================
-- 7. 菜单
-- =============================================================================

-- -------- t_menu --------
DROP TABLE IF EXISTS `t_menu`;
CREATE TABLE `t_menu` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `menu_id` varchar(64) NOT NULL COMMENT '菜单唯一标识',
  `parent_id` varchar(64) DEFAULT NULL COMMENT '父菜单ID（为空表示顶级菜单）',
  `name` varchar(128) NOT NULL COMMENT '菜单名称',
  `path` varchar(255) DEFAULT NULL COMMENT '菜单路径（路由路径）',
  `component` varchar(255) DEFAULT NULL COMMENT '组件路径（前端组件）',
  `icon` varchar(128) DEFAULT NULL COMMENT '图标（图标名称或URL）',
  `order` int NOT NULL DEFAULT '0' COMMENT '排序（数值越小越靠前）',
  `is_visible` tinyint NOT NULL DEFAULT '1' COMMENT '是否可见：0-隐藏，1-显示',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '是否启用：0-禁用，1-启用',
  `description` varchar(512) DEFAULT NULL COMMENT '菜单描述',
  `meta` text COMMENT '扩展元数据（JSON格式）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_menu_id` (`menu_id`),
  KEY `idx_parent_id` (`parent_id`),
  KEY `idx_order` (`order`),
  KEY `idx_visible` (`is_visible`),
  KEY `idx_enabled` (`is_enabled`)
) ENGINE=InnoDB AUTO_INCREMENT=20 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='菜单表';

-- =============================================================================
-- 8. 通知
-- =============================================================================

-- -------- t_notification_channels 通知渠道 --------
DROP TABLE IF EXISTS `t_notification_channels`;
CREATE TABLE `t_notification_channels` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `channel_id` varchar(100) NOT NULL COMMENT 'Unique channel identifier',
  `name` varchar(200) NOT NULL COMMENT 'Channel name',
  `type` varchar(50) NOT NULL COMMENT 'Channel type (feishu_app/dingtalk/slack/etc)',
  `config` text NOT NULL COMMENT 'Channel configuration (JSON)',
  `auth_config` text COMMENT 'Authentication configuration (JSON, optional)',
  `description` text COMMENT 'Channel description',
  `is_active` tinyint(1) DEFAULT '1' COMMENT 'Active status',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_channel_id` (`channel_id`),
  KEY `idx_type` (`type`),
  KEY `idx_is_active` (`is_active`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='Notification channel configurations';

-- -------- t_notification_logs 通知发送日志 --------
DROP TABLE IF EXISTS `t_notification_logs`;
CREATE TABLE `t_notification_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `template_id` varchar(100) DEFAULT NULL COMMENT 'Template ID used',
  `channel` varchar(50) NOT NULL COMMENT 'Channel name',
  `recipient` varchar(500) DEFAULT NULL COMMENT 'Recipient information',
  `content` text COMMENT 'Rendered content',
  `status` varchar(50) NOT NULL COMMENT 'Status (success/failed)',
  `error_msg` text COMMENT 'Error message if failed',
  `metadata` text COMMENT 'Additional metadata (JSON)',
  `sent_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'Sent timestamp',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_template_id` (`template_id`),
  KEY `idx_channel` (`channel`),
  KEY `idx_status` (`status`),
  KEY `idx_sent_at` (`sent_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='Notification sending logs';

-- -------- t_notification_templates 通知模板 --------
DROP TABLE IF EXISTS `t_notification_templates`;
CREATE TABLE `t_notification_templates` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `template_id` varchar(100) NOT NULL COMMENT 'Unique template identifier',
  `name` varchar(200) NOT NULL COMMENT 'Template name',
  `type` varchar(50) NOT NULL COMMENT 'Template type (build/approval)',
  `channel` varchar(50) NOT NULL COMMENT 'Target channel (dingtalk/feishu/slack/etc)',
  `title` varchar(200) DEFAULT NULL COMMENT 'Template title',
  `content` text NOT NULL COMMENT 'Template content with variables',
  `variables` text COMMENT 'Required variables (JSON array)',
  `format` varchar(50) DEFAULT 'markdown' COMMENT 'Message format (text/markdown/html)',
  `metadata` text COMMENT 'Additional metadata (JSON)',
  `description` text COMMENT 'Template description',
  `is_active` tinyint(1) DEFAULT '1' COMMENT 'Active status',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_template_id` (`template_id`),
  KEY `idx_type` (`type`),
  KEY `idx_channel` (`channel`),
  KEY `idx_is_active` (`is_active`)
) ENGINE=InnoDB AUTO_INCREMENT=7 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='Notification templates';

-- =============================================================================
-- 9. 组织
-- =============================================================================

-- -------- t_organization --------
DROP TABLE IF EXISTS `t_organization`;
CREATE TABLE `t_organization` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `org_id` varchar(64) NOT NULL COMMENT '组织唯一标识',
  `name` varchar(128) NOT NULL COMMENT '组织名称(英文标识)',
  `display_name` varchar(255) NOT NULL COMMENT '组织显示名称',
  `description` text COMMENT '组织描述',
  `logo` varchar(512) DEFAULT NULL COMMENT '组织Logo URL',
  `website` varchar(512) DEFAULT NULL COMMENT '组织官网',
  `email` varchar(128) DEFAULT NULL COMMENT '组织联系邮箱',
  `phone` varchar(32) DEFAULT NULL COMMENT '组织联系电话',
  `address` varchar(512) DEFAULT NULL COMMENT '组织地址',
  `settings` json DEFAULT NULL COMMENT '组织设置(JSON格式)',
  `plan` varchar(32) NOT NULL DEFAULT 'free' COMMENT '订阅计划(free/pro/enterprise)',
  `status` tinyint NOT NULL DEFAULT '1' COMMENT '状态: 0-未激活, 1-正常, 2-冻结, 3-已删除',
  `owner_user_id` varchar(64) NOT NULL COMMENT '组织所有者用户ID',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0: disabled, 1: enabled',
  `total_members` int NOT NULL DEFAULT '0' COMMENT '成员总数',
  `total_teams` int NOT NULL DEFAULT '0' COMMENT '团队总数',
  `total_projects` int NOT NULL DEFAULT '0' COMMENT '项目总数',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_org_id` (`org_id`),
  UNIQUE KEY `uk_name` (`name`),
  KEY `idx_status` (`status`),
  KEY `idx_owner_user_id` (`owner_user_id`),
  KEY `idx_plan` (`plan`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='组织表';

-- -------- t_organization_invitation 组织邀请 --------
DROP TABLE IF EXISTS `t_organization_invitation`;
CREATE TABLE `t_organization_invitation` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `invitation_id` varchar(64) NOT NULL COMMENT '邀请唯一标识',
  `org_id` varchar(64) NOT NULL COMMENT '组织ID',
  `email` varchar(128) NOT NULL COMMENT '被邀请人邮箱',
  `role` varchar(32) NOT NULL DEFAULT 'member' COMMENT '角色(owner/admin/member)',
  `token` varchar(255) NOT NULL COMMENT '邀请令牌',
  `invited_by` varchar(64) NOT NULL COMMENT '邀请人用户ID',
  `status` tinyint NOT NULL DEFAULT '0' COMMENT '状态: 0-待接受, 1-已接受, 2-已拒绝, 3-已过期',
  `expires_at` datetime NOT NULL COMMENT '过期时间',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invitation_id` (`invitation_id`),
  UNIQUE KEY `uk_token` (`token`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_email` (`email`),
  KEY `idx_status` (`status`),
  KEY `idx_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='组织邀请表';

-- -------- t_organization_member 组织成员 --------
DROP TABLE IF EXISTS `t_organization_member`;
CREATE TABLE `t_organization_member` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `org_id` varchar(64) NOT NULL COMMENT '组织ID',
  `user_id` varchar(64) NOT NULL COMMENT '用户ID',
  `role_id` varchar(64) NOT NULL COMMENT '角色ID（引用 t_role）',
  `username` varchar(64) DEFAULT NULL COMMENT '用户名(冗余)',
  `email` varchar(128) DEFAULT NULL COMMENT '邮箱(冗余)',
  `invited_by` varchar(64) DEFAULT NULL COMMENT '邀请人用户ID',
  `status` tinyint NOT NULL DEFAULT '1' COMMENT '状态: 0-待接受, 1-正常, 2-禁用',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_org_user` (`org_id`,`user_id`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role` (`role_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='组织成员表';

-- =============================================================================
-- 10. 流水线定义与执行
-- =============================================================================

-- -------- t_pipeline 流水线定义 --------
DROP TABLE IF EXISTS `t_pipeline`;
CREATE TABLE `t_pipeline` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `pipeline_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '流水线唯一标识',
  `project_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '所属项目ID',
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '流水线名称',
  `description` text COLLATE utf8mb4_unicode_ci COMMENT '流水线描述',
  `repo_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '代码仓库URL',
  `default_branch` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT 'main' COMMENT '默认分支',
  `pipeline_file_path` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '流水线定义文件路径',
  `status` tinyint NOT NULL DEFAULT '1' COMMENT '流水线状态: 0-未知, 1-等待, 2-运行中, 3-成功, 4-失败, 5-已取消, 6-暂停',
  `save_mode` tinyint NOT NULL DEFAULT '1' COMMENT '保存模式: 1-直推, 2-PR/MR',
  `pr_target_branch` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'PR/MR 目标分支',
  `metadata` json DEFAULT NULL COMMENT '扩展元数据(JSON格式)',
  `last_sync_status` tinyint NOT NULL DEFAULT '0' COMMENT '最近同步状态: 0-未知, 1-成功, 2-失败',
  `last_sync_message` text COLLATE utf8mb4_unicode_ci COMMENT '最近同步消息',
  `last_synced_at` datetime DEFAULT NULL COMMENT '最近同步时间',
  `last_editor` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近编辑者',
  `last_commit_sha` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近提交 SHA',
  `last_save_request_id` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近保存请求幂等 ID',
  `total_runs` int NOT NULL DEFAULT '0' COMMENT '总执行次数',
  `success_runs` int NOT NULL DEFAULT '0' COMMENT '成功次数',
  `failed_runs` int NOT NULL DEFAULT '0' COMMENT '失败次数',
  `created_by` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '创建者用户ID',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '是否启用: 0-禁用, 1-启用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pipeline_id` (`pipeline_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_name` (`name`(191)),
  KEY `idx_status` (`status`),
  KEY `idx_created_by` (`created_by`),
  KEY `idx_is_enabled` (`is_enabled`),
  KEY `idx_last_save_request_id` (`last_save_request_id`(64))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='流水线定义表';

-- -------- t_pipeline_run 流水线执行记录 --------
DROP TABLE IF EXISTS `t_pipeline_run`;
CREATE TABLE `t_pipeline_run` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `run_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '流水线执行唯一标识',
  `pipeline_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '流水线ID',
  `request_id` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '触发请求幂等 ID',
  `pipeline_name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '流水线名称(冗余)',
  `branch` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '分支',
  `commit_sha` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'Commit SHA',
  `definition_commit_sha` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '定义文件对应 Commit SHA',
  `definition_path` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '定义文件路径',
  `status` tinyint NOT NULL DEFAULT '1' COMMENT '执行状态: 0-未知, 1-等待, 2-运行中, 3-成功, 4-失败, 5-已取消, 6-暂停',
  `trigger_type` tinyint NOT NULL DEFAULT '1' COMMENT '触发类型: 0-未知, 1-手动, 2-Webhook, 3-定时, 4-API',
  `triggered_by` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '触发者用户ID',
  `env` json DEFAULT NULL COMMENT '环境变量(JSON格式)',
  `total_jobs` int NOT NULL DEFAULT '0' COMMENT '总任务数',
  `completed_jobs` int NOT NULL DEFAULT '0' COMMENT '已完成任务数',
  `failed_jobs` int NOT NULL DEFAULT '0' COMMENT '失败任务数',
  `running_jobs` int NOT NULL DEFAULT '0' COMMENT '运行中任务数',
  `current_stage` int NOT NULL DEFAULT '0' COMMENT '当前阶段',
  `total_stages` int NOT NULL DEFAULT '0' COMMENT '总阶段数',
  `start_time` datetime DEFAULT NULL COMMENT '开始时间',
  `end_time` datetime DEFAULT NULL COMMENT '结束时间',
  `duration` bigint DEFAULT NULL COMMENT '执行时长(毫秒)',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_run_id` (`run_id`),
  UNIQUE KEY `uk_pipeline_request_id` (`pipeline_id`,`request_id`),
  KEY `idx_pipeline_id` (`pipeline_id`),
  KEY `idx_request_id` (`request_id`),
  KEY `idx_status` (`status`),
  KEY `idx_triggered_by` (`triggered_by`),
  KEY `idx_start_time` (`start_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='流水线执行记录表';

-- -------- t_pipeline_stage 流水线阶段 --------
DROP TABLE IF EXISTS `t_pipeline_stage`;
CREATE TABLE `t_pipeline_stage` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `stage_id` varchar(64) NOT NULL COMMENT '阶段唯一标识',
  `pipeline_id` varchar(64) NOT NULL COMMENT '流水线ID',
  `name` varchar(255) NOT NULL COMMENT '阶段名称',
  `stage_order` int NOT NULL DEFAULT '0' COMMENT '阶段顺序',
  `parallel` tinyint NOT NULL DEFAULT '0' COMMENT '是否并行执行: 0-否, 1-是',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_stage_id` (`stage_id`),
  KEY `idx_pipeline_id` (`pipeline_id`),
  KEY `idx_stage_order` (`stage_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='流水线阶段表';

-- -------- t_step_run 步骤执行 --------
DROP TABLE IF EXISTS `t_step_run`;
CREATE TABLE `t_step_run` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `step_run_id` varchar(64) NOT NULL COMMENT '步骤运行唯一标识',
  `pipeline_id` varchar(64) NOT NULL COMMENT '流水线ID',
  `pipeline_run_id` varchar(64) DEFAULT NULL COMMENT '流水线运行ID',
  `job_id` varchar(64) DEFAULT NULL COMMENT '任务ID',
  `name` varchar(255) DEFAULT NULL COMMENT '步骤名称',
  `agent_id` varchar(64) DEFAULT NULL COMMENT '执行 Agent ID',
  `status` tinyint NOT NULL DEFAULT '1' COMMENT '状态: 1-等待 2-入队 3-运行中 4-成功 5-失败 6-已取消 7-超时 8-已跳过',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_step_run_id` (`step_run_id`),
  KEY `idx_pipeline_id` (`pipeline_id`),
  KEY `idx_pipeline_run_id` (`pipeline_run_id`),
  KEY `idx_job_id` (`job_id`),
  KEY `idx_agent_id` (`agent_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='步骤执行表';

-- -------- t_step_run_artifact 步骤执行产物 --------
DROP TABLE IF EXISTS `t_step_run_artifact`;
CREATE TABLE `t_step_run_artifact` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `artifact_id` varchar(64) NOT NULL COMMENT '产物唯一标识',
  `step_run_id` varchar(64) NOT NULL COMMENT '步骤运行ID',
  `job_run_id` varchar(64) DEFAULT NULL COMMENT '任务运行ID',
  `pipeline_run_id` varchar(64) DEFAULT NULL COMMENT '流水线运行ID',
  `name` varchar(255) NOT NULL COMMENT '产物名称',
  `path` varchar(1024) NOT NULL COMMENT '产物路径',
  `destination` varchar(1024) DEFAULT NULL COMMENT '目标路径',
  `size` bigint DEFAULT NULL COMMENT '大小(字节)',
  `storage_type` varchar(32) DEFAULT NULL COMMENT '存储类型',
  `storage_path` varchar(1024) DEFAULT NULL COMMENT '存储路径',
  `expire` tinyint NOT NULL DEFAULT '0' COMMENT '是否过期',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_artifact_id` (`artifact_id`),
  KEY `idx_step_run_id` (`step_run_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='步骤执行产物表';

-- =============================================================================
-- 11. 项目
-- =============================================================================

-- -------- t_project --------
DROP TABLE IF EXISTS `t_project`;
CREATE TABLE `t_project` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` varchar(64) NOT NULL COMMENT '项目唯一标识',
  `org_id` varchar(64) NOT NULL COMMENT '所属组织ID',
  `name` varchar(128) NOT NULL COMMENT '项目名称(英文标识)',
  `display_name` varchar(255) NOT NULL COMMENT '项目显示名称',
  `namespace` varchar(255) NOT NULL COMMENT '项目命名空间(org_name/project_name)',
  `description` text COMMENT '项目描述',
  `repo_url` varchar(512) NOT NULL COMMENT '代码仓库URL',
  `repo_type` varchar(32) NOT NULL DEFAULT 'git' COMMENT '仓库类型(git/github/gitlab/gitee/svn)',
  `default_branch` varchar(128) NOT NULL DEFAULT 'main' COMMENT '默认分支',
  `auth_type` tinyint NOT NULL DEFAULT '0' COMMENT '认证类型: 0-无, 1-用户名密码, 2-Token, 3-SSH密钥',
  `credential` text COMMENT '认证凭证(加密存储)',
  `trigger_mode` int NOT NULL DEFAULT '1' COMMENT '触发模式(位掩码): 1-手动, 2-Webhook, 4-定时, 8-Push, 16-MR, 32-Tag',
  `webhook_secret` varchar(255) DEFAULT NULL COMMENT 'Webhook密钥',
  `cron_expr` varchar(128) DEFAULT NULL COMMENT '定时任务Cron表达式',
  `build_config` json DEFAULT NULL COMMENT '构建配置(JSON格式)',
  `env_vars` json DEFAULT NULL COMMENT '环境变量(JSON格式)',
  `settings` json DEFAULT NULL COMMENT '项目设置(JSON格式)',
  `tags` varchar(512) DEFAULT NULL COMMENT '项目标签(逗号分隔)',
  `language` varchar(64) DEFAULT NULL COMMENT '主要编程语言(Go/Java/Python/Node.js等)',
  `framework` varchar(128) DEFAULT NULL COMMENT '使用的框架',
  `status` tinyint NOT NULL DEFAULT '1' COMMENT '项目状态: 0-未激活, 1-正常, 2-归档, 3-禁用',
  `visibility` tinyint NOT NULL DEFAULT '0' COMMENT '可见性: 0-私有, 1-内部, 2-公开',
  `access_level` varchar(32) NOT NULL DEFAULT 'team' COMMENT '默认访问级别(owner/team/org)',
  `created_by` varchar(64) NOT NULL COMMENT '创建者用户ID',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '是否启用: 0-禁用, 1-启用',
  `icon` varchar(512) DEFAULT NULL COMMENT '项目图标URL',
  `homepage` varchar(512) DEFAULT NULL COMMENT '项目主页',
  `total_pipelines` int NOT NULL DEFAULT '0' COMMENT '流水线总数',
  `total_builds` int NOT NULL DEFAULT '0' COMMENT '构建总次数',
  `success_builds` int NOT NULL DEFAULT '0' COMMENT '成功构建次数',
  `failed_builds` int NOT NULL DEFAULT '0' COMMENT '失败构建次数',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_id` (`project_id`),
  UNIQUE KEY `uk_name` (`name`),
  UNIQUE KEY `uk_namespace` (`namespace`),
  KEY `idx_status` (`status`),
  KEY `idx_visibility` (`visibility`),
  KEY `idx_created_by` (`created_by`),
  KEY `idx_is_enabled` (`is_enabled`),
  KEY `idx_repo_type` (`repo_type`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_project_access` (`access_level`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='项目表';

-- -------- t_project_member 项目成员 --------
DROP TABLE IF EXISTS `t_project_member`;
CREATE TABLE `t_project_member` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` varchar(64) NOT NULL COMMENT '项目ID',
  `user_id` varchar(64) NOT NULL COMMENT '用户ID',
  `role` varchar(32) NOT NULL COMMENT '角色(owner/maintainer/developer/reporter/guest)',
  `username` varchar(64) DEFAULT NULL COMMENT '用户名(冗余)',
  `source` varchar(32) NOT NULL DEFAULT 'direct' COMMENT '来源(direct/team/org)',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_user` (`project_id`,`user_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role` (`role`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='项目成员表';

-- -------- t_project_team_access 项目团队访问 --------
DROP TABLE IF EXISTS `t_project_team_access`;
CREATE TABLE `t_project_team_access` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` varchar(64) NOT NULL COMMENT '项目ID',
  `team_id` varchar(64) NOT NULL COMMENT '团队ID',
  `access_level` varchar(32) NOT NULL COMMENT '访问权限: read/write/admin',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_team` (`project_id`,`team_id`),
  KEY `idx_project` (`project_id`),
  KEY `idx_team` (`team_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='项目团队访问权限表';

-- -------- t_project_team_relation 项目团队关联 --------
DROP TABLE IF EXISTS `t_project_team_relation`;
CREATE TABLE `t_project_team_relation` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` varchar(64) NOT NULL COMMENT '项目ID',
  `team_id` varchar(64) NOT NULL COMMENT '团队ID',
  `access` varchar(32) NOT NULL DEFAULT 'read' COMMENT '访问权限(read/write/admin)',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_team` (`project_id`,`team_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_team_id` (`team_id`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='项目团队关联表';

-- -------- t_project_variable 项目变量 --------
DROP TABLE IF EXISTS `t_project_variable`;
CREATE TABLE `t_project_variable` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `variable_id` varchar(64) NOT NULL COMMENT '变量唯一标识',
  `project_id` varchar(64) NOT NULL COMMENT '项目ID',
  `key` varchar(255) NOT NULL COMMENT '变量键',
  `value` text NOT NULL COMMENT '变量值(敏感信息加密存储)',
  `type` varchar(32) NOT NULL DEFAULT 'env' COMMENT '类型(env/secret/file)',
  `protected` tinyint NOT NULL DEFAULT '0' COMMENT '是否保护(仅在保护分支可用): 0-否, 1-是',
  `masked` tinyint NOT NULL DEFAULT '0' COMMENT '是否掩码(日志中隐藏): 0-否, 1-是',
  `description` varchar(512) DEFAULT NULL COMMENT '描述',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_variable_id` (`variable_id`),
  UNIQUE KEY `uk_project_key` (`project_id`,`key`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_type` (`type`)
) ENGINE=InnoDB AUTO_INCREMENT=4 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='项目变量表';

-- -------- t_project_webhook 项目 Webhook --------
DROP TABLE IF EXISTS `t_project_webhook`;
CREATE TABLE `t_project_webhook` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `webhook_id` varchar(64) NOT NULL COMMENT 'Webhook唯一标识',
  `project_id` varchar(64) NOT NULL COMMENT '项目ID',
  `name` varchar(128) NOT NULL COMMENT 'Webhook名称',
  `url` varchar(512) NOT NULL COMMENT 'Webhook URL',
  `secret` varchar(255) DEFAULT NULL COMMENT '密钥',
  `events` json NOT NULL COMMENT '触发事件列表(push/merge_request/tag等)',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0: disabled, 1: enabled',
  `description` varchar(512) DEFAULT NULL COMMENT '描述',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_webhook_id` (`webhook_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_is_enabled` (`is_enabled`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='项目Webhook表';

-- =============================================================================
-- 12. 角色与权限
-- =============================================================================

-- -------- t_role --------
DROP TABLE IF EXISTS `t_role`;
CREATE TABLE `t_role` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `role_id` varchar(64) NOT NULL COMMENT '角色唯一标识',
  `name` varchar(64) NOT NULL COMMENT '角色名称',
  `display_name` varchar(128) DEFAULT NULL COMMENT '角色显示名称',
  `description` varchar(512) DEFAULT NULL COMMENT '角色描述',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0: disabled, 1: enabled',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_id` (`role_id`),
  KEY `idx_enabled` (`is_enabled`)
) ENGINE=InnoDB AUTO_INCREMENT=14 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色表（支持自定义角色）';

-- -------- t_role_menu_binding 角色菜单关联 --------
DROP TABLE IF EXISTS `t_role_menu_binding`;
CREATE TABLE `t_role_menu_binding` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `role_menu_id` varchar(64) NOT NULL COMMENT '关联唯一标识',
  `role_id` varchar(64) NOT NULL COMMENT '角色ID（引用 t_role 表）',
  `menu_id` varchar(64) NOT NULL COMMENT '菜单ID（引用 t_menu 表）',
  `resource_id` varchar(64) DEFAULT NULL COMMENT '资源ID（组织ID/团队ID/项目ID，平台级为空）',
  `is_visible` tinyint NOT NULL DEFAULT '1' COMMENT '是否可见：0-隐藏，1-显示',
  `is_accessible` tinyint NOT NULL DEFAULT '1' COMMENT '是否可访问：0-不可访问，1-可访问',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_menu_id` (`role_menu_id`),
  UNIQUE KEY `uk_role_menu` (`role_id`,`menu_id`,`resource_id`),
  KEY `idx_role_id` (`role_id`),
  KEY `idx_menu_id` (`menu_id`),
  KEY `idx_resource_id` (`resource_id`),
  KEY `idx_visible` (`is_visible`),
  KEY `idx_accessible` (`is_accessible`),
  KEY `idx_rmb_role_accessible_res` (`role_id`,`is_accessible`,`resource_id`)
) ENGINE=InnoDB AUTO_INCREMENT=37 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色菜单关联表';

-- =============================================================================
-- 13. 密钥与存储
-- =============================================================================

-- -------- t_secret 密钥管理 --------
DROP TABLE IF EXISTS `t_secret`;
CREATE TABLE `t_secret` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `secret_id` varchar(64) NOT NULL COMMENT '密钥唯一标识',
  `name` varchar(255) NOT NULL COMMENT '密钥名称',
  `secret_type` varchar(32) NOT NULL COMMENT '密钥类型(password/token/ssh_key/env)',
  `secret_value` text NOT NULL COMMENT '密钥值(加密存储)',
  `description` varchar(512) DEFAULT NULL COMMENT '密钥描述',
  `scope` varchar(32) NOT NULL DEFAULT 'global' COMMENT '作用域(global/pipeline/user)',
  `scope_id` varchar(64) DEFAULT NULL COMMENT '作用域ID',
  `created_by` varchar(64) NOT NULL COMMENT '创建者用户ID',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_secret_id` (`secret_id`),
  KEY `idx_name` (`name`(191)),
  KEY `idx_scope` (`scope`,`scope_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='密钥管理表';

-- -------- t_storage_config 对象存储配置 --------
DROP TABLE IF EXISTS `t_storage_config`;
CREATE TABLE `t_storage_config` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `storage_id` varchar(64) NOT NULL COMMENT '存储唯一标识',
  `name` varchar(128) NOT NULL COMMENT '存储名称',
  `storage_type` varchar(32) NOT NULL COMMENT '存储类型(minio/s3/oss/gcs/cos)',
  `config` json NOT NULL COMMENT '存储配置(根据type不同,内容结构不同)',
  `description` varchar(512) DEFAULT NULL COMMENT '描述',
  `is_default` tinyint NOT NULL DEFAULT '0' COMMENT '0: not default, 1: default',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0: disabled, 1: enabled',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_storage_id` (`storage_id`),
  KEY `idx_storage_type` (`storage_type`),
  KEY `idx_is_default` (`is_default`),
  KEY `idx_is_enabled` (`is_enabled`)
) ENGINE=InnoDB AUTO_INCREMENT=26 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='对象存储配置表';

-- -------- t_system_event 系统事件 --------
DROP TABLE IF EXISTS `t_system_event`;
CREATE TABLE `t_system_event` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `event_id` varchar(64) NOT NULL COMMENT '事件唯一标识',
  `event_type` tinyint NOT NULL COMMENT '事件类型: 1-任务创建, 2-任务开始, 3-任务完成, 4-任务失败, 5-Agent上线, 6-流水线开始, 7-流水线完成, 8-流水线失败',
  `resource_type` varchar(32) NOT NULL COMMENT '资源类型(job/pipeline/agent)',
  `resource_id` varchar(64) NOT NULL COMMENT '资源ID',
  `resource_name` varchar(255) DEFAULT NULL COMMENT '资源名称',
  `message` text COMMENT '事件消息',
  `metadata` json DEFAULT NULL COMMENT '事件元数据(JSON格式)',
  `user_id` varchar(64) DEFAULT NULL COMMENT '关联用户ID',
  `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event_id` (`event_id`),
  KEY `idx_event_type` (`event_type`),
  KEY `idx_resource` (`resource_type`,`resource_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_create_time` (`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='系统事件表';

-- =============================================================================
-- 14. 团队
-- =============================================================================

-- -------- t_team --------
DROP TABLE IF EXISTS `t_team`;
CREATE TABLE `t_team` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `team_id` varchar(64) NOT NULL COMMENT '团队唯一标识',
  `org_id` varchar(64) NOT NULL COMMENT '所属组织ID',
  `name` varchar(128) NOT NULL COMMENT '团队名称(英文标识)',
  `display_name` varchar(255) NOT NULL COMMENT '团队显示名称',
  `description` text COMMENT '团队描述',
  `avatar` varchar(512) DEFAULT NULL COMMENT '团队头像',
  `parent_team_id` varchar(64) DEFAULT NULL COMMENT '父团队ID(支持嵌套)',
  `path` varchar(512) DEFAULT NULL COMMENT '团队路径(用于层级关系,如:/parent/child)',
  `level` int NOT NULL DEFAULT '1' COMMENT '团队层级(1为顶层)',
  `settings` json DEFAULT NULL COMMENT '团队设置(JSON格式)',
  `visibility` tinyint NOT NULL DEFAULT '0' COMMENT '可见性: 0-私有, 1-内部, 2-公开',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0: disabled, 1: enabled',
  `total_members` int NOT NULL DEFAULT '0' COMMENT '成员总数',
  `total_projects` int NOT NULL DEFAULT '0' COMMENT '项目总数',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_team_id` (`team_id`),
  UNIQUE KEY `uk_org_name` (`org_id`,`name`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_parent_team_id` (`parent_team_id`),
  KEY `idx_visibility` (`visibility`),
  KEY `idx_path` (`path`(191))
) ENGINE=InnoDB AUTO_INCREMENT=4 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='团队表';

-- -------- t_team_member 团队成员 --------
DROP TABLE IF EXISTS `t_team_member`;
CREATE TABLE `t_team_member` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `team_id` varchar(64) NOT NULL COMMENT '团队ID',
  `user_id` varchar(64) NOT NULL COMMENT '用户ID',
  `role` varchar(32) NOT NULL COMMENT '团队角色(owner/maintainer/developer/reporter/guest)',
  `username` varchar(64) DEFAULT NULL COMMENT '用户名(冗余)',
  `invited_by` varchar(64) DEFAULT NULL COMMENT '邀请人用户ID',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_team_user` (`team_id`,`user_id`),
  KEY `idx_team_id` (`team_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role` (`role`)
) ENGINE=InnoDB AUTO_INCREMENT=4 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='团队成员表';

-- -------- t_team_variable 团队变量 --------
DROP TABLE IF EXISTS `t_team_variable`;
CREATE TABLE `t_team_variable` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `variable_id` varchar(64) NOT NULL COMMENT '变量唯一标识',
  `team_id` varchar(64) NOT NULL COMMENT '团队ID',
  `key` varchar(255) NOT NULL COMMENT '变量键',
  `value` text NOT NULL COMMENT '变量值(敏感信息加密存储)',
  `type` varchar(32) NOT NULL DEFAULT 'env' COMMENT '类型(env/secret/file)',
  `protected` tinyint NOT NULL DEFAULT '0' COMMENT '是否保护(仅在保护分支可用): 0-否, 1-是',
  `masked` tinyint NOT NULL DEFAULT '0' COMMENT '是否掩码(日志中隐藏): 0-否, 1-是',
  `description` varchar(512) DEFAULT NULL COMMENT '描述',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_variable_id` (`variable_id`),
  UNIQUE KEY `uk_team_key` (`team_id`,`key`),
  KEY `idx_team_id` (`team_id`),
  KEY `idx_type` (`type`)
) ENGINE=InnoDB AUTO_INCREMENT=5 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='团队变量表';

-- =============================================================================
-- 15. 用户
-- =============================================================================

-- -------- t_user --------
DROP TABLE IF EXISTS `t_user`;
CREATE TABLE `t_user` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `user_id` varchar(64) NOT NULL COMMENT '用户唯一标识',
  `username` varchar(64) NOT NULL COMMENT '用户名',
  `full_name` varchar(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci DEFAULT '' COMMENT 'full name',
  `password` varchar(255) NOT NULL COMMENT '密码(加密)',
  `avatar` varchar(512) DEFAULT NULL COMMENT '头像URL',
  `email` varchar(128) DEFAULT NULL COMMENT '邮箱',
  `phone` varchar(32) DEFAULT NULL COMMENT '手机号',
  `is_enabled` tinyint NOT NULL DEFAULT '1' COMMENT '0: disabled, 1: enabled',
  `is_super_admin` tinyint NOT NULL DEFAULT '0' COMMENT '0: normal user, 1: super admin',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_id` (`user_id`),
  UNIQUE KEY `uk_username` (`username`),
  UNIQUE KEY `uk_email` (`email`),
  KEY `idx_is_enabled` (`is_enabled`),
  KEY `idx_is_super_admin` (`is_super_admin`),
  KEY `idx_user_username_enabled` (`username`,`is_enabled`),
  KEY `idx_user_email_enabled` (`email`,`is_enabled`)
) ENGINE=InnoDB AUTO_INCREMENT=20 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户表';

-- -------- t_user_ext 用户扩展 --------
DROP TABLE IF EXISTS `t_user_ext`;
CREATE TABLE `t_user_ext` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT 'Auto-increment ID',
  `user_id` varchar(64) NOT NULL COMMENT 'User ID (foreign key to t_user)',
  `timezone` varchar(100) DEFAULT 'UTC' COMMENT 'User timezone (e.g., Asia/Shanghai, America/New_York)',
  `last_login_at` timestamp NULL DEFAULT NULL COMMENT 'Last login timestamp',
  `invitation_status` varchar(20) DEFAULT 'pending' COMMENT 'Invitation status: pending, accepted, expired, revoked',
  `invited_by` varchar(64) DEFAULT '' COMMENT 'Invited by user ID',
  `invited_at` timestamp NULL DEFAULT NULL COMMENT 'Invitation timestamp',
  `accepted_at` timestamp NULL DEFAULT NULL COMMENT 'Invitation accepted timestamp',
  `created_at` timestamp NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'Created timestamp',
  `updated_at` timestamp NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT 'Updated timestamp',
  PRIMARY KEY (`id`),
  UNIQUE KEY `user_id` (`user_id`),
  KEY `idx_invitation_status` (`invitation_status`),
  KEY `idx_invited_by` (`invited_by`),
  KEY `idx_last_login_at` (`last_login_at`)
) ENGINE=InnoDB AUTO_INCREMENT=6 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户扩展表';

-- -------- t_user_role_binding 用户角色绑定 --------
DROP TABLE IF EXISTS `t_user_role_binding`;
CREATE TABLE `t_user_role_binding` (
  `id` int NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `binding_id` varchar(64) NOT NULL COMMENT '绑定唯一标识',
  `user_id` varchar(64) NOT NULL COMMENT '用户ID',
  `role_id` varchar(64) NOT NULL COMMENT '角色ID',
  `granted_by` varchar(64) DEFAULT NULL COMMENT '授权人ID',
  `create_time` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `binding_id` (`binding_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role_id` (`role_id`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户角色绑定表（支持多层级权限管理）';

SET FOREIGN_KEY_CHECKS = 1;
