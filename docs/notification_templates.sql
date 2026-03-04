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

-- Notification Templates Table
CREATE TABLE IF NOT EXISTS `notification_templates` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `template_id` varchar(100) NOT NULL COMMENT 'Unique template identifier',
  `name` varchar(200) NOT NULL COMMENT 'Template name',
  `type` varchar(50) NOT NULL COMMENT 'Template type (build/approval)',
  `channel` varchar(50) NOT NULL COMMENT 'Target channel (dingtalk/feishu/slack/etc)',
  `title` varchar(200) DEFAULT NULL COMMENT 'Template title',
  `content` text NOT NULL COMMENT 'Template content with variables',
  `variables` text DEFAULT NULL COMMENT 'Required variables (JSON array)',
  `format` varchar(50) DEFAULT 'markdown' COMMENT 'Message format (text/markdown/html)',
  `metadata` text DEFAULT NULL COMMENT 'Additional metadata (JSON)',
  `description` text DEFAULT NULL COMMENT 'Template description',
  `is_active` tinyint(1) DEFAULT 1 COMMENT 'Active status',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_template_id` (`template_id`),
  KEY `idx_type` (`type`),
  KEY `idx_channel` (`channel`),
  KEY `idx_is_active` (`is_active`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Notification templates';

-- Notification Logs Table
CREATE TABLE IF NOT EXISTS `notification_logs` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `template_id` varchar(100) DEFAULT NULL COMMENT 'Template ID used',
  `channel` varchar(50) NOT NULL COMMENT 'Channel name',
  `recipient` varchar(500) DEFAULT NULL COMMENT 'Recipient information',
  `content` text DEFAULT NULL COMMENT 'Rendered content',
  `status` varchar(50) NOT NULL COMMENT 'Status (success/failed)',
  `error_msg` text DEFAULT NULL COMMENT 'Error message if failed',
  `metadata` text DEFAULT NULL COMMENT 'Additional metadata (JSON)',
  `sent_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'Sent timestamp',
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_template_id` (`template_id`),
  KEY `idx_channel` (`channel`),
  KEY `idx_status` (`status`),
  KEY `idx_sent_at` (`sent_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Notification sending logs';

-- Insert predefined templates
INSERT INTO `notification_templates` (`template_id`, `name`, `type`, `channel`, `title`, `content`, `format`, `description`, `is_active`) VALUES
('build_success', 'Build Success', 'build', 'all', 'Build Successful', '✅ **Build Success**\n\nProject: {{.project_name}}\nBranch: {{.branch}}\nCommit: {{.commit_id}}\nBuild Number: {{.build_number}}\nDuration: {{.duration}}\nTriggered By: {{.triggered_by}}\n\nBuild completed successfully!', 'markdown', 'Notification for successful build', 1),
('build_failed', 'Build Failed', 'build', 'all', 'Build Failed', '❌ **Build Failed**\n\nProject: {{.project_name}}\nBranch: {{.branch}}\nCommit: {{.commit_id}}\nBuild Number: {{.build_number}}\nDuration: {{.duration}}\nTriggered By: {{.triggered_by}}\nError: {{.error_message}}\n\nPlease check the build logs for details.', 'markdown', 'Notification for failed build', 1),
('build_started', 'Build Started', 'build', 'all', 'Build Started', '🚀 **Build Started**\n\nProject: {{.project_name}}\nBranch: {{.branch}}\nCommit: {{.commit_id}}\nBuild Number: {{.build_number}}\nTriggered By: {{.triggered_by}}\n\nBuild is now in progress...', 'markdown', 'Notification when build starts', 1),
('approval_pending', 'Approval Pending', 'approval', 'all', 'Approval Required', '📋 **Approval Required**\n\nTitle: {{.approval_title}}\nType: {{.approval_type}}\nRequester: {{.requester}}\nCreated: {{.created_at}}\nDescription: {{.description}}\n\nPlease review and approve this request.', 'markdown', 'Notification for pending approval', 1),
('approval_approved', 'Approval Approved', 'approval', 'all', 'Approval Approved', '✅ **Approval Approved**\n\nTitle: {{.approval_title}}\nType: {{.approval_type}}\nApproved By: {{.approver}}\nApproved At: {{.approved_at}}\nComment: {{.comment}}\n\nYour approval request has been approved.', 'markdown', 'Notification when approval is approved', 1),
('approval_rejected', 'Approval Rejected', 'approval', 'all', 'Approval Rejected', '❌ **Approval Rejected**\n\nTitle: {{.approval_title}}\nType: {{.approval_type}}\nRejected By: {{.rejector}}\nRejected At: {{.rejected_at}}\nReason: {{.reason}}\n\nYour approval request has been rejected.', 'markdown', 'Notification when approval is rejected', 1);

