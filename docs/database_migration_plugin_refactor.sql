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

-- ==========================================
-- 插件系统重构 - 数据库迁移脚本
-- ==========================================
-- 版本: v1.0.0
-- 日期: 2025-01-16
-- 描述: 为插件系统添加新字段以支持完整的生命周期管理
-- ==========================================

-- 1. 添加新字段到 t_plugin 表
ALTER TABLE t_plugin
ADD COLUMN source VARCHAR(20) DEFAULT 'local' COMMENT '插件来源: local/market' AFTER checksum,
ADD COLUMN s3_path VARCHAR(500) DEFAULT '' COMMENT 'S3存储路径' AFTER source,
ADD COLUMN manifest JSON DEFAULT NULL COMMENT '插件清单' AFTER s3_path,
ADD COLUMN install_time DATETIME DEFAULT NULL COMMENT '安装时间' AFTER manifest,
ADD COLUMN update_time DATETIME DEFAULT NULL COMMENT '更新时间' AFTER install_time;

-- 2. 更新 is_enabled 字段注释（支持错误状态）
ALTER TABLE t_plugin MODIFY COLUMN is_enabled INT DEFAULT 1 COMMENT '状态: 0-禁用 1-启用 2-错误';

-- 2.1 移除 install_path 字段（本地路径改为动态生成，不再存储在数据库）
-- 说明：本地路径格式为 {localCacheDir}/{plugin_id}_{version}.so
-- 如果确认不再需要 install_path 字段，可以执行以下语句：
-- ALTER TABLE t_plugin DROP COLUMN install_path;
-- 注意：建议先备份数据，确认系统正常运行后再执行删除操作

-- 3. 更新现有数据（如果有旧数据需要迁移）
-- 为所有插件设置来源（所有插件都是下载安装的，没有内置插件）
-- ==========================================
-- 回滚脚本（如需回滚，请谨慎使用）
-- ==========================================


