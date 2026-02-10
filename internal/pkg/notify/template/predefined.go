// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package template

import "context"

// PredefinedTemplates contains commonly used notification templates
var PredefinedTemplates = []*Template{
	// Build Templates
	{
		ID:      "build_success",
		Name:    "Build Success",
		Type:    TemplateTypeBuild,
		Channel: "all",
		Title:   "Build Successful",
		Content: `✅ **Build Success**

Project: {{.project_name}}
Branch: {{.branch}}
Commit: {{.commit_id}}
Build Number: {{.build_number}}
Duration: {{.duration}}
Triggered By: {{.triggered_by}}

Build completed successfully!`,
		Format:      "markdown",
		Description: "Notification for successful build",
	},
	{
		ID:      "build_failed",
		Name:    "Build Failed",
		Type:    TemplateTypeBuild,
		Channel: "all",
		Title:   "Build Failed",
		Content: `❌ **Build Failed**

Project: {{.project_name}}
Branch: {{.branch}}
Commit: {{.commit_id}}
Build Number: {{.build_number}}
Duration: {{.duration}}
Triggered By: {{.triggered_by}}
Error: {{.error_message}}

Please check the build logs for details.`,
		Format:      "markdown",
		Description: "Notification for failed build",
	},
	{
		ID:      "build_started",
		Name:    "Build Started",
		Type:    TemplateTypeBuild,
		Channel: "all",
		Title:   "Build Started",
		Content: `🚀 **Build Started**

Project: {{.project_name}}
Branch: {{.branch}}
Commit: {{.commit_id}}
Build Number: {{.build_number}}
Triggered By: {{.triggered_by}}

Build is now in progress...`,
		Format:      "markdown",
		Description: "Notification when build starts",
	},

	// Approval Templates
	{
		ID:      "approval_pending",
		Name:    "Approval Pending",
		Type:    TemplateTypeApproval,
		Channel: "all",
		Title:   "Approval Required",
		Content: `📋 **Approval Required**

Title: {{.approval_title}}
Type: {{.approval_type}}
Requester: {{.requester}}
Created: {{.created_at}}
Description: {{.description}}

Please review and approve this request.`,
		Format:      "markdown",
		Description: "Notification for pending approval",
	},
	{
		ID:      "approval_approved",
		Name:    "Approval Approved",
		Type:    TemplateTypeApproval,
		Channel: "all",
		Title:   "Approval Approved",
		Content: `✅ **Approval Approved**

Title: {{.approval_title}}
Type: {{.approval_type}}
Approved By: {{.approver}}
Approved At: {{.approved_at}}
Comment: {{.comment}}

Your approval request has been approved.`,
		Format:      "markdown",
		Description: "Notification when approval is approved",
	},
	{
		ID:      "approval_rejected",
		Name:    "Approval Rejected",
		Type:    TemplateTypeApproval,
		Channel: "all",
		Title:   "Approval Rejected",
		Content: `❌ **Approval Rejected**

Title: {{.approval_title}}
Type: {{.approval_type}}
Rejected By: {{.rejector}}
Rejected At: {{.rejected_at}}
Reason: {{.reason}}

Your approval request has been rejected.`,
		Format:      "markdown",
		Description: "Notification when approval is rejected",
	},

	// DingTalk specific templates
	{
		ID:      "dingtalk_build_success",
		Name:    "DingTalk Build Success",
		Type:    TemplateTypeBuild,
		Channel: "dingtalk",
		Title:   "构建成功",
		Content: `✅ 构建成功

项目：{{.project_name}}
分支：{{.branch}}
提交：{{.commit_id}}
构建号：{{.build_number}}
耗时：{{.duration}}
触发者：{{.triggered_by}}

构建已成功完成！`,
		Format:      "text",
		Description: "DingTalk specific build success template",
	},

	// Feishu specific templates
	{
		ID:      "feishu_approval_pending",
		Name:    "Feishu Approval Pending",
		Type:    TemplateTypeApproval,
		Channel: "feishu",
		Title:   "审批待处理",
		Content: `📋 **审批待处理**

标题：{{.approval_title}}
类型：{{.approval_type}}
申请人：{{.requester}}
创建时间：{{.created_at}}
描述：{{.description}}

请及时审批。`,
		Format:      "markdown",
		Description: "Feishu specific approval pending template",
	},
}

// InitializePredefinedTemplates initializes predefined templates in the repository
func InitializePredefinedTemplates(ctx context.Context, service *TemplateService) error {
	for _, tmpl := range PredefinedTemplates {
		if err := service.CreateTemplate(ctx, tmpl); err != nil {
			// Ignore if template already exists
			continue
		}
	}
	return nil
}
