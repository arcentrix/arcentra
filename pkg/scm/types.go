package scm

import (
	"time"
)

type ProviderKind string

const (
	ProviderKindGitHub    ProviderKind = "github"
	ProviderKindGitLab    ProviderKind = "gitlab"
	ProviderKindBitbucket ProviderKind = "bitbucket"
	ProviderKindGitee     ProviderKind = "gitee"
	ProviderKindGitea     ProviderKind = "gitea"
)

type EventType string

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
	Url      string `json:"url,omitempty"`
}

// Change describes a PR/MR change in a normalized form.
type Change struct {
	Number        int    `json:"number,omitempty"`
	Title         string `json:"title,omitempty"`
	SourceBranch  string `json:"sourceBranch,omitempty"`
	TargetBranch  string `json:"targetBranch,omitempty"`
	State         string `json:"state,omitempty"`
	IsMerged      bool   `json:"isMerged,omitempty"`
	MergeCommitId string `json:"mergeCommitId,omitempty"`
}

// Event is a normalized SCM event.
type Event struct {
	ProviderKind ProviderKind   `json:"providerKind"`
	EventType    EventType      `json:"eventType"`
	Repo         Repo           `json:"repo"`
	ActorName    string         `json:"actorName,omitempty"`
	CommitId     string         `json:"commitId,omitempty"`
	Ref          string         `json:"ref,omitempty"`
	OccurredAt   time.Time      `json:"occurredAt"`
	Change       *Change        `json:"change,omitempty"`
	Raw          map[string]any `json:"raw,omitempty"`
}

type Capability string

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
