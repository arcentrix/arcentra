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

-- ================================================================
-- 角色系统重构迁移脚本（支持自定义角色）
-- 移除全局角色，采用基于项目和团队的角色管理
-- 支持自定义角色和细粒度权限点
-- ================================================================

-- 1. 删除旧的全局角色表
DROP TABLE IF EXISTS `t_role_relation`;
DROP TABLE IF EXISTS `t_role`;

-- 2. 创建新的角色表（支持自定义）
CREATE TABLE IF NOT EXISTS `t_role` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `role_id` VARCHAR(64) NOT NULL COMMENT '角色唯一标识',
  `name` VARCHAR(64) NOT NULL COMMENT '角色名称',
  `display_name` VARCHAR(128) DEFAULT NULL COMMENT '角色显示名称',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '角色描述',
  `scope` VARCHAR(32) NOT NULL COMMENT '作用域: project/team/org',
  `org_id` VARCHAR(64) DEFAULT NULL COMMENT '所属组织ID（全局角色为空）',
  `is_builtin` TINYINT NOT NULL DEFAULT 0 COMMENT '是否内置角色: 0-自定义, 1-内置',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `priority` INT NOT NULL DEFAULT 0 COMMENT '优先级（数值越大权限越高）',
  `permissions` TEXT COMMENT '权限点列表（JSON数组）',
  `created_by` VARCHAR(64) DEFAULT NULL COMMENT '创建者',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_id` (`role_id`),
  KEY `idx_scope` (`scope`),
  KEY `idx_org` (`org_id`),
  KEY `idx_builtin` (`is_builtin`),
  KEY `idx_enabled` (`is_enabled`),
  KEY `idx_priority` (`priority`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色表（支持自定义角色）';

-- 3. 创建项目成员表
CREATE TABLE IF NOT EXISTS `t_project_member` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目ID',
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID',
  `role_id` VARCHAR(64) NOT NULL COMMENT '角色ID（引用 t_role）',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_user` (`project_id`, `user_id`),
  KEY `idx_project` (`project_id`),
  KEY `idx_user` (`user_id`),
  KEY `idx_role` (`role_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='项目成员表';

-- 4. 创建团队表（如果不存在）
CREATE TABLE IF NOT EXISTS `t_team` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `team_id` VARCHAR(64) NOT NULL COMMENT '团队唯一标识',
  `org_id` VARCHAR(64) NOT NULL COMMENT '所属组织ID',
  `name` VARCHAR(128) NOT NULL COMMENT '团队名称',
  `display_name` VARCHAR(128) DEFAULT NULL COMMENT '团队显示名称',
  `description` VARCHAR(512) DEFAULT NULL COMMENT '团队描述',
  `avatar` VARCHAR(512) DEFAULT NULL COMMENT '团队头像URL',
  `parent_team_id` VARCHAR(64) DEFAULT NULL COMMENT '父团队ID（支持嵌套）',
  `path` VARCHAR(512) DEFAULT NULL COMMENT '团队路径',
  `level` INT DEFAULT 0 COMMENT '团队层级',
  `settings` JSON DEFAULT NULL COMMENT '团队设置',
  `visibility` TINYINT DEFAULT 1 COMMENT '可见性: 0-私有, 1-内部, 2-公开',
  `is_enabled` TINYINT NOT NULL DEFAULT 1 COMMENT '是否启用: 0-禁用, 1-启用',
  `total_members` INT DEFAULT 0 COMMENT '成员总数',
  `total_projects` INT DEFAULT 0 COMMENT '项目总数',
  `created_by` VARCHAR(64) NOT NULL COMMENT '创建者用户ID',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_team_id` (`team_id`),
  KEY `idx_org` (`org_id`),
  KEY `idx_name` (`name`),
  KEY `idx_parent` (`parent_team_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='团队表';

-- 5. 创建团队成员表
CREATE TABLE IF NOT EXISTS `t_team_member` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `team_id` VARCHAR(64) NOT NULL COMMENT '团队ID',
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID',
  `role_id` VARCHAR(64) NOT NULL COMMENT '角色ID（引用 t_role）',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_team_user` (`team_id`, `user_id`),
  KEY `idx_team` (`team_id`),
  KEY `idx_user` (`user_id`),
  KEY `idx_role` (`role_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='团队成员表';

-- 6. 创建项目团队访问权限表
CREATE TABLE IF NOT EXISTS `t_project_team_access` (
  `id` INT NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目ID',
  `team_id` VARCHAR(64) NOT NULL COMMENT '团队ID',
  `access_level` VARCHAR(32) NOT NULL COMMENT '访问权限: read/write/admin',
  `create_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `update_time` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_team` (`project_id`, `team_id`),
  KEY `idx_project` (`project_id`),
  KEY `idx_team` (`team_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='项目团队访问权限表';

-- 7. 更新组织成员表，使用 role_id
ALTER TABLE `t_organization_member`
CHANGE COLUMN `role` `role_id` VARCHAR(64) NOT NULL COMMENT '角色ID（引用 t_role）';

-- 8. 插入内置项目角色
INSERT INTO `t_role` (`role_id`, `name`, `display_name`, `description`, `scope`, `is_builtin`, `is_enabled`, `priority`, `permissions`) VALUES
('project_owner', 'owner', '所有者', '项目所有者，拥有完全控制权', 'project', 1, 1, 50, '["project.view","project.edit","project.delete","project.transfer","project.archive","project.settings","project.variables","project.webhook","project.integration","build.view","build.trigger","build.cancel","build.retry","build.delete","build.artifact","build.log","pipeline.view","pipeline.create","pipeline.edit","pipeline.delete","pipeline.run","pipeline.stop","deploy.view","deploy.create","deploy.execute","deploy.rollback","deploy.approve","member.view","member.invite","member.edit","member.remove","team.view","team.project","team.settings","issue.view","issue.create","issue.edit","issue.close","issue.delete","monitor.view","monitor.metrics","monitor.logs","monitor.alert","monitor.dashboard","security.scan","security.audit","security.policy"]'),
('project_maintainer', 'maintainer', '维护者', '项目维护者，可以管理项目和成员', 'project', 1, 1, 40, '["project.view","project.edit","project.archive","project.settings","project.variables","project.webhook","project.integration","build.view","build.trigger","build.cancel","build.retry","build.delete","build.artifact","build.log","pipeline.view","pipeline.create","pipeline.edit","pipeline.delete","pipeline.run","pipeline.stop","deploy.view","deploy.create","deploy.execute","deploy.rollback","deploy.approve","member.view","member.invite","member.edit","member.remove","team.view","team.project","team.settings","issue.view","issue.create","issue.edit","issue.close","issue.delete","monitor.view","monitor.metrics","monitor.logs","monitor.alert","monitor.dashboard","security.scan","security.audit"]'),
('project_developer', 'developer', '开发者', '项目开发者，可以开发和构建', 'project', 1, 1, 30, '["project.view","build.view","build.trigger","build.cancel","build.retry","build.artifact","build.log","pipeline.view","pipeline.create","pipeline.edit","pipeline.run","pipeline.stop","deploy.view","deploy.create","deploy.execute","member.view","team.view","issue.view","issue.create","issue.edit","issue.close","monitor.view","monitor.metrics","monitor.logs","security.scan"]'),
('project_reporter', 'reporter', '报告者', '项目报告者，可以查看和报告问题', 'project', 1, 1, 20, '["project.view","build.view","build.artifact","build.log","pipeline.view","deploy.view","member.view","team.view","issue.view","issue.create","issue.edit","monitor.view","monitor.metrics","monitor.logs"]'),
('project_guest', 'guest', '访客', '项目访客，仅可查看', 'project', 1, 1, 10, '["project.view","build.view","build.log","pipeline.view","deploy.view","member.view","issue.view","monitor.view"]');

-- 9. 插入内置团队角色
INSERT INTO `t_role` (`role_id`, `name`, `display_name`, `description`, `scope`, `is_builtin`, `is_enabled`, `priority`, `permissions`) VALUES
('team_owner', 'owner', '所有者', '团队所有者，完全控制团队', 'team', 1, 1, 50, '["team.view","team.edit","team.delete","team.member","team.project","team.settings"]'),
('team_maintainer', 'maintainer', '维护者', '团队维护者，管理团队成员和项目', 'team', 1, 1, 40, '["team.view","team.edit","team.member","team.project","team.settings"]'),
('team_developer', 'developer', '开发者', '团队开发者，参与开发', 'team', 1, 1, 30, '["team.view","team.project"]'),
('team_reporter', 'reporter', '报告者', '团队报告者，报告问题', 'team', 1, 1, 20, '["team.view"]'),
('team_guest', 'guest', '访客', '团队访客，仅查看', 'team', 1, 1, 10, '["team.view"]');

-- 10. 插入内置组织角色
INSERT INTO `t_role` (`role_id`, `name`, `display_name`, `description`, `scope`, `is_builtin`, `is_enabled`, `priority`, `permissions`) VALUES
('org_owner', 'owner', '所有者', '组织所有者，拥有最高权限', 'org', 1, 1, 50, '["team.view","team.create","team.edit","team.delete","team.member","team.project","team.settings"]'),
('org_admin', 'admin', '管理员', '组织管理员，管理组织、成员、团队', 'org', 1, 1, 40, '["team.view","team.create","team.edit","team.member","team.project","team.settings"]'),
('org_member', 'member', '成员', '组织普通成员', 'org', 1, 1, 10, '["team.view"]');

-- ================================================================
-- 权限点说明
-- ================================================================

-- 项目权限:
--   project.view        - 查看项目
--   project.edit        - 编辑项目设置
--   project.delete      - 删除项目
--   project.transfer    - 转移项目所有权
--   project.archive     - 归档项目
--   project.settings    - 修改项目设置
--   project.variables   - 管理项目变量
--   project.webhook     - 管理Webhook
--   project.integration - 管理集成

-- 构建权限:
--   build.view     - 查看构建
--   build.trigger  - 触发构建
--   build.cancel   - 取消构建
--   build.retry    - 重试构建
--   build.delete   - 删除构建记录
--   build.artifact - 下载构建产物
--   build.log      - 查看构建日志

-- 流水线权限:
--   pipeline.view   - 查看流水线
--   pipeline.create - 创建流水线
--   pipeline.edit   - 编辑流水线
--   pipeline.delete - 删除流水线
--   pipeline.run    - 运行流水线
--   pipeline.stop   - 停止流水线

-- 部署权限:
--   deploy.view     - 查看部署
--   deploy.create   - 创建部署
--   deploy.execute  - 执行部署
--   deploy.rollback - 回滚部署
--   deploy.approve  - 审批部署

-- 成员权限:
--   member.view   - 查看成员
--   member.invite - 邀请成员
--   member.edit   - 编辑成员角色
--   member.remove - 移除成员

-- 团队权限:
--   team.view     - 查看团队
--   team.create   - 创建团队
--   team.edit     - 编辑团队
--   team.delete   - 删除团队
--   team.member   - 管理团队成员
--   team.project  - 管理团队项目
--   team.settings - 修改团队设置

-- Issue权限:
--   issue.view   - 查看Issue
--   issue.create - 创建Issue
--   issue.edit   - 编辑Issue
--   issue.close  - 关闭Issue
--   issue.delete - 删除Issue

-- 监控权限:
--   monitor.view      - 查看监控
--   monitor.metrics   - 查看指标
--   monitor.logs      - 查看日志
--   monitor.alert     - 管理告警
--   monitor.dashboard - 管理仪表板

-- 安全权限:
--   security.scan   - 安全扫描
--   security.audit  - 安全审计
--   security.policy - 安全策略管理

-- ================================================================
-- 自定义角色示例
-- ================================================================

-- 示例1: 构建管理员角色（只能触发和管理构建）
INSERT INTO `t_role` (`role_id`, `name`, `display_name`, `description`, `scope`, `org_id`, `is_builtin`, `priority`, `permissions`) VALUES
('custom_build_admin', 'build_admin', '构建管理员', '只能触发和管理构建', 'project', NULL, 0, 25, '["project.view","build.view","build.trigger","build.cancel","build.retry","build.artifact","build.log","pipeline.view","pipeline.run","member.view"]');

-- 示例2: 部署管理员角色（只能部署）
INSERT INTO `t_role` (`role_id`, `name`, `display_name`, `description`, `scope`, `org_id`, `is_builtin`, `priority`, `permissions`) VALUES
('custom_deploy_admin', 'deploy_admin', '部署管理员', '只能部署', 'project', NULL, 0, 35, '["project.view","build.view","build.log","build.artifact","pipeline.view","pipeline.run","deploy.view","deploy.create","deploy.execute","deploy.rollback","deploy.approve"]');

-- 示例3: 监控管理员角色（只能查看监控和日志）
INSERT INTO `t_role` (`role_id`, `name`, `display_name`, `description`, `scope`, `org_id`, `is_builtin`, `priority`, `permissions`) VALUES
('custom_monitor_admin', 'monitor_admin', '监控管理员', '只能查看监控和日志', 'project', NULL, 0, 15, '["project.view","build.view","build.log","pipeline.view","monitor.view","monitor.metrics","monitor.logs","monitor.alert","monitor.dashboard"]');

-- ================================================================
