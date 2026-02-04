-- ================================================================
-- Arcade CI/CD 平台完整数据库表结构设计
-- 数据库: arcade_ci_meta
-- 字符集: utf8mb4
-- 排序规则: utf8mb4_unicode_ci
-- ================================================================

-- ================================================================
-- 1. 用户和权限管理模块
-- ================================================================

-- 用户表
CREATE TABLE IF NOT EXISTS `t_user` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户唯一标识',
  `username` VARCHAR(64) NOT NULL COMMENT '用户名',
  `nick_name` VARCHAR(128) DEFAULT NULL COMMENT '昵称',
  `password` VARCHAR(255) NOT NULL COMMENT '密码(加密)',
  `avatar` VARCHAR(512) DEFAULT NULL COMMENT '头像URL',
  `email` VARCHAR(128) DEFAULT NULL COMMENT '邮箱',
  `phone` VARCHAR(32) DEFAULT NULL COMMENT '手机号',
  `is_enabled` TINYINT NOT NULL DEFAULT 0 COMMENT '是否启用: 0-启用, 1-禁用',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_id` (`user_id`),
  UNIQUE KEY `uk_username` (`username`),
  UNIQUE KEY `uk_email` (`email`),
  KEY `idx_is_enabled` (`is_enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户表';

-- 角色表
CREATE TABLE IF NOT EXISTS `t_role` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `role_id` VARCHAR(64) NOT NULL COMMENT '角色唯一标识',
  `role_name` VARCHAR(128) NOT NULL COMMENT '角色名称',
  `role_code` VARCHAR(64) NOT NULL COMMENT '角色编码',
  `role_desc` VARCHAR(512) DEFAULT NULL COMMENT '角色描述',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_id` (`role_id`),
  UNIQUE KEY `uk_role_code` (`role_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色表';

-- 用户组表
CREATE TABLE IF NOT EXISTS `t_user_group` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `group_id` VARCHAR(64) NOT NULL COMMENT '用户组唯一标识',
  `group_name` VARCHAR(128) NOT NULL COMMENT '用户组名称',
  `group_desc` VARCHAR(512) DEFAULT NULL COMMENT '用户组描述',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_group_id` (`group_id`),
  UNIQUE KEY `uk_group_name` (`group_name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户组表';

-- 角色关系表（用户-角色、用户组-角色）
CREATE TABLE IF NOT EXISTS `t_role_relation` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `role_id` VARCHAR(64) NOT NULL COMMENT '角色ID',
  `user_id` VARCHAR(64) DEFAULT NULL COMMENT '用户ID',
  `group_id` VARCHAR(64) DEFAULT NULL COMMENT '用户组ID',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  KEY `idx_role_id` (`role_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_group_id` (`group_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色关系表';

-- SSO认证提供者表
CREATE TABLE IF NOT EXISTS `t_sso_provider` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `provider_id` VARCHAR(64) NOT NULL COMMENT '提供者唯一标识',
  `name` VARCHAR(128) NOT NULL COMMENT '提供者名称',
  `provider_type` VARCHAR(32) NOT NULL COMMENT '提供者类型(oauth/ldap/oidc/saml)',
  `config` JSON NOT NULL COMMENT '配置内容(根据type不同,内容结构不同)',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '描述',
  `priority` INT NOT NULL DEFAULT 0 COMMENT '优先级(数字越小优先级越高)',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_provider_id` (`provider_id`),
  KEY `idx_provider_type` (`provider_type`),
  KEY `idx_priority` (`priority`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='SSO认证提供者表';

-- ================================================================
-- 2. 组织和团队管理模块
-- ================================================================

-- 组织表
CREATE TABLE IF NOT EXISTS `t_organization` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `org_id` VARCHAR(64) NOT NULL COMMENT '组织唯一标识',
  `name` VARCHAR(128) NOT NULL COMMENT '组织名称(英文标识)',
  `display_name` VARCHAR(255) NOT NULL COMMENT '组织显示名称',
  `description` TEXT DEFAULT NULL COMMENT '组织描述',
  `logo` VARCHAR(512) DEFAULT NULL COMMENT '组织Logo URL',
  `website` VARCHAR(512) DEFAULT NULL COMMENT '组织官网',
  `email` VARCHAR(128) DEFAULT NULL COMMENT '组织联系邮箱',
  `phone` VARCHAR(32) DEFAULT NULL COMMENT '组织联系电话',
  `address` VARCHAR(512) DEFAULT NULL COMMENT '组织地址',
  `settings` JSON DEFAULT NULL COMMENT '组织设置(JSON格式)',
  `plan` VARCHAR(32) NOT NULL DEFAULT 'free' COMMENT '订阅计划(free/pro/enterprise)',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '状态: 0-未激活, 1-正常, 2-冻结, 3-已删除',
  `owner_user_id` VARCHAR(64) NOT NULL COMMENT '组织所有者用户ID',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `total_members` INT NOT NULL DEFAULT 0 COMMENT '成员总数',
  `total_teams` INT NOT NULL DEFAULT 0 COMMENT '团队总数',
  `total_projects` INT NOT NULL DEFAULT 0 COMMENT '项目总数',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_org_id` (`org_id`),
  UNIQUE KEY `uk_name` (`name`),
  KEY `idx_status` (`status`),
  KEY `idx_owner_user_id` (`owner_user_id`),
  KEY `idx_plan` (`plan`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='组织表';

-- 组织成员表
CREATE TABLE IF NOT EXISTS `t_organization_member` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `org_id` VARCHAR(64) NOT NULL COMMENT '组织ID',
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID',
  `role` VARCHAR(32) NOT NULL COMMENT '组织角色(owner/admin/member)',
  `username` VARCHAR(64) DEFAULT NULL COMMENT '用户名(冗余)',
  `email` VARCHAR(128) DEFAULT NULL COMMENT '邮箱(冗余)',
  `invited_by` VARCHAR(64) DEFAULT NULL COMMENT '邀请人用户ID',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '状态: 0-待接受, 1-正常, 2-禁用',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_org_user` (`org_id`, `user_id`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role` (`role`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='组织成员表';

-- 团队表
CREATE TABLE IF NOT EXISTS `t_team` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `team_id` VARCHAR(64) NOT NULL COMMENT '团队唯一标识',
  `org_id` VARCHAR(64) NOT NULL COMMENT '所属组织ID',
  `name` VARCHAR(128) NOT NULL COMMENT '团队名称(英文标识)',
  `display_name` VARCHAR(255) NOT NULL COMMENT '团队显示名称',
  `description` TEXT DEFAULT NULL COMMENT '团队描述',
  `avatar` VARCHAR(512) DEFAULT NULL COMMENT '团队头像',
  `parent_team_id` VARCHAR(64) DEFAULT NULL COMMENT '父团队ID(支持嵌套)',
  `path` VARCHAR(512) DEFAULT NULL COMMENT '团队路径(用于层级关系,如:/parent/child)',
  `level` INT NOT NULL DEFAULT 1 COMMENT '团队层级(1为顶层)',
  `settings` JSON DEFAULT NULL COMMENT '团队设置(JSON格式)',
  `visibility` TINYINT NOT NULL DEFAULT 0 COMMENT '可见性: 0-私有, 1-内部, 2-公开',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `total_members` INT NOT NULL DEFAULT 0 COMMENT '成员总数',
  `total_projects` INT NOT NULL DEFAULT 0 COMMENT '项目总数',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_team_id` (`team_id`),
  UNIQUE KEY `uk_org_name` (`org_id`, `name`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_parent_team_id` (`parent_team_id`),
  KEY `idx_visibility` (`visibility`),
  KEY `idx_path` (`path`(191))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='团队表';

-- 团队成员表
CREATE TABLE IF NOT EXISTS `t_team_member` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `team_id` VARCHAR(64) NOT NULL COMMENT '团队ID',
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID',
  `role` VARCHAR(32) NOT NULL COMMENT '团队角色(owner/maintainer/developer/reporter/guest)',
  `username` VARCHAR(64) DEFAULT NULL COMMENT '用户名(冗余)',
  `invited_by` VARCHAR(64) DEFAULT NULL COMMENT '邀请人用户ID',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_team_user` (`team_id`, `user_id`),
  KEY `idx_team_id` (`team_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role` (`role`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='团队成员表';

-- 组织邀请表
CREATE TABLE IF NOT EXISTS `t_organization_invitation` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `invitation_id` VARCHAR(64) NOT NULL COMMENT '邀请唯一标识',
  `org_id` VARCHAR(64) NOT NULL COMMENT '组织ID',
  `email` VARCHAR(128) NOT NULL COMMENT '被邀请人邮箱',
  `role` VARCHAR(32) NOT NULL DEFAULT 'member' COMMENT '角色(owner/admin/member)',
  `token` VARCHAR(255) NOT NULL COMMENT '邀请令牌',
  `invited_by` VARCHAR(64) NOT NULL COMMENT '邀请人用户ID',
  `status` TINYINT NOT NULL DEFAULT 0 COMMENT '状态: 0-待接受, 1-已接受, 2-已拒绝, 3-已过期',
  `expires_at` DATETIME NOT NULL COMMENT '过期时间',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invitation_id` (`invitation_id`),
  UNIQUE KEY `uk_token` (`token`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_email` (`email`),
  KEY `idx_status` (`status`),
  KEY `idx_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='组织邀请表';

-- ================================================================
-- 3. 项目管理模块
-- ================================================================

-- 项目表
CREATE TABLE IF NOT EXISTS `t_project` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目唯一标识',
  `org_id` VARCHAR(64) NOT NULL COMMENT '所属组织ID',
  `name` VARCHAR(128) NOT NULL COMMENT '项目名称(英文标识)',
  `display_name` VARCHAR(255) NOT NULL COMMENT '项目显示名称',
  `namespace` VARCHAR(255) NOT NULL COMMENT '项目命名空间(org_name/project_name)',
  `description` TEXT DEFAULT NULL COMMENT '项目描述',
  `repo_url` VARCHAR(512) NOT NULL COMMENT '代码仓库URL',
  `repo_type` VARCHAR(32) NOT NULL DEFAULT 'git' COMMENT '仓库类型(git/github/gitlab/gitee/svn)',
  `default_branch` VARCHAR(128) NOT NULL DEFAULT 'main' COMMENT '默认分支',
  `auth_type` TINYINT NOT NULL DEFAULT 0 COMMENT '认证类型: 0-无, 1-用户名密码, 2-Token, 3-SSH密钥',
  `credential` TEXT DEFAULT NULL COMMENT '认证凭证(加密存储)',
  `trigger_mode` INT NOT NULL DEFAULT 1 COMMENT '触发模式(位掩码): 1-手动, 2-Webhook, 4-定时, 8-Push, 16-MR, 32-Tag',
  `webhook_secret` VARCHAR(255) DEFAULT NULL COMMENT 'Webhook密钥',
  `cron_expr` VARCHAR(128) DEFAULT NULL COMMENT '定时任务Cron表达式',
  `build_config` JSON DEFAULT NULL COMMENT '构建配置(JSON格式)',
  `env_vars` JSON DEFAULT NULL COMMENT '环境变量(JSON格式)',
  `settings` JSON DEFAULT NULL COMMENT '项目设置(JSON格式)',
  `tags` VARCHAR(512) DEFAULT NULL COMMENT '项目标签(逗号分隔)',
  `language` VARCHAR(64) DEFAULT NULL COMMENT '主要编程语言(Go/Java/Python/Node.js等)',
  `framework` VARCHAR(128) DEFAULT NULL COMMENT '使用的框架',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '项目状态: 0-未激活, 1-正常, 2-归档, 3-禁用',
  `visibility` TINYINT NOT NULL DEFAULT 0 COMMENT '可见性: 0-私有, 1-内部, 2-公开',
  `access_level` VARCHAR(32) NOT NULL DEFAULT 'team' COMMENT '默认访问级别(owner/team/org)',
  `created_by` VARCHAR(64) NOT NULL COMMENT '创建者用户ID',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `icon` VARCHAR(512) DEFAULT NULL COMMENT '项目图标URL',
  `homepage` VARCHAR(512) DEFAULT NULL COMMENT '项目主页',
  `total_pipelines` INT NOT NULL DEFAULT 0 COMMENT '流水线总数',
  `total_builds` INT NOT NULL DEFAULT 0 COMMENT '构建总次数',
  `success_builds` INT NOT NULL DEFAULT 0 COMMENT '成功构建次数',
  `failed_builds` INT NOT NULL DEFAULT 0 COMMENT '失败构建次数',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_id` (`project_id`),
  UNIQUE KEY `uk_namespace` (`namespace`),
  KEY `idx_org_id` (`org_id`),
  KEY `idx_name` (`name`),
  KEY `idx_status` (`status`),
  KEY `idx_visibility` (`visibility`),
  KEY `idx_created_by` (`created_by`),
  KEY `idx_is_enabled` (`is_enabled`),
  KEY `idx_repo_type` (`repo_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='项目表';

-- 项目成员表
CREATE TABLE IF NOT EXISTS `t_project_member` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目ID',
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID',
  `role` VARCHAR(32) NOT NULL COMMENT '角色(owner/maintainer/developer/reporter/guest)',
  `username` VARCHAR(64) DEFAULT NULL COMMENT '用户名(冗余)',
  `source` VARCHAR(32) NOT NULL DEFAULT 'direct' COMMENT '来源(direct/team/org)',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_user` (`project_id`, `user_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role` (`role`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='项目成员表';

-- 项目团队关联表
CREATE TABLE IF NOT EXISTS `t_project_team_relation` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目ID',
  `team_id` VARCHAR(64) NOT NULL COMMENT '团队ID',
  `access` VARCHAR(32) NOT NULL DEFAULT 'read' COMMENT '访问权限(read/write/admin)',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_team` (`project_id`, `team_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_team_id` (`team_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='项目团队关联表';

-- 项目Webhook表
CREATE TABLE IF NOT EXISTS `t_project_webhook` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `webhook_id` VARCHAR(64) NOT NULL COMMENT 'Webhook唯一标识',
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目ID',
  `name` VARCHAR(128) NOT NULL COMMENT 'Webhook名称',
  `url` VARCHAR(512) NOT NULL COMMENT 'Webhook URL',
  `secret` VARCHAR(255) DEFAULT NULL COMMENT '密钥',
  `events` JSON NOT NULL COMMENT '触发事件列表(push/merge_request/tag等)',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '描述',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_webhook_id` (`webhook_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_is_enabled` (`is_enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='项目Webhook表';

-- 项目变量表
CREATE TABLE IF NOT EXISTS `t_project_variable` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `variable_id` VARCHAR(64) NOT NULL COMMENT '变量唯一标识',
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目ID',
  `key` VARCHAR(255) NOT NULL COMMENT '变量键',
  `value` TEXT NOT NULL COMMENT '变量值(敏感信息加密存储)',
  `type` VARCHAR(32) NOT NULL DEFAULT 'env' COMMENT '类型(env/secret/file)',
  `protected` TINYINT NOT NULL DEFAULT 0 COMMENT '是否保护(仅在保护分支可用): 0-否, 1-是',
  `masked` TINYINT NOT NULL DEFAULT 0 COMMENT '是否掩码(日志中隐藏): 0-否, 1-是',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '描述',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_variable_id` (`variable_id`),
  UNIQUE KEY `uk_project_key` (`project_id`, `key`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_type` (`type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='项目变量表';

-- 团队变量表
CREATE TABLE IF NOT EXISTS `t_team_variable` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `variable_id` VARCHAR(64) NOT NULL COMMENT '变量唯一标识',
  `team_id` VARCHAR(64) NOT NULL COMMENT '团队ID',
  `key` VARCHAR(255) NOT NULL COMMENT '变量键',
  `value` TEXT NOT NULL COMMENT '变量值(敏感信息加密存储)',
  `type` VARCHAR(32) NOT NULL DEFAULT 'env' COMMENT '类型(env/secret/file)',
  `protected` TINYINT NOT NULL DEFAULT 0 COMMENT '是否保护(仅在保护分支可用): 0-否, 1-是',
  `masked` TINYINT NOT NULL DEFAULT 0 COMMENT '是否掩码(日志中隐藏): 0-否, 1-是',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '描述',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_variable_id` (`variable_id`),
  UNIQUE KEY `uk_team_key` (`team_id`, `key`),
  KEY `idx_team_id` (`team_id`),
  KEY `idx_type` (`type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='团队变量表';

-- ================================================================
-- 4. Agent管理模块
-- ================================================================

-- Agent表（执行器）
CREATE TABLE IF NOT EXISTS `t_agent` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `agent_id` VARCHAR(64) NOT NULL COMMENT 'Agent唯一标识',
  `agent_name` VARCHAR(128) NOT NULL COMMENT 'Agent名称',
  `hostname` VARCHAR(255) DEFAULT NULL COMMENT '主机名',
  `address` VARCHAR(255) NOT NULL COMMENT 'Agent地址',
  `port` VARCHAR(10) NOT NULL COMMENT 'Agent端口',
  `username` VARCHAR(64) DEFAULT NULL COMMENT 'SSH用户名',
  `auth_type` TINYINT NOT NULL DEFAULT 0 COMMENT '认证类型: 0-密码, 1-密钥',
  `os` VARCHAR(32) DEFAULT NULL COMMENT '操作系统',
  `arch` VARCHAR(32) DEFAULT NULL COMMENT '架构(amd64/arm64)',
  `version` VARCHAR(32) DEFAULT NULL COMMENT 'Agent版本',
  `status` TINYINT NOT NULL DEFAULT 0 COMMENT 'Agent状态: 0-未知, 1-在线, 2-离线, 3-忙碌, 4-空闲',
  `max_concurrent_jobs` INT NOT NULL DEFAULT 1 COMMENT '最大并发任务数',
  `running_jobs_count` INT NOT NULL DEFAULT 0 COMMENT '正在执行的任务数',
  `labels` JSON DEFAULT NULL COMMENT 'Agent标签(JSON格式)',
  `metrics` JSON DEFAULT NULL COMMENT 'Agent指标(CPU/内存等)',
  `last_heartbeat` DATETIME DEFAULT NULL COMMENT '最后心跳时间',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_agent_id` (`agent_id`),
  KEY `idx_status` (`status`),
  KEY `idx_is_enabled` (`is_enabled`),
  KEY `idx_last_heartbeat` (`last_heartbeat`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Agent表';

-- Agent配置表（每个Agent一条记录）
CREATE TABLE IF NOT EXISTS `t_agent_config` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `agent_id` VARCHAR(64) NOT NULL COMMENT 'Agent唯一标识',
  `config_items` JSON NOT NULL COMMENT '配置项',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '配置描述',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_agent_id` (`agent_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Agent配置表';

-- ================================================================
-- 5. 流水线和任务管理模块
-- ================================================================

-- 流水线定义表
CREATE TABLE IF NOT EXISTS `t_pipeline` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `pipeline_id` VARCHAR(64) NOT NULL COMMENT '流水线唯一标识',
  `project_id` VARCHAR(64) DEFAULT NULL COMMENT '所属项目ID',
  `name` VARCHAR(255) NOT NULL COMMENT '流水线名称',
  `description` TEXT DEFAULT NULL COMMENT '流水线描述',
  `repo_url` VARCHAR(512) DEFAULT NULL COMMENT '代码仓库URL',
  `branch` VARCHAR(128) DEFAULT 'main' COMMENT '分支',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '流水线状态: 0-未知, 1-等待, 2-运行中, 3-成功, 4-失败, 5-已取消, 6-部分成功',
  `trigger_type` TINYINT NOT NULL DEFAULT 1 COMMENT '触发类型: 0-未知, 1-手动, 2-Webhook, 3-定时, 4-API',
  `cron` VARCHAR(128) DEFAULT NULL COMMENT 'Cron表达式(定时触发)',
  `env` JSON DEFAULT NULL COMMENT '全局环境变量(JSON格式)',
  `total_runs` INT NOT NULL DEFAULT 0 COMMENT '总执行次数',
  `success_runs` INT NOT NULL DEFAULT 0 COMMENT '成功次数',
  `failed_runs` INT NOT NULL DEFAULT 0 COMMENT '失败次数',
  `created_by` VARCHAR(64) NOT NULL COMMENT '创建者用户ID',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_pipeline_id` (`pipeline_id`),
  KEY `idx_project_id` (`project_id`),
  KEY `idx_name` (`name`(191)),
  KEY `idx_status` (`status`),
  KEY `idx_created_by` (`created_by`),
  KEY `idx_is_enabled` (`is_enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='流水线定义表';

-- 流水线执行记录表
CREATE TABLE IF NOT EXISTS `t_pipeline_run` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `run_id` VARCHAR(64) NOT NULL COMMENT '流水线执行唯一标识',
  `pipeline_id` VARCHAR(64) NOT NULL COMMENT '流水线ID',
  `pipeline_name` VARCHAR(255) NOT NULL COMMENT '流水线名称(冗余)',
  `branch` VARCHAR(128) DEFAULT NULL COMMENT '分支',
  `commit_sha` VARCHAR(64) DEFAULT NULL COMMENT 'Commit SHA',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '执行状态: 0-未知, 1-等待, 2-运行中, 3-成功, 4-失败, 5-已取消, 6-部分成功',
  `trigger_type` TINYINT NOT NULL DEFAULT 1 COMMENT '触发类型: 0-未知, 1-手动, 2-Webhook, 3-定时, 4-API',
  `triggered_by` VARCHAR(64) DEFAULT NULL COMMENT '触发者用户ID',
  `env` JSON DEFAULT NULL COMMENT '环境变量(JSON格式)',
  `total_jobs` INT NOT NULL DEFAULT 0 COMMENT '总任务数',
  `completed_jobs` INT NOT NULL DEFAULT 0 COMMENT '已完成任务数',
  `failed_jobs` INT NOT NULL DEFAULT 0 COMMENT '失败任务数',
  `running_jobs` INT NOT NULL DEFAULT 0 COMMENT '运行中任务数',
  `current_stage` INT NOT NULL DEFAULT 0 COMMENT '当前阶段',
  `total_stages` INT NOT NULL DEFAULT 0 COMMENT '总阶段数',
  `start_time` DATETIME DEFAULT NULL COMMENT '开始时间',
  `end_time` DATETIME DEFAULT NULL COMMENT '结束时间',
  `duration` BIGINT DEFAULT NULL COMMENT '执行时长(毫秒)',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_run_id` (`run_id`),
  KEY `idx_pipeline_id` (`pipeline_id`),
  KEY `idx_status` (`status`),
  KEY `idx_triggered_by` (`triggered_by`),
  KEY `idx_start_time` (`start_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='流水线执行记录表';

-- 流水线阶段表
CREATE TABLE IF NOT EXISTS `t_pipeline_stage` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `stage_id` VARCHAR(64) NOT NULL COMMENT '阶段唯一标识',
  `pipeline_id` VARCHAR(64) NOT NULL COMMENT '流水线ID',
  `name` VARCHAR(255) NOT NULL COMMENT '阶段名称',
  `stage_order` INT NOT NULL DEFAULT 0 COMMENT '阶段顺序',
  `parallel` TINYINT NOT NULL DEFAULT 0 COMMENT '是否并行执行: 0-否, 1-是',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_stage_id` (`stage_id`),
  KEY `idx_pipeline_id` (`pipeline_id`),
  KEY `idx_stage_order` (`stage_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='流水线阶段表';

-- 任务表
CREATE TABLE IF NOT EXISTS `t_job` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `job_id` VARCHAR(64) NOT NULL COMMENT '任务唯一标识',
  `name` VARCHAR(255) NOT NULL COMMENT '任务名称',
  `pipeline_id` VARCHAR(64) DEFAULT NULL COMMENT '所属流水线ID',
  `pipeline_run_id` VARCHAR(64) DEFAULT NULL COMMENT '所属流水线执行ID',
  `stage_id` VARCHAR(64) DEFAULT NULL COMMENT '所属阶段ID',
  `stage` INT NOT NULL DEFAULT 0 COMMENT '阶段序号',
  `agent_id` VARCHAR(64) DEFAULT NULL COMMENT '执行的Agent ID',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '任务状态: 0-未知, 1-等待, 2-入队, 3-运行中, 4-成功, 5-失败, 6-已取消, 7-超时, 8-已跳过',
  `priority` INT NOT NULL DEFAULT 5 COMMENT '优先级: 1-最高, 5-普通, 10-最低',
  `image` VARCHAR(255) DEFAULT NULL COMMENT 'Docker镜像',
  `commands` TEXT DEFAULT NULL COMMENT '执行命令列表(JSON数组)',
  `workspace` VARCHAR(512) DEFAULT NULL COMMENT '工作目录',
  `env` JSON DEFAULT NULL COMMENT '环境变量(JSON格式)',
  `secrets` JSON DEFAULT NULL COMMENT '密钥信息(JSON格式)',
  `timeout` INT NOT NULL DEFAULT 3600 COMMENT '超时时间(秒)',
  `retry_count` INT NOT NULL DEFAULT 0 COMMENT '重试次数',
  `current_retry` INT NOT NULL DEFAULT 0 COMMENT '当前重试次数',
  `allow_failure` TINYINT NOT NULL DEFAULT 0 COMMENT '是否允许失败: 0-否, 1-是',
  `label_selector` JSON DEFAULT NULL COMMENT '标签选择器(JSON格式)',
  `tags` VARCHAR(512) DEFAULT NULL COMMENT '任务标签(逗号分隔,已废弃)',
  `depends_on` VARCHAR(512) DEFAULT NULL COMMENT '依赖的任务ID列表(逗号分隔)',
  `exit_code` INT DEFAULT NULL COMMENT '退出码',
  `error_message` TEXT DEFAULT NULL COMMENT '错误信息',
  `start_time` DATETIME DEFAULT NULL COMMENT '开始时间',
  `end_time` DATETIME DEFAULT NULL COMMENT '结束时间',
  `duration` BIGINT DEFAULT NULL COMMENT '执行时长(毫秒)',
  `created_by` VARCHAR(64) DEFAULT NULL COMMENT '创建者用户ID',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_job_id` (`job_id`),
  KEY `idx_pipeline_id` (`pipeline_id`),
  KEY `idx_pipeline_run_id` (`pipeline_run_id`),
  KEY `idx_agent_id` (`agent_id`),
  KEY `idx_status` (`status`),
  KEY `idx_priority` (`priority`),
  KEY `idx_start_time` (`start_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='任务表';

-- 任务产物表
CREATE TABLE IF NOT EXISTS `t_job_artifact` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `artifact_id` VARCHAR(64) NOT NULL COMMENT '产物唯一标识',
  `job_id` VARCHAR(64) NOT NULL COMMENT '任务ID',
  `pipeline_run_id` VARCHAR(64) DEFAULT NULL COMMENT '流水线执行ID',
  `name` VARCHAR(255) NOT NULL COMMENT '产物名称',
  `path` VARCHAR(1024) NOT NULL COMMENT '产物路径(支持glob模式)',
  `destination` VARCHAR(1024) DEFAULT NULL COMMENT '目标存储路径',
  `size` BIGINT DEFAULT NULL COMMENT '文件大小(字节)',
  `storage_type` VARCHAR(32) DEFAULT 'minio' COMMENT '存储类型(minio/s3/oss/gcs/cos)',
  `storage_path` VARCHAR(1024) DEFAULT NULL COMMENT '实际存储路径',
  `expire` TINYINT NOT NULL DEFAULT 0 COMMENT '是否过期: 0-否, 1-是',
  `expire_days` INT DEFAULT NULL COMMENT '过期天数',
  `expired_at` DATETIME DEFAULT NULL COMMENT '过期时间',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_artifact_id` (`artifact_id`),
  KEY `idx_job_id` (`job_id`),
  KEY `idx_pipeline_run_id` (`pipeline_run_id`),
  KEY `idx_expire` (`expire`, `expired_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='任务产物表';

-- ================================================================
-- 6. 日志和事件模块
-- ================================================================

-- 系统事件表
CREATE TABLE IF NOT EXISTS `t_system_event` (
  `id` BIGINT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `event_id` VARCHAR(64) NOT NULL COMMENT '事件唯一标识',
  `event_type` TINYINT NOT NULL COMMENT '事件类型: 1-任务创建, 2-任务开始, 3-任务完成, 4-任务失败, 5-Agent上线, 6-流水线开始, 7-流水线完成, 8-流水线失败',
  `resource_type` VARCHAR(32) NOT NULL COMMENT '资源类型(job/pipeline/agent)',
  `resource_id` VARCHAR(64) NOT NULL COMMENT '资源ID',
  `resource_name` VARCHAR(255) DEFAULT NULL COMMENT '资源名称',
  `message` TEXT DEFAULT NULL COMMENT '事件消息',
  `metadata` JSON DEFAULT NULL COMMENT '事件元数据(JSON格式)',
  `user_id` VARCHAR(64) DEFAULT NULL COMMENT '关联用户ID',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event_id` (`event_id`),
  KEY `idx_event_type` (`event_type`),
  KEY `idx_resource` (`resource_type`, `resource_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_create_time` (`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='系统事件表';

-- ================================================================
-- 7. 插件管理模块
-- ================================================================

-- 插件表
CREATE TABLE IF NOT EXISTS `t_plugin` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `plugin_id` VARCHAR(64) NOT NULL COMMENT '插件唯一标识',
  `name` VARCHAR(128) NOT NULL COMMENT '插件名称',
  `version` VARCHAR(32) NOT NULL COMMENT '插件版本',
  `description` TEXT DEFAULT NULL COMMENT '插件描述',
  `author` VARCHAR(128) DEFAULT NULL COMMENT '插件作者',
  `plugin_type` VARCHAR(32) NOT NULL COMMENT '插件类型(notify/deploy/test/build/custom)',
  `entry_point` VARCHAR(255) NOT NULL COMMENT '插件入口(文件路径或命令)',
  `config_schema` JSON DEFAULT NULL COMMENT '配置项Schema定义(JSON Schema格式)',
  `params_schema` JSON DEFAULT NULL COMMENT '参数Schema定义(JSON Schema格式)',
  `default_config` JSON DEFAULT NULL COMMENT '默认配置值',
  `icon` VARCHAR(512) DEFAULT NULL COMMENT '插件图标URL',
  `repository` VARCHAR(512) DEFAULT NULL COMMENT '代码仓库地址',
  `documentation` TEXT DEFAULT NULL COMMENT '文档说明',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `is_builtin` TINYINT NOT NULL DEFAULT 0 COMMENT '是否内置插件: 0-否, 1-是',
  `install_path` VARCHAR(512) DEFAULT NULL COMMENT '插件安装路径',
  `checksum` VARCHAR(128) DEFAULT NULL COMMENT '插件文件校验和(SHA256)',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_plugin_id_version` (`plugin_id`, `version`),
  KEY `idx_plugin_type` (`plugin_type`),
  KEY `idx_is_enabled` (`is_enabled`),
  KEY `idx_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='插件表';

-- 插件配置表
CREATE TABLE IF NOT EXISTS `t_plugin_config` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `config_id` VARCHAR(64) NOT NULL COMMENT '配置唯一标识',
  `plugin_id` VARCHAR(64) NOT NULL COMMENT '插件ID',
  `name` VARCHAR(128) NOT NULL COMMENT '配置名称',
  `config_items` JSON NOT NULL COMMENT '配置项(key-value格式)',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '配置描述',
  `scope` VARCHAR(32) NOT NULL DEFAULT 'global' COMMENT '作用域(global/pipeline/user)',
  `scope_id` VARCHAR(64) DEFAULT NULL COMMENT '作用域ID',
  `is_default` TINYINT NOT NULL DEFAULT 0 COMMENT '是否默认配置: 0-否, 1-是',
  `created_by` VARCHAR(64) DEFAULT NULL COMMENT '创建者用户ID',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_config_id` (`config_id`),
  KEY `idx_plugin_id` (`plugin_id`),
  KEY `idx_scope` (`scope`, `scope_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='插件配置表';

-- 任务插件关联表
CREATE TABLE IF NOT EXISTS `t_job_plugin` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `job_id` VARCHAR(64) NOT NULL COMMENT '任务ID',
  `plugin_id` VARCHAR(64) NOT NULL COMMENT '插件ID',
  `plugin_config_id` VARCHAR(64) DEFAULT NULL COMMENT '插件配置ID',
  `params` JSON DEFAULT NULL COMMENT '任务特定的插件参数',
  `execution_order` INT NOT NULL DEFAULT 0 COMMENT '执行顺序',
  `execution_stage` VARCHAR(32) NOT NULL COMMENT '执行阶段(before/after/on_success/on_failure)',
  `status` TINYINT DEFAULT 0 COMMENT '执行状态: 0-未执行, 1-执行中, 2-成功, 3-失败',
  `result` TEXT DEFAULT NULL COMMENT '执行结果',
  `error_message` TEXT DEFAULT NULL COMMENT '错误信息',
  `started_at` DATETIME DEFAULT NULL COMMENT '开始执行时间',
  `completed_at` DATETIME DEFAULT NULL COMMENT '完成时间',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  KEY `idx_job_id` (`job_id`),
  KEY `idx_plugin_id` (`plugin_id`),
  KEY `idx_execution_order` (`execution_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='任务插件关联表';

-- 插件权限表
CREATE TABLE IF NOT EXISTS `t_plugin_permission` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `plugin_id` VARCHAR(64) NOT NULL COMMENT '插件ID',
  `permission_type` VARCHAR(32) NOT NULL COMMENT '权限类型(file_system/network/process/database/environment)',
  `resource` VARCHAR(255) DEFAULT NULL COMMENT '资源路径或标识',
  `action` VARCHAR(32) NOT NULL COMMENT '操作(read/write/execute)',
  `is_allowed` TINYINT NOT NULL DEFAULT 0 COMMENT '是否允许: 0-否, 1-是',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  KEY `idx_plugin_id` (`plugin_id`),
  KEY `idx_permission_type` (`permission_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='插件权限表';

-- 插件资源配额表
CREATE TABLE IF NOT EXISTS `t_plugin_quota` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `plugin_id` VARCHAR(64) NOT NULL COMMENT '插件ID',
  `max_cpu_percent` INT DEFAULT 50 COMMENT '最大CPU使用率(%)',
  `max_memory_mb` INT DEFAULT 512 COMMENT '最大内存(MB)',
  `max_execution_time` INT DEFAULT 300 COMMENT '最大执行时间(秒)',
  `max_disk_io_mbps` INT DEFAULT 100 COMMENT '最大磁盘IO(MB/s)',
  `max_network_mbps` INT DEFAULT 10 COMMENT '最大网络带宽(MB/s)',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_plugin_id` (`plugin_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='插件资源配额表';

-- 插件执行审计表
CREATE TABLE IF NOT EXISTS `t_plugin_audit` (
  `id` BIGINT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `plugin_id` VARCHAR(64) NOT NULL COMMENT '插件ID',
  `job_id` VARCHAR(64) DEFAULT NULL COMMENT '关联任务ID',
  `user_id` VARCHAR(64) DEFAULT NULL COMMENT '触发用户ID',
  `action` VARCHAR(64) NOT NULL COMMENT '操作(load/execute/unload)',
  `status` VARCHAR(32) NOT NULL COMMENT '状态(success/failed)',
  `error_message` TEXT DEFAULT NULL COMMENT '错误信息',
  `execution_time_ms` INT DEFAULT NULL COMMENT '执行时长(毫秒)',
  `resource_usage` JSON DEFAULT NULL COMMENT '资源使用情况',
  `network_calls` JSON DEFAULT NULL COMMENT '网络调用记录',
  `file_access` JSON DEFAULT NULL COMMENT '文件访问记录',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_plugin_id` (`plugin_id`),
  KEY `idx_job_id` (`job_id`),
  KEY `idx_action` (`action`),
  KEY `idx_create_time` (`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='插件执行审计表';

-- 插件来源表
CREATE TABLE IF NOT EXISTS `t_plugin_source` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `source_id` VARCHAR(64) NOT NULL COMMENT '来源唯一标识',
  `name` VARCHAR(128) NOT NULL COMMENT '来源名称',
  `source_type` VARCHAR(32) NOT NULL COMMENT '来源类型(official/verified/community)',
  `repository` VARCHAR(512) NOT NULL COMMENT '仓库地址',
  `public_key` TEXT DEFAULT NULL COMMENT '签名公钥',
  `is_trusted` TINYINT NOT NULL DEFAULT 0 COMMENT '是否信任: 0-否, 1-是',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_source_id` (`source_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='插件来源表';

-- ================================================================
-- 8. 配置管理模块
-- ================================================================

-- 对象存储配置表
CREATE TABLE IF NOT EXISTS `t_storage_config` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `storage_id` VARCHAR(64) NOT NULL COMMENT '存储唯一标识',
  `name` VARCHAR(128) NOT NULL COMMENT '存储名称',
  `storage_type` VARCHAR(32) NOT NULL COMMENT '存储类型(minio/s3/oss/gcs/cos)',
  `config` JSON NOT NULL COMMENT '存储配置(根据type不同,内容结构不同)',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '描述',
  `is_default` TINYINT NOT NULL DEFAULT 0 COMMENT '是否默认: 0-否, 1-是',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_storage_id` (`storage_id`),
  KEY `idx_storage_type` (`storage_type`),
  KEY `idx_is_default` (`is_default`),
  KEY `idx_is_enabled` (`is_enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='对象存储配置表';

-- 系统配置表
CREATE TABLE IF NOT EXISTS `t_system_config` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `config_key` VARCHAR(128) NOT NULL COMMENT '配置键',
  `config_value` TEXT NOT NULL COMMENT '配置值',
  `config_type` VARCHAR(32) NOT NULL DEFAULT 'string' COMMENT '配置类型(string/json/int/bool)',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '配置描述',
  `is_encrypted` TINYINT NOT NULL DEFAULT 0 COMMENT '是否加密: 0-否, 1-是',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_config_key` (`config_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='系统配置表';

-- 密钥管理表
CREATE TABLE IF NOT EXISTS `t_secret` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `secret_id` VARCHAR(64) NOT NULL COMMENT '密钥唯一标识',
  `name` VARCHAR(255) NOT NULL COMMENT '密钥名称',
  `secret_type` VARCHAR(32) NOT NULL COMMENT '密钥类型(password/token/ssh_key/env)',
  `secret_value` TEXT NOT NULL COMMENT '密钥值(加密存储)',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '密钥描述',
  `scope` VARCHAR(32) NOT NULL DEFAULT 'global' COMMENT '作用域(global/pipeline/user)',
  `scope_id` VARCHAR(64) DEFAULT NULL COMMENT '作用域ID',
  `created_by` VARCHAR(64) NOT NULL COMMENT '创建者用户ID',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_secret_id` (`secret_id`),
  KEY `idx_name` (`name`(191)),
  KEY `idx_scope` (`scope`, `scope_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='密钥管理表';

-- ================================================================
-- 9. 审计日志模块
-- ================================================================

-- 操作审计日志表
CREATE TABLE IF NOT EXISTS `t_audit_log` (
  `id` BIGINT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `user_id` VARCHAR(64) NOT NULL COMMENT '操作用户ID',
  `username` VARCHAR(64) NOT NULL COMMENT '操作用户名',
  `action` VARCHAR(64) NOT NULL COMMENT '操作动作(create/update/delete/execute)',
  `resource_type` VARCHAR(32) NOT NULL COMMENT '资源类型(pipeline/job/agent/user)',
  `resource_id` VARCHAR(64) DEFAULT NULL COMMENT '资源ID',
  `resource_name` VARCHAR(255) DEFAULT NULL COMMENT '资源名称',
  `ip_address` VARCHAR(64) DEFAULT NULL COMMENT 'IP地址',
  `user_agent` VARCHAR(512) DEFAULT NULL COMMENT 'User Agent',
  `request_params` JSON DEFAULT NULL COMMENT '请求参数(JSON格式)',
  `response_status` INT DEFAULT NULL COMMENT '响应状态码',
  `error_message` TEXT DEFAULT NULL COMMENT '错误信息',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '操作时间',
  PRIMARY KEY (`id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_action` (`action`),
  KEY `idx_resource` (`resource_type`, `resource_id`),
  KEY `idx_create_time` (`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='操作审计日志表';

-- ================================================================
-- 初始化数据
-- ================================================================

-- 插入默认管理员用户（密码: admin123，需要在应用层加密）
INSERT INTO `t_user` (`user_id`, `username`, `nick_name`, `password`, `email`, `is_enabled`)
VALUES ('user_admin', 'admin', '系统管理员', 'TO_BE_ENCRYPTED', 'admin@arcade.local', 0)
ON DUPLICATE KEY UPDATE `username` = `username`;

-- 插入默认角色
INSERT INTO `t_role` (`role_id`, `role_name`, `role_code`, `role_desc`)
VALUES
  ('role_admin', '管理员', 'ADMIN', '系统管理员角色'),
  ('role_developer', '开发者', 'DEVELOPER', '开发者角色'),
  ('role_operator', '运维人员', 'OPERATOR', '运维人员角色'),
  ('role_viewer', '查看者', 'VIEWER', '只读查看角色')
ON DUPLICATE KEY UPDATE `role_name` = `role_name`;

-- 绑定管理员角色
INSERT INTO `t_role_relation` (`role_id`, `user_id`)
VALUES ('role_admin', 'user_admin')
ON DUPLICATE KEY UPDATE `role_id` = `role_id`;

-- 插入默认组织
INSERT INTO `t_organization` (
  `org_id`,
  `name`,
  `display_name`,
  `description`,
  `website`,
  `email`,
  `settings`,
  `plan`,
  `status`,
  `owner_user_id`
) VALUES (
  'org_default',
  'default-org',
  '默认组织',
  '这是系统默认组织',
  'https://arcade.example.com',
  'admin@example.com',
  JSON_OBJECT(
    'allow_public_projects', true,
    'require_two_factor', false,
    'allowed_domains', JSON_ARRAY('example.com'),
    'max_members', 100,
    'max_projects', 50,
    'max_teams', 20,
    'max_agents', 10,
    'default_visibility', 'private',
    'enable_saml', false,
    'enable_ldap', false
  ),
  'free',
  1,
  'user_admin'
) ON DUPLICATE KEY UPDATE `name` = `name`;

-- 插入组织成员
INSERT INTO `t_organization_member` (`org_id`, `user_id`, `role`, `username`, `email`, `status`)
VALUES 
  ('org_default', 'user_admin', 'owner', 'admin', 'admin@example.com', 1)
ON DUPLICATE KEY UPDATE `role` = `role`;

-- 插入示例团队
INSERT INTO `t_team` (
  `team_id`,
  `org_id`,
  `name`,
  `display_name`,
  `description`,
  `parent_team_id`,
  `path`,
  `level`,
  `settings`,
  `visibility`
) VALUES (
  'team_dev',
  'org_default',
  'development',
  '开发团队',
  '负责产品研发的核心团队',
  NULL,
  '/development',
  1,
  JSON_OBJECT(
    'default_role', 'developer',
    'allow_member_invite', false,
    'require_approval', true,
    'max_members', 50
  ),
  1
),
(
  'team_ops',
  'org_default',
  'operations',
  '运维团队',
  '负责系统运维和基础设施管理',
  NULL,
  '/operations',
  1,
  JSON_OBJECT(
    'default_role', 'developer',
    'allow_member_invite', false,
    'require_approval', true,
    'max_members', 30
  ),
  1
)
ON DUPLICATE KEY UPDATE `name` = `name`;

-- 插入团队成员
INSERT INTO `t_team_member` (`team_id`, `user_id`, `role`, `username`)
VALUES 
  ('team_dev', 'user_admin', 'owner', 'admin'),
  ('team_ops', 'user_admin', 'maintainer', 'admin')
ON DUPLICATE KEY UPDATE `role` = `role`;

-- 插入示例项目
INSERT INTO `t_project` (
  `project_id`,
  `org_id`, 
  `name`, 
  `display_name`,
  `namespace`, 
  `description`, 
  `repo_url`, 
  `repo_type`, 
  `default_branch`, 
  `auth_type`,
  `trigger_mode`,
  `build_config`,
  `env_vars`,
  `settings`,
  `tags`,
  `language`,
  `framework`,
  `status`,
  `visibility`,
  `access_level`,
  `created_by`
) VALUES (
  'proj_demo',
  'org_default',
  'demo-project',
  '演示项目',
  'default-org/demo-project',
  '这是一个示例项目，用于演示 Arcade CI/CD 平台的功能',
  'https://github.com/example/demo-project.git',
  'github',
  'main',
  2,
  7,
  JSON_OBJECT(
    'dockerfile', 'Dockerfile',
    'build_context', '.',
    'cache_enabled', true,
    'test_enabled', true,
    'lint_enabled', true,
    'scan_enabled', false,
    'artifact_paths', JSON_ARRAY('dist/', 'build/'),
    'artifact_expire', 30
  ),
  JSON_OBJECT(
    'NODE_ENV', 'production',
    'APP_NAME', 'Demo App'
  ),
  JSON_OBJECT(
    'auto_cancel', true,
    'timeout', 3600,
    'max_concurrent', 3,
    'retry_count', 1,
    'notify_on_success', false,
    'notify_on_failure', true,
    'clean_workspace', true,
    'allowed_branches', JSON_ARRAY('main', 'develop', 'release/*'),
    'badge_enabled', true
  ),
  'demo,example,ci-cd',
  'Go',
  'Gin',
  1,
  1,
  'team',
  'user_admin'
) ON DUPLICATE KEY UPDATE `name` = `name`;

-- 插入项目成员
INSERT INTO `t_project_member` (`project_id`, `user_id`, `role`, `username`, `source`)
VALUES 
  ('proj_demo', 'user_admin', 'owner', 'admin', 'direct')
ON DUPLICATE KEY UPDATE `role` = `role`;

-- 插入项目团队关联
INSERT INTO `t_project_team_relation` (`project_id`, `team_id`, `access`)
VALUES 
  ('proj_demo', 'team_dev', 'admin')
ON DUPLICATE KEY UPDATE `access` = `access`;

-- 插入项目变量
INSERT INTO `t_project_variable` (`variable_id`, `project_id`, `key`, `value`, `type`, `protected`, `masked`, `description`)
VALUES 
  ('var_001', 'proj_demo', 'DATABASE_URL', 'mysql://user:pass@localhost:3306/db', 'secret', 1, 1, '数据库连接URL'),
  ('var_002', 'proj_demo', 'API_KEY', 'your-api-key-here', 'secret', 1, 1, 'API密钥'),
  ('var_003', 'proj_demo', 'BUILD_ENV', 'production', 'env', 0, 0, '构建环境')
ON DUPLICATE KEY UPDATE `key` = `key`;

-- 插入团队变量
INSERT INTO `t_team_variable` (`variable_id`, `team_id`, `key`, `value`, `type`, `protected`, `masked`, `description`)
VALUES 
  ('team_var_001', 'team_dev', 'TEAM_API_KEY', 'dev-team-api-key-here', 'secret', 1, 1, '开发团队API密钥'),
  ('team_var_002', 'team_dev', 'DEPLOY_ENV', 'staging', 'env', 0, 0, '部署环境'),
  ('team_var_003', 'team_ops', 'OPS_NOTIFY_URL', 'https://notify.example.com/ops', 'env', 0, 0, '运维团队通知URL'),
  ('team_var_004', 'team_ops', 'DOCKER_REGISTRY', 'registry.example.com', 'env', 0, 0, 'Docker镜像仓库地址')
ON DUPLICATE KEY UPDATE `key` = `key`;

-- 插入示例 Agent 配置
INSERT INTO `t_agent_config` (`agent_id`, `config_items`, `description`)
VALUES (
  'agent_001',
  JSON_OBJECT(
    'heartbeat_interval', 30,
    'max_concurrent_jobs', 5,
    'job_timeout', 3600,
    'workspace_dir', '/var/lib/arcade/workspace',
    'temp_dir', '/var/lib/arcade/temp',
    'log_level', 'INFO',
    'enable_docker', true,
    'docker_network', 'bridge',
    'resource_limits', JSON_OBJECT('cpu', '2', 'memory', '4G'),
    'allowed_commands', JSON_ARRAY('docker', 'kubectl', 'npm', 'yarn', 'go', 'python'),
    'env_vars', JSON_OBJECT('PATH', '/usr/local/bin:/usr/bin:/bin'),
    'cache_dir', '/var/lib/arcade/cache',
    'cleanup_policy', JSON_OBJECT('max_age_days', 7, 'max_size_gb', 50)
  ),
  'Agent 001 默认配置'
) ON DUPLICATE KEY UPDATE `config_items` = `config_items`;

-- 插入官方插件来源
INSERT INTO `t_plugin_source` (`source_id`, `name`, `source_type`, `repository`, `is_trusted`)
VALUES 
  ('source_arcade_official', 'Arcade Official', 'official', 'https://github.com/arcentrix/arcade/plugins', 1),
  ('source_community', 'Community', 'community', 'https://plugins.arcade.io', 0)
ON DUPLICATE KEY UPDATE `name` = `name`;

