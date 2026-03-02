// Copyright 2026 Arcentra Authors.
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

package git

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/arcentrix/arcentra/pkg/request"
	"github.com/valyala/fasthttp"
)

type repoRef struct {
	Owner string
	Name  string
}

func CreatePullRequest(req CreatePullRequestRequest) (string, error) {
	repoInfo, ok := parseRepoFromURL(req.PipelineRepoURL)
	if !ok {
		return "", fmt.Errorf("invalid repository url: %s", req.PipelineRepoURL)
	}
	token := strings.TrimSpace(req.Credential)
	if req.AuthType != AuthTypeToken || token == "" {
		return "", fmt.Errorf("project token credential is required for PR mode")
	}
	repoType := strings.ToLower(strings.TrimSpace(req.RepoType))
	if strings.TrimSpace(req.ProjectRepoURL) == "" {
		return "", fmt.Errorf("project repository url is empty")
	}

	switch repoType {
	case RepoTypeGitHub:
		var out struct {
			HtmlURL string `json:"html_url"`
		}
		r, err := request.NewRequest(
			"https://api.github.com/repos/"+url.PathEscape(repoInfo.Owner)+"/"+url.PathEscape(repoInfo.Name)+"/pulls",
			fasthttp.MethodPost,
			map[string]string{
				"Accept":        "application/vnd.github+json",
				"Authorization": "Bearer " + token,
			},
			nil,
		).
			WithBodyJSON(map[string]any{
				"title": req.Title,
				"head":  req.SourceBranch,
				"base":  req.TargetBranch,
				"body":  "created by arcentra pipeline editor",
			}).
			WithResult(&out).
			Do()
		if err != nil || isErrorStatus(r) {
			return "", fmt.Errorf("github create pr failed: %v %s", err, safeResponseBody(r))
		}
		return out.HtmlURL, nil
	case RepoTypeGitLab:
		base := strings.TrimSpace(extractBaseURL(req.PipelineRepoURL))
		if base == "" {
			base = "https://gitlab.com"
		}
		var out struct {
			WebURL string `json:"web_url"`
		}
		projectPath := url.PathEscape(repoInfo.Owner + "/" + repoInfo.Name)
		r, err := request.NewRequest(
			strings.TrimRight(base, "/")+"/api/v4/projects/"+projectPath+"/merge_requests",
			fasthttp.MethodPost,
			map[string]string{
				"PRIVATE-TOKEN": token,
			},
			nil,
		).
			WithBodyJSON(map[string]any{
				"source_branch": req.SourceBranch,
				"target_branch": req.TargetBranch,
				"title":         req.Title,
				"description":   "created by arcentra pipeline editor",
			}).
			WithResult(&out).
			Do()
		if err != nil || isErrorStatus(r) {
			return "", fmt.Errorf("gitlab create mr failed: %v %s", err, safeResponseBody(r))
		}
		return out.WebURL, nil
	case RepoTypeGitea:
		base := strings.TrimSpace(extractBaseURL(req.PipelineRepoURL))
		if base == "" {
			return "", fmt.Errorf("gitea base url is required")
		}
		var out struct {
			HtmlURL string `json:"html_url"`
		}
		r, err := request.NewRequest(
			strings.TrimRight(base, "/")+"/api/v1/repos/"+url.PathEscape(repoInfo.Owner)+"/"+url.PathEscape(repoInfo.Name)+"/pulls",
			fasthttp.MethodPost,
			map[string]string{
				"Authorization": "Bearer " + token,
			},
			nil,
		).
			WithBodyJSON(map[string]any{
				"title": req.Title,
				"head":  req.SourceBranch,
				"base":  req.TargetBranch,
				"body":  "created by arcentra pipeline editor",
			}).
			WithResult(&out).
			Do()
		if err != nil || isErrorStatus(r) {
			return "", fmt.Errorf("gitea create pr failed: %v %s", err, safeResponseBody(r))
		}
		return out.HtmlURL, nil
	case RepoTypeGitee:
		var out struct {
			HtmlURL string `json:"html_url"`
		}
		r, err := request.NewRequest(
			"https://gitee.com/api/v5/repos/"+url.PathEscape(repoInfo.Owner)+"/"+url.PathEscape(repoInfo.Name)+"/pulls",
			fasthttp.MethodPost,
			nil,
			nil,
		).
			WithBodyJSON(map[string]any{
				"access_token": token,
				"title":        req.Title,
				"head":         req.SourceBranch,
				"base":         req.TargetBranch,
				"body":         "created by arcentra pipeline editor",
			}).
			WithResult(&out).
			Do()
		if err != nil || isErrorStatus(r) {
			return "", fmt.Errorf("gitee create pr failed: %v %s", err, safeResponseBody(r))
		}
		return out.HtmlURL, nil
	default:
		return "", fmt.Errorf("repo type '%s' does not support api pull request creation", repoType)
	}
}

func parseRepoFromURL(repoURL string) (repoRef, bool) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return repoRef{}, false
	}
	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return repoRef{}, false
		}
		path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return repoRef{}, false
		}
		return repoRef{Owner: parts[len(parts)-2], Name: parts[len(parts)-1]}, true
	}
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.SplitN(repoURL, ":", 2)
		if len(parts) != 2 {
			return repoRef{}, false
		}
		path := strings.Trim(strings.TrimSuffix(parts[1], ".git"), "/")
		sub := strings.Split(path, "/")
		if len(sub) < 2 {
			return repoRef{}, false
		}
		return repoRef{Owner: sub[len(sub)-2], Name: sub[len(sub)-1]}, true
	}
	return repoRef{}, false
}

func extractBaseURL(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return ""
		}
		return u.Scheme + "://" + u.Host
	}
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.SplitN(repoURL, ":", 2)
		if len(parts) != 2 {
			return ""
		}
		host := strings.TrimPrefix(parts[0], "git@")
		if host == "" {
			return ""
		}
		return "https://" + host
	}
	return ""
}

func isErrorStatus(r *fasthttp.Response) bool {
	if r == nil {
		return true
	}
	return r.StatusCode() >= 400
}

func safeResponseBody(r *fasthttp.Response) string {
	if r == nil {
		return ""
	}
	body := strings.TrimSpace(string(r.Body()))
	if len(body) > 256 {
		return body[:256]
	}
	return body
}
