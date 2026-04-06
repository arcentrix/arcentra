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

import "context"

// IProjectRepository defines persistence operations for Project entities.
type IProjectRepository interface {
	Create(ctx context.Context, p *Project) error
	Get(ctx context.Context, projectID string) (*Project, error)
	GetByName(ctx context.Context, orgID, name string) (*Project, error)
	Update(ctx context.Context, projectID string, updates map[string]any) error
	Delete(ctx context.Context, projectID string) error
	List(ctx context.Context, orgID string, page, size int, status *ProjectStatus) ([]*Project, int64, error)
	ListByUser(ctx context.Context, userID string, page, size int) ([]*Project, int64, error)
	Exists(ctx context.Context, projectID string) (bool, error)
	NameExists(ctx context.Context, orgID, name string, excludeProjectID ...string) (bool, error)
	UpdateStatistics(ctx context.Context, projectID string, totalPipelines, totalBuilds, successBuilds, failedBuilds *int) error
	Enable(ctx context.Context, projectID string) error
	Disable(ctx context.Context, projectID string) error
}

// IProjectMemberRepository defines persistence operations for ProjectMember entities.
type IProjectMemberRepository interface {
	Get(ctx context.Context, projectID, userID string) (*ProjectMember, error)
	ListByProject(ctx context.Context, projectID string) ([]ProjectMember, error)
	ListByUser(ctx context.Context, userID string) ([]ProjectMember, error)
	Add(ctx context.Context, member *ProjectMember) error
	UpdateRole(ctx context.Context, projectID, userID, roleID string) error
	Remove(ctx context.Context, projectID, userID string) error
}

// IProjectTeamAccessRepository defines persistence operations for ProjectTeamAccess entities.
type IProjectTeamAccessRepository interface {
	Get(ctx context.Context, projectID, teamID string) (*ProjectTeamAccess, error)
	ListByProject(ctx context.Context, projectID string) ([]ProjectTeamAccess, error)
	ListByTeam(ctx context.Context, teamID string) ([]ProjectTeamAccess, error)
	Grant(ctx context.Context, access *ProjectTeamAccess) error
	UpdateLevel(ctx context.Context, projectID, teamID string, level TeamAccessLevel) error
	Revoke(ctx context.Context, projectID, teamID string) error
}

// ISecretRepository defines persistence operations for Secret entities.
type ISecretRepository interface {
	Create(ctx context.Context, secret *Secret) error
	Update(ctx context.Context, secret *Secret) error
	Get(ctx context.Context, secretID string) (*Secret, error)
	List(ctx context.Context, page, size int, secretType, scope, scopeID, createdBy string) ([]*Secret, int64, error)
	Delete(ctx context.Context, secretID string) error
	ListByScope(ctx context.Context, scope, scopeID string) ([]*Secret, error)
	GetValue(ctx context.Context, secretID string) (string, error)
}

// IGeneralSettingsRepository defines persistence operations for GeneralSettings entities.
type IGeneralSettingsRepository interface {
	Update(ctx context.Context, settings *GeneralSettings) error
	Get(ctx context.Context, settingsID string) (*GeneralSettings, error)
	GetByName(ctx context.Context, category, name string) (*GeneralSettings, error)
	List(ctx context.Context, page, size int, category string) ([]*GeneralSettings, int64, error)
	GetCategories(ctx context.Context) ([]string, error)
}
