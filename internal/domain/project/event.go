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

import "time"

// ProjectCreated is raised when a new project is created.
type ProjectCreated struct {
	ProjectID  string
	OrgID      string
	Name       string
	OccurredAt time.Time
}

func (e ProjectCreated) EventType() string { return "project.created" }

// ProjectStatusChanged is raised when a project's status changes.
type ProjectStatusChanged struct {
	ProjectID  string
	OldStatus  ProjectStatus
	NewStatus  ProjectStatus
	OccurredAt time.Time
}

func (e ProjectStatusChanged) EventType() string { return "project.status_changed" }

// ProjectMemberAdded is raised when a user is added to a project.
type ProjectMemberAdded struct {
	ProjectID  string
	UserID     string
	RoleID     string
	OccurredAt time.Time
}

func (e ProjectMemberAdded) EventType() string { return "project.member_added" }

// SecretCreated is raised when a new secret is created.
type SecretCreated struct {
	SecretID   string
	Scope      string
	ScopeID    string
	OccurredAt time.Time
}

func (e SecretCreated) EventType() string { return "project.secret_created" }
