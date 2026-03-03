-- ============================================================
-- 插件注册状态迁移脚本
-- 文件: database_migration_plugin_register_status.sql
-- 描述: 为 t_plugin 表添加注册状态相关字段
-- 创建时间: 2025-10-17
-- ============================================================

-- 为 t_plugin 表添加注册状态字段
ALTER TABLE `t_plugin`
ADD COLUMN `register_status` INT NOT NULL DEFAULT 0 COMMENT '注册状态: 0=未注册 1=注册中 2=已注册 3=注册失败' AFTER `is_enabled`,
ADD COLUMN `register_error` TEXT NULL COMMENT '注册错误信息' AFTER `register_status`;

-- 创建索引以提升查询性能
CREATE INDEX `idx_register_status` ON `t_plugin` (`register_status`);

-- 将现有插件的注册状态设置为未注册
UPDATE `t_plugin`
SET `register_status` = 0, `register_error` = NULL
WHERE `register_status` IS NULL;

-- 查看表结构
-- DESC `t_plugin`;

-- ============================================================
-- 回滚脚本 (如需回滚，请执行以下语句)
-- ============================================================
-- DROP INDEX `idx_register_status` ON `t_plugin`;
-- ALTER TABLE `t_plugin`
-- DROP COLUMN `register_error`,
-- DROP COLUMN `register_status`;
-- ============================================================

