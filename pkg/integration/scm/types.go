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

package scm

import (
	"time"
)

type ProviderKind string

// Supported SCM providers.
const (
	ProviderKindGitHub    ProviderKind = "github"
	ProviderKindGitLab    ProviderKind = "gitlab"
	ProviderKindBitbucket ProviderKind = "bitbucket"
	ProviderKindGitee     ProviderKind = "gitee"
	ProviderKindGitea     ProviderKind = "gitea"
)

type EventType string

// Normalized SCM event types.
const (
	EventTypePush               EventType = "push"
	EventTypeTag                EventType = "tag"
	EventTypePullRequest        EventType = "pull_request"
	EventTypeMergeRequest       EventType = "merge_request"
	EventTypePullMerged         EventType = "pull_merged"
	EventTypeMergeRequestMerged EventType = "merge_request_merged"
)

// Repo identifies a repository in a provider-agnostic way.
type Repo struct {
	Host     string `json:"host"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	FullName string `json:"fullName,omitempty"`
	URL      string `json:"url,omitempty"`
}

// Change describes a PR/MR change in a normalized form.
type Change struct {
	Number        int    `json:"number,omitempty"`
	Title         string `json:"title,omitempty"`
	SourceBranch  string `json:"sourceBranch,omitempty"`
	TargetBranch  string `json:"targetBranch,omitempty"`
	State         string `json:"state,omitempty"`
	IsMerged      bool   `json:"isMerged,omitempty"`
	MergeCommitID string `json:"mergeCommitId,omitempty"`
}

// Event is a normalized SCM event.
type Event struct {
	ProviderKind ProviderKind   `json:"providerKind"`
	EventType    EventType      `json:"eventType"`
	Repo         Repo           `json:"repo"`
	ActorName    string         `json:"actorName,omitempty"`
	CommitID     string         `json:"commitId,omitempty"`
	Ref          string         `json:"ref,omitempty"`
	OccurredAt   time.Time      `json:"occurredAt"`
	Change       *Change        `json:"change,omitempty"`
	Raw          map[string]any `json:"raw,omitempty"`
}

type Capability string

// Provider capabilities.
const (
	CapWebhookVerify Capability = "webhook.verify"
	CapWebhookParse  Capability = "webhook.parse"
	CapPollEvents    Capability = "events.poll"
)

type CapSet map[Capability]bool

// Has reports whether the capability is enabled in the set.
func (s CapSet) Has(c Capability) bool {
	if s == nil {
		return false
	}
	return s[c]
}

// Cursor stores provider-specific progress for polling.
type Cursor struct {
	Since  time.Time `json:"since"`
	Opaque string    `json:"opaque,omitempty"`
}

// Auth types.
const (
	AuthTypeToken = 2
)

type GitAuth struct {
	Username string
	Token    string
	Password string
	SSHKey   string
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

type ChangeRequestInput struct {
	RepoType        string
	AuthType        int
	Credential      string
	ProjectRepoURL  string
	PipelineRepoURL string
	TargetBranch    string
	SourceBranch    string
	Title           string
}
