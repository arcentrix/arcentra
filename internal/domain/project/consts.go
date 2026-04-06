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

package project

// ProjectStatus represents the lifecycle state of a project.
type ProjectStatus int

const (
	ProjectStatusInactive ProjectStatus = 0
	ProjectStatusActive   ProjectStatus = 1
	ProjectStatusArchived ProjectStatus = 2
	ProjectStatusDisabled ProjectStatus = 3
)

func (s ProjectStatus) String() string {
	switch s {
	case ProjectStatusActive:
		return "active"
	case ProjectStatusArchived:
		return "archived"
	case ProjectStatusDisabled:
		return "disabled"
	default:
		return "inactive"
	}
}

// ProjectVisibility controls who can see the project.
type ProjectVisibility int

const (
	VisibilityPrivate  ProjectVisibility = 0
	VisibilityInternal ProjectVisibility = 1
	VisibilityPublic   ProjectVisibility = 2
)

// AuthType represents the authentication method for a project's repository.
type AuthType int

const (
	AuthTypeNone     AuthType = 0
	AuthTypePassword AuthType = 1
	AuthTypeToken    AuthType = 2
	AuthTypeSSHKey   AuthType = 3
)

// Trigger mode bitmask values.
const (
	TriggerModeManual   = 1 << 0
	TriggerModeWebhook  = 1 << 1
	TriggerModeSchedule = 1 << 2
	TriggerModePush     = 1 << 3
	TriggerModeMR       = 1 << 4
	TriggerModeTag      = 1 << 5
)

// Repository type identifiers.
const (
	RepoTypeGit       = "git"
	RepoTypeGitHub    = "github"
	RepoTypeGitLab    = "gitlab"
	RepoTypeGitee     = "gitee"
	RepoTypeBitbucket = "bitbucket"
	RepoTypeGitea     = "gitea"
	RepoTypeSVN       = "svn"
)

// TeamAccessLevel controls the permission level of a team on a project.
type TeamAccessLevel string

const (
	TeamAccessRead  TeamAccessLevel = "read"
	TeamAccessWrite TeamAccessLevel = "write"
	TeamAccessAdmin TeamAccessLevel = "admin"
)

// Project member role identifiers.
const (
	ProjectRoleOwner      = "owner"
	ProjectRoleMaintainer = "maintainer"
	ProjectRoleDeveloper  = "developer"
	ProjectRoleReporter   = "reporter"
	ProjectRoleGuest      = "guest"
)
