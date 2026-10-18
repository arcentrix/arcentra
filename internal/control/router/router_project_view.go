// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/arcentrix/arcentra/internal/control/model"
)

// Project reads are metadata APIs, never configuration or secret export APIs.
// Keep this allowlist independent of persistence-model additions.
func publicProject(p *model.Project) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"projectId": p.ProjectID, "orgId": p.OrgID, "name": p.Name, "displayName": p.DisplayName,
		"namespace": p.Namespace, "description": p.Description, "repoUrl": publicProjectURL(p.RepoURL, true),
		"repoType": p.RepoType, "defaultBranch": p.DefaultBranch, "tags": p.Tags, "language": p.Language,
		"framework": p.Framework, "status": p.Status, "visibility": p.Visibility, "accessLevel": p.AccessLevel,
		"createdBy": p.CreatedBy, "isEnabled": p.IsEnabled, "icon": publicProjectURL(p.Icon, false),
		"homepage": publicProjectURL(p.Homepage, false), "totalPipelines": p.TotalPipelines,
		"totalBuilds": p.TotalBuilds, "successBuilds": p.SuccessBuilds, "failedBuilds": p.FailedBuilds,
	}
}
func publicProjects(projects []*model.Project) []map[string]any {
	result := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		if p != nil {
			result = append(result, publicProject(p))
		}
	}
	return result
}

// Display URLs omit userinfo, query, and fragment. Unparseable or opaque values
// are withheld instead of reflecting possible credentials into any response.
func publicProjectURL(value string, repository bool) string {
	for _, r := range value {
		if unicode.IsControl(r) {
			return ""
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Hostname() == "" {
		return ""
	}
	allowed := parsed.Scheme == "https" || parsed.Scheme == "http"
	if repository {
		allowed = allowed || parsed.Scheme == "git" || parsed.Scheme == "ssh"
	}
	if !allowed {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if strings.Contains(parsed.Host, "@") {
		return ""
	}
	return parsed.String()
}
