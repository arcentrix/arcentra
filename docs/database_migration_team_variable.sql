-- ================================================================
-- 团队变量表迁移脚本
-- 创建时间: 2025-10-14
-- ================================================================

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

-- 插入示例团队变量
INSERT INTO `t_team_variable` (`variable_id`, `team_id`, `key`, `value`, `type`, `protected`, `masked`, `description`)
VALUES
  ('team_var_001', 'team_dev', 'TEAM_API_KEY', 'dev-team-api-key-here', 'secret', 1, 1, '开发团队API密钥'),
  ('team_var_002', 'team_dev', 'DEPLOY_ENV', 'staging', 'env', 0, 0, '部署环境'),
  ('team_var_003', 'team_ops', 'OPS_NOTIFY_URL', 'https://notify.example.com/ops', 'env', 0, 0, '运维团队通知URL'),
  ('team_var_004', 'team_ops', 'DOCKER_REGISTRY', 'registry.example.com', 'env', 0, 0, 'Docker镜像仓库地址')
ON DUPLICATE KEY UPDATE `key` = `key`;

