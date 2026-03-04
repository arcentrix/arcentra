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

-- Notification Channels Table
CREATE TABLE IF NOT EXISTS `notification_channels` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `channel_id` varchar(100) NOT NULL COMMENT 'Unique channel identifier',
  `name` varchar(200) NOT NULL COMMENT 'Channel name',
  `type` varchar(50) NOT NULL COMMENT 'Channel type (feishu_app/dingtalk/slack/etc)',
  `config` text NOT NULL COMMENT 'Channel configuration (JSON)',
  `auth_config` text DEFAULT NULL COMMENT 'Authentication configuration (JSON, optional)',
  `description` text DEFAULT NULL COMMENT 'Channel description',
  `is_active` tinyint(1) DEFAULT 1 COMMENT 'Active status',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_channel_id` (`channel_id`),
  KEY `idx_type` (`type`),
  KEY `idx_is_active` (`is_active`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Notification channel configurations';
