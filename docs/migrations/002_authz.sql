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

-- =========================================================
-- Arcentra AuthZ v2 migration
-- Stage: development (no backward compatibility)
-- =========================================================

-- ----------- 1) Drop legacy access tables -----------
DROP TABLE IF EXISTS user_role_binding;
DROP TABLE IF EXISTS role_menu_binding;
DROP TABLE IF EXISTS project_team_access;

-- ----------- 2) Remove role_id from membership tables -----------
ALTER TABLE organization_member DROP COLUMN IF EXISTS role_id;
ALTER TABLE project_member DROP COLUMN IF EXISTS role_id;
ALTER TABLE team_member DROP COLUMN IF EXISTS role_id;

-- ----------- 3) Recreate role table -----------
DROP TABLE IF EXISTS role;
CREATE TABLE role (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    role_id VARCHAR(64) NOT NULL COMMENT '角色业务ID',
    name VARCHAR(128) NOT NULL COMMENT '角色内部名称',
    display_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '角色显示名称',
    description VARCHAR(500) NOT NULL DEFAULT '' COMMENT '角色描述',
    scope_type VARCHAR(16) NOT NULL DEFAULT 'platform' COMMENT '作用域类型：platform/org/project/team',
    is_system TINYINT NOT NULL DEFAULT 0 COMMENT '0自定义，1内置',
    org_id VARCHAR(64) DEFAULT NULL COMMENT '自定义角色所属组织ID',
    is_enabled TINYINT NOT NULL DEFAULT 1 COMMENT '0禁用，1启用',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE INDEX uk_role_id (role_id),
    INDEX idx_scope_type (scope_type),
    INDEX idx_org_id (org_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色表';

-- ----------- 4) Recreate menu table (route metadata) -----------
DROP TABLE IF EXISTS menu;
CREATE TABLE menu (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    menu_id VARCHAR(64) NOT NULL COMMENT '菜单业务ID',
    parent_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '父菜单ID',
    name VARCHAR(128) NOT NULL COMMENT '路由名称',
    title VARCHAR(128) NOT NULL DEFAULT '' COMMENT '菜单显示标题',
    path VARCHAR(255) NOT NULL DEFAULT '' COMMENT '路由路径',
    component VARCHAR(255) NOT NULL DEFAULT '' COMMENT '组件注册键',
    redirect VARCHAR(255) NOT NULL DEFAULT '' COMMENT '重定向路径',
    is_layout TINYINT NOT NULL DEFAULT 0 COMMENT '0普通节点，1布局节点',
    is_index TINYINT NOT NULL DEFAULT 0 COMMENT '0普通路由，1索引路由',
    icon VARCHAR(64) NOT NULL DEFAULT '' COMMENT '图标名称',
    `order` INT NOT NULL DEFAULT 0 COMMENT '排序',
    meta_json JSON DEFAULT NULL COMMENT '扩展元数据',
    permission_id VARCHAR(128) DEFAULT NULL COMMENT '所需权限ID',
    scope_type VARCHAR(16) NOT NULL DEFAULT 'platform' COMMENT '作用域类型：platform/org/project/team',
    is_visible TINYINT NOT NULL DEFAULT 1 COMMENT '0隐藏，1显示',
    is_enabled TINYINT NOT NULL DEFAULT 1 COMMENT '0禁用，1启用',
    description VARCHAR(500) NOT NULL DEFAULT '' COMMENT '描述',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE INDEX uk_menu_id (menu_id),
    UNIQUE INDEX uk_name (name),
    INDEX idx_parent_id (parent_id),
    INDEX idx_scope_type (scope_type),
    INDEX idx_permission_id (permission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='菜单与路由元数据表';

-- ----------- 5) Recreate approval_request -----------
DROP TABLE IF EXISTS approval_request;
CREATE TABLE approval_request (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    approval_id VARCHAR(64) NOT NULL COMMENT '审批业务ID',
    pipeline_run_id VARCHAR(64) NOT NULL COMMENT '流水线运行ID',
    job_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '作业名称',
    step_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '步骤名称',
    plugin VARCHAR(64) NOT NULL DEFAULT '' COMMENT '审批插件名称',
    status TINYINT NOT NULL DEFAULT 0 COMMENT '0待审批，1已通过，2已拒绝，3已过期，4已取消',
    policy_id VARCHAR(64) DEFAULT NULL COMMENT '匹配到的策略ID',
    mode VARCHAR(16) NOT NULL DEFAULT 'any' COMMENT '审批模式：serial/parallel/any',
    required_approver_count INT NOT NULL DEFAULT 1 COMMENT '所需最少通过人数',
    approved_count INT NOT NULL DEFAULT 0 COMMENT '已通过人数',
    rejected_count INT NOT NULL DEFAULT 0 COMMENT '已拒绝人数',
    environment VARCHAR(32) NOT NULL DEFAULT '' COMMENT '环境',
    requested_by VARCHAR(64) NOT NULL COMMENT '发起人用户ID',
    candidate_approvers JSON DEFAULT NULL COMMENT '候选审批人快照',
    reason TEXT COMMENT '原因',
    callback_url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '回调地址',
    notify_channels VARCHAR(512) NOT NULL DEFAULT '' COMMENT '通知渠道',
    expires_at DATETIME DEFAULT NULL COMMENT '过期时间',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE INDEX uk_approval_id (approval_id),
    INDEX idx_pipeline_run_id (pipeline_run_id),
    INDEX idx_status (status),
    INDEX idx_policy_id (policy_id),
    INDEX idx_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='审批请求表';

-- ----------- 6) Create permission dictionary -----------
CREATE TABLE permission (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    permission_id VARCHAR(128) NOT NULL COMMENT '权限动作ID，例如 pipeline:trigger',
    resource_type VARCHAR(64) NOT NULL COMMENT '资源类型',
    action VARCHAR(64) NOT NULL COMMENT '动作',
    scope_type VARCHAR(16) NOT NULL DEFAULT 'project' COMMENT '作用域类型：platform/org/project/team/pipeline',
    description VARCHAR(500) NOT NULL DEFAULT '' COMMENT '描述',
    is_system TINYINT NOT NULL DEFAULT 1 COMMENT '是否系统内置',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE INDEX uk_permission_id (permission_id),
    INDEX idx_resource_type (resource_type),
    INDEX idx_scope_type (scope_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='权限字典表';

-- ----------- 7) Create role-permission binding -----------
CREATE TABLE role_permission_binding (
    role_id VARCHAR(64) NOT NULL COMMENT '角色ID',
    permission_id VARCHAR(128) NOT NULL COMMENT '权限ID',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    PRIMARY KEY (role_id, permission_id),
    INDEX idx_permission_id (permission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色权限绑定表';

-- ----------- 8) Create role grants -----------
CREATE TABLE role_grant (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    grant_id VARCHAR(64) NOT NULL COMMENT '授权业务ID',
    subject_type VARCHAR(16) NOT NULL COMMENT '主体类型：user/team',
    subject_id VARCHAR(64) NOT NULL COMMENT '主体ID（用户或团队）',
    role_id VARCHAR(64) NOT NULL COMMENT '角色ID',
    scope_type VARCHAR(16) NOT NULL COMMENT '作用域类型：platform/org/project/team/pipeline',
    scope_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '作用域业务ID，平台级为空',
    granted_by VARCHAR(64) NOT NULL DEFAULT '' COMMENT '授权人用户ID',
    expires_at DATETIME DEFAULT NULL COMMENT '过期时间',
    is_enabled TINYINT NOT NULL DEFAULT 1 COMMENT '0撤销，1生效',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE INDEX uk_grant_id (grant_id),
    UNIQUE INDEX uk_grant_unique (subject_type, subject_id, role_id, scope_type, scope_id),
    INDEX idx_subject (subject_type, subject_id),
    INDEX idx_scope (scope_type, scope_id),
    INDEX idx_role_id (role_id),
    INDEX idx_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='角色授权表';

-- ----------- 9) Create approval policies -----------
CREATE TABLE approval_policy (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    policy_id VARCHAR(64) NOT NULL COMMENT '策略业务ID',
    scope_type VARCHAR(16) NOT NULL COMMENT '作用域类型：project/pipeline',
    scope_id VARCHAR(64) NOT NULL COMMENT '作用域业务ID',
    name VARCHAR(128) NOT NULL COMMENT '策略名称',
    match_rule JSON NOT NULL COMMENT '匹配规则',
    approver_rule JSON NOT NULL COMMENT '审批人规则',
    min_approvals INT NOT NULL DEFAULT 1 COMMENT '最小通过人数',
    mode VARCHAR(16) NOT NULL DEFAULT 'any' COMMENT '审批模式：serial/parallel/any',
    timeout_seconds INT NOT NULL DEFAULT 3600 COMMENT '审批超时时间（秒）',
    enabled TINYINT NOT NULL DEFAULT 1 COMMENT '0禁用，1启用',
    description VARCHAR(500) NOT NULL DEFAULT '' COMMENT '描述',
    created_by VARCHAR(64) NOT NULL COMMENT '创建人用户ID',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE INDEX uk_policy_id (policy_id),
    INDEX idx_scope (scope_type, scope_id),
    INDEX idx_enabled (enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='审批策略表';

-- ----------- 10) Create approval decisions -----------
CREATE TABLE approval_decision (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    decision_id VARCHAR(64) NOT NULL COMMENT '决策业务ID',
    approval_id VARCHAR(64) NOT NULL COMMENT '审批ID',
    user_id VARCHAR(64) NOT NULL COMMENT '审批人用户ID',
    decision VARCHAR(16) NOT NULL COMMENT '决策结果：approve/reject',
    comment TEXT COMMENT '审批意见',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    UNIQUE INDEX uk_decision_id (decision_id),
    UNIQUE INDEX uk_approval_user (approval_id, user_id),
    INDEX idx_approval_id (approval_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='审批决策记录表';

-- ----------- 11) Create audit logs -----------
CREATE TABLE audit_log (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '主键',
    log_id VARCHAR(64) NOT NULL COMMENT '日志业务ID',
    actor_user_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '操作人用户ID',
    actor_ip VARCHAR(64) NOT NULL DEFAULT '' COMMENT '操作人IP',
    action VARCHAR(128) NOT NULL COMMENT '操作名称',
    resource_type VARCHAR(64) NOT NULL DEFAULT '' COMMENT '资源类型',
    resource_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '资源业务ID',
    scope_type VARCHAR(16) NOT NULL DEFAULT '' COMMENT '作用域类型',
    scope_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '作用域业务ID',
    result VARCHAR(16) NOT NULL COMMENT '结果：success/denied/error',
    reason VARCHAR(500) NOT NULL DEFAULT '' COMMENT '原因信息',
    payload JSON DEFAULT NULL COMMENT '扩展载荷',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    UNIQUE INDEX uk_log_id (log_id),
    INDEX idx_actor (actor_user_id),
    INDEX idx_action (action),
    INDEX idx_resource (resource_type, resource_id),
    INDEX idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='审计日志表';

-- ----------- 12) 初始化 root 用户平台超管授权（幂等） -----------
-- 约定：
-- 1) 业务ID默认使用 UUID
-- 2) 平台超管角色业务ID固定为 '1'

-- 12.1 确保平台超级管理员角色存在（超管业务ID固定为 1）
INSERT INTO role (role_id, name, display_name, description, scope_type, is_system, is_enabled, created_at, updated_at)
VALUES ('1', 'PlatformSuperAdmin', '平台超级管理员', '平台最高权限', 'platform', 1, 1, NOW(), NOW())
ON DUPLICATE KEY UPDATE
    name = VALUES(name),
    display_name = VALUES(display_name),
    description = VALUES(description),
    scope_type = VALUES(scope_type),
    is_system = 1,
    is_enabled = 1,
    updated_at = NOW();

-- 12.2 确保超管通配权限存在（业务ID使用 UUID，允许任意资源与任意动作）
INSERT INTO permission (permission_id, resource_type, action, scope_type, description, is_system, created_at, updated_at)
VALUES ('00000000-0000-0000-0000-000000000001', '*', '*', 'platform', '平台超级管理员通配权限', 1, NOW(), NOW())
ON DUPLICATE KEY UPDATE
    resource_type = VALUES(resource_type),
    action = VALUES(action),
    scope_type = VALUES(scope_type),
    description = VALUES(description),
    is_system = 1,
    updated_at = NOW();

-- 12.3 绑定平台超管角色与通配权限
INSERT IGNORE INTO role_permission_binding (role_id, permission_id, created_at)
VALUES ('1', '00000000-0000-0000-0000-000000000001', NOW());

-- 12.3.1 内置权限字典（与 internal/control/consts/permission.go SeedBuiltinPermissions 对齐）
INSERT INTO permission (permission_id, resource_type, action, scope_type, description, is_system, created_at, updated_at) VALUES
    ('org:read',                    'org',      'read',                 'org',      '读取组织信息',          1, NOW(), NOW()),
    ('org:update',                  'org',      'update',               'org',      '更新组织信息',          1, NOW(), NOW()),
    ('org:manage_member',           'org',      'manage_member',        'org',      '管理组织成员',          1, NOW(), NOW()),
    ('org:create_project',          'org',      'create_project',       'org',      '在组织内创建项目',      1, NOW(), NOW()),
    ('org:create_team',             'org',      'create_team',          'org',      '在组织内创建团队',      1, NOW(), NOW()),
    ('project:read',                'project',  'read',                 'project',  '读取项目',              1, NOW(), NOW()),
    ('project:update',              'project',  'update',               'project',  '更新项目',              1, NOW(), NOW()),
    ('project:manage_member',       'project',  'manage_member',        'project',  '管理项目成员',          1, NOW(), NOW()),
    ('project:manage_secret',       'project',  'manage_secret',        'project',  '管理项目密钥',          1, NOW(), NOW()),
    ('project:manage_template',     'project',  'manage_template',      'project',  '管理项目模板',          1, NOW(), NOW()),
    ('project:manage_team_access',  'project',  'manage_team_access',   'project',  '管理项目团队权限',      1, NOW(), NOW()),
    ('pipeline:create',             'pipeline', 'create',               'project',  '创建流水线',            1, NOW(), NOW()),
    ('pipeline:read',               'pipeline', 'read',                 'project',  '读取流水线',            1, NOW(), NOW()),
    ('pipeline:update',             'pipeline', 'update',               'project',  '更新流水线',            1, NOW(), NOW()),
    ('pipeline:delete',             'pipeline', 'delete',               'project',  '删除流水线',            1, NOW(), NOW()),
    ('pipeline:trigger',            'pipeline', 'trigger',              'project',  '触发流水线执行',        1, NOW(), NOW()),
    ('pipeline:cancel',             'pipeline', 'cancel',               'project',  '取消流水线执行',        1, NOW(), NOW()),
    ('pipeline:pause',              'pipeline', 'pause',                'project',  '暂停流水线',            1, NOW(), NOW()),
    ('pipeline:resume',             'pipeline', 'resume',               'project',  '恢复流水线',            1, NOW(), NOW()),
    ('pipeline:view_log',           'pipeline', 'view_log',             'project',  '查看流水线日志',        1, NOW(), NOW()),
    ('approval:read',               'approval', 'read',                 'project',  '读取审批单',            1, NOW(), NOW()),
    ('approval:approve',            'approval', 'approve',              'project',  '审批通过',              1, NOW(), NOW()),
    ('approval:reject',             'approval', 'reject',               'project',  '审批拒绝',              1, NOW(), NOW()),
    ('approval:configure_policy',   'approval', 'configure_policy',     'project',  '配置审批策略',          1, NOW(), NOW()),
    ('release:deploy_dev',          'release',  'deploy_dev',           'project',  '部署到开发环境',        1, NOW(), NOW()),
    ('release:deploy_staging',      'release',  'deploy_staging',       'project',  '部署到预发环境',        1, NOW(), NOW()),
    ('release:deploy_prod',         'release',  'deploy_prod',          'project',  '部署到生产环境',        1, NOW(), NOW()),
    ('secret:read',                 'secret',   'read',                 'project',  '读取密钥',              1, NOW(), NOW()),
    ('secret:write',                'secret',   'write',                'project',  '写入密钥',              1, NOW(), NOW()),
    ('secret:delete',               'secret',   'delete',               'project',  '删除密钥',              1, NOW(), NOW()),
    ('agent:read',                  'agent',    'read',                 'platform', '读取 Agent',            1, NOW(), NOW()),
    ('agent:register',              'agent',    'register',             'platform', '注册 Agent',            1, NOW(), NOW()),
    ('agent:approve',               'agent',    'approve',              'platform', '审批 Agent',            1, NOW(), NOW()),
    ('agent:delete',                'agent',    'delete',               'platform', '删除 Agent',            1, NOW(), NOW()),
    ('agent:update_config',         'agent',    'update_config',        'platform', '更新 Agent 配置',       1, NOW(), NOW()),
    ('audit:read',                  'audit',    'read',                 'platform', '读取审计日志',          1, NOW(), NOW())
ON DUPLICATE KEY UPDATE
    resource_type = VALUES(resource_type),
    action = VALUES(action),
    scope_type = VALUES(scope_type),
    description = VALUES(description),
    is_system = 1,
    updated_at = NOW();

-- 12.4 给 root 用户授予平台超管角色（grant_id 使用 UUID）
INSERT INTO role_grant (
    grant_id, subject_type, subject_id, role_id, scope_type, scope_id,
    granted_by, expires_at, is_enabled, created_at, updated_at
)
SELECT
    REPLACE(UUID(), '-', ''),
    'user',
    u.user_id,
    '1',
    'platform',
    '',
    'system',
    NULL,
    1,
    NOW(),
    NOW()
FROM user u
WHERE u.username = 'root'
LIMIT 1
ON DUPLICATE KEY UPDATE
    granted_by = VALUES(granted_by),
    expires_at = VALUES(expires_at),
    is_enabled = 1,
    updated_at = NOW();

-- ----------- 13) 初始化内置菜单（幂等） -----------
-- 与 internal/control/consts/permission.go SeedBuiltinMenus() 保持一致
INSERT INTO menu (
    menu_id, parent_id, name, title, path, component, redirect,
    is_layout, is_index, icon, `order`, meta_json, permission_id,
    scope_type, is_visible, is_enabled, description, created_at, updated_at
) VALUES
    ('menu_dashboard', '', 'Dashboard', 'Overview', '/',         'Dashboard',              '', 0, 1, 'LayoutDashboard', 1, NULL, 'project:read',         'platform', 1, 1, '首页',       NOW(), NOW()),
    ('menu_projects',  '', 'Projects',  'Projects', '/projects', 'Projects',               '', 0, 0, 'Frame',           2, NULL, 'project:read',         'platform', 1, 1, '项目列表',   NOW(), NOW()),
    ('menu_build',     '', 'Build',     'Build',    '/build',    'Build',                  '', 0, 0, 'Hammer',          3, NULL, 'pipeline:read',        'platform', 1, 1, '构建中心',   NOW(), NOW()),
    ('menu_deploy',    '', 'Deploy',    'Deploy',   '/deploy',   'Deploy',                 '', 0, 0, 'Rocket',          4, NULL, 'release:deploy_dev',   'platform', 1, 1, '部署中心',   NOW(), NOW()),
    ('menu_agents',    '', 'Agents',    'Agents',   '/agents',   'Models/Agents/Overview', '', 0, 0, 'Bot',             5, NULL, 'agent:read',           'platform', 1, 1, 'Agent 列表', NOW(), NOW()),
    ('menu_observe',   '', 'Observe',   'Observe',  '/observe',  'Observe',                '', 0, 0, 'Activity',        6, NULL, 'pipeline:view_log',    'platform', 1, 1, '观测中心',   NOW(), NOW()),
    ('menu_secure',    '', 'Secure',    'Secure',   '/secure',   'Secure',                 '', 0, 0, 'Shield',          7, NULL, 'secret:read',          'platform', 1, 1, '安全中心',   NOW(), NOW()),
    ('menu_settings',  '', 'Settings',  'Settings', '/settings', 'Settings',               '', 0, 0, 'Settings2',       8, NULL, 'org:read',             'platform', 1, 1, '设置',       NOW(), NOW()),
    ('menu_users',     '', 'Users',     'Users',    '/users',    'Users',                  '', 0, 0, 'Users',           9, NULL, 'org:manage_member',    'platform', 0, 1, '用户管理',   NOW(), NOW()),
    ('menu_roles',     '', 'Roles',     'Roles',    '/roles',    'Roles',                  '', 0, 0, 'Shield',          10, NULL,'org:manage_member',    'platform', 0, 1, '角色管理',   NOW(), NOW()),
    ('menu_permissions', '', 'Permissions', 'Permissions', '/permissions', 'Permissions',  '', 0, 0, 'Shield',          11, NULL,'org:manage_member',    'platform', 0, 1, '权限字典',   NOW(), NOW())
ON DUPLICATE KEY UPDATE
    parent_id = VALUES(parent_id),
    name = VALUES(name),
    title = VALUES(title),
    path = VALUES(path),
    component = VALUES(component),
    redirect = VALUES(redirect),
    is_layout = VALUES(is_layout),
    is_index = VALUES(is_index),
    icon = VALUES(icon),
    `order` = VALUES(`order`),
    permission_id = VALUES(permission_id),
    scope_type = VALUES(scope_type),
    is_visible = VALUES(is_visible),
    is_enabled = VALUES(is_enabled),
    description = VALUES(description),
    updated_at = NOW();
