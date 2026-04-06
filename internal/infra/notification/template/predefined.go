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

import (
	"context"

	"github.com/arcentrix/arcentra/internal/domain/notification"
)

// PredefinedTemplates contains commonly used notification templates.
var PredefinedTemplates = []*notification.Template{
	{
		ID:      "build_success",
		Name:    "Build Success",
		Type:    notification.TemplateBuild,
		Channel: "all",
		Title:   "Build Successful",
		Content: "✅ **Build Success**\n\n" +
			"Project: {{.project_name}}\nBranch: {{.branch}}\nCommit: {{.commit_id}}\n" +
			"Build Number: {{.build_number}}\nDuration: {{.duration}}\n" +
			"Triggered By: {{.triggered_by}}\n\nBuild completed successfully!",
		Format: "markdown",
	},
	{
		ID:      "build_failed",
		Name:    "Build Failed",
		Type:    notification.TemplateBuild,
		Channel: "all",
		Title:   "Build Failed",
		Content: "❌ **Build Failed**\n\n" +
			"Project: {{.project_name}}\nBranch: {{.branch}}\nCommit: {{.commit_id}}\n" +
			"Build Number: {{.build_number}}\nDuration: {{.duration}}\n" +
			"Triggered By: {{.triggered_by}}\nError: {{.error_message}}\n\n" +
			"Please check the build logs for details.",
		Format: "markdown",
	},
	{
		ID:      "build_started",
		Name:    "Build Started",
		Type:    notification.TemplateBuild,
		Channel: "all",
		Title:   "Build Started",
		Content: "🚀 **Build Started**\n\n" +
			"Project: {{.project_name}}\nBranch: {{.branch}}\nCommit: {{.commit_id}}\n" +
			"Build Number: {{.build_number}}\nTriggered By: {{.triggered_by}}\n\n" +
			"Build is now in progress...",
		Format: "markdown",
	},
	{
		ID:      "approval_pending",
		Name:    "Approval Pending",
		Type:    notification.TemplateApproval,
		Channel: "all",
		Title:   "Approval Required",
		Content: "📋 **Approval Required**\n\n" +
			"Title: {{.approval_title}}\nType: {{.approval_type}}\n" +
			"Requester: {{.requester}}\nCreated: {{.created_at}}\n" +
			"Description: {{.description}}\n\nPlease review and approve this request.",
		Format: "markdown",
	},
}

// InitializePredefinedTemplates initializes predefined templates in the repository.
func InitializePredefinedTemplates(ctx context.Context, service *Service) error {
	for _, tmpl := range PredefinedTemplates {
		if err := service.CreateTemplate(ctx, tmpl); err != nil {
			continue
		}
	}
	return nil
}
