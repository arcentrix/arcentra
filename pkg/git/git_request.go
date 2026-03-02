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

import "strings"

type GitAuth struct {
	Username string
	Token    string
	SSHKey   string
}

func NewGitAuthFromMap(auth map[string]string) GitAuth {
	return GitAuth{
		Username: strings.TrimSpace(auth["username"]),
		Token:    strings.TrimSpace(auth["token"]),
		SSHKey:   strings.TrimSpace(auth["ssh_key"]),
	}
}

type GitCloneRequest struct {
	Workdir string
	RepoURL string
	Branch  string
	Auth    GitAuth
}

type GitHeadSHARequest struct {
	Workdir string
}

type GitAddRequest struct {
	Workdir  string
	FilePath string
}

type GitCommitRequest struct {
	Workdir string
	Message string
	Author  string
}

type GitCheckoutBranchRequest struct {
	Workdir string
	Branch  string
}

type GitPushRequest struct {
	Workdir string
	Remote  string
	Branch  string
	Auth    GitAuth
}

const (
	AuthTypeToken = 2
)

const (
	RepoTypeGitHub = "github"
	RepoTypeGitLab = "gitlab"
	RepoTypeGitee  = "gitee"
	RepoTypeGitea  = "gitea"
)

type CreatePullRequestRequest struct {
	RepoType        string
	AuthType        int
	Credential      string
	ProjectRepoURL  string
	PipelineRepoURL string
	TargetBranch    string
	SourceBranch    string
	Title           string
}
