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

package repo

import (
	"context"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// IProjectTeamAccessRepository defines project team access persistence with context support.
type IProjectTeamAccessRepository interface {
	Get(ctx context.Context, projectId, teamId string) (*model.ProjectTeamAccess, error)
	ListProjectTeams(ctx context.Context, projectId string) ([]model.ProjectTeamAccess, error)
	ListTeamProjects(ctx context.Context, teamId string) ([]model.ProjectTeamAccess, error)
	GrantTeamAccess(ctx context.Context, access *model.ProjectTeamAccess) error
	UpdateTeamAccessLevel(ctx context.Context, projectId, teamId, accessLevel string) error
	RevokeTeamAccess(ctx context.Context, projectId, teamId string) error
}

type ProjectTeamAccessRepo struct {
	database.IDatabase
}

func NewProjectTeamAccessRepo(db database.IDatabase) IProjectTeamAccessRepository {
	return &ProjectTeamAccessRepo{IDatabase: db}
}

// Get returns project team access by projectId and teamId.
func (r *ProjectTeamAccessRepo) Get(ctx context.Context, projectId, teamId string) (*model.ProjectTeamAccess, error) {
	var access model.ProjectTeamAccess
	err := r.Database().WithContext(ctx).Select("id", "project_id", "team_id", "access_level", "created_at", "updated_at").
		Where("project_id = ? AND team_id = ?", projectId, teamId).First(&access).Error
	return &access, err
}

// ListProjectTeams lists project teams.
func (r *ProjectTeamAccessRepo) ListProjectTeams(ctx context.Context, projectId string) ([]model.ProjectTeamAccess, error) {
	var accesses []model.ProjectTeamAccess
	err := r.Database().WithContext(ctx).Select("id", "project_id", "team_id", "access_level", "created_at", "updated_at").
		Where("project_id = ?", projectId).Find(&accesses).Error
	return accesses, err
}

// ListTeamProjects lists team projects.
func (r *ProjectTeamAccessRepo) ListTeamProjects(ctx context.Context, teamId string) ([]model.ProjectTeamAccess, error) {
	var accesses []model.ProjectTeamAccess
	err := r.Database().WithContext(ctx).Select("id", "project_id", "team_id", "access_level", "created_at", "updated_at").
		Where("team_id = ?", teamId).Find(&accesses).Error
	return accesses, err
}

// GrantTeamAccess grants team access.
func (r *ProjectTeamAccessRepo) GrantTeamAccess(ctx context.Context, access *model.ProjectTeamAccess) error {
	return r.Database().WithContext(ctx).Create(access).Error
}

// UpdateTeamAccessLevel updates team access level.
func (r *ProjectTeamAccessRepo) UpdateTeamAccessLevel(ctx context.Context, projectId, teamId, accessLevel string) error {
	return r.Database().WithContext(ctx).Model(&model.ProjectTeamAccess{}).
		Where("project_id = ? AND team_id = ?", projectId, teamId).
		Update("access_level", accessLevel).Error
}

// RevokeTeamAccess revokes team access.
func (r *ProjectTeamAccessRepo) RevokeTeamAccess(ctx context.Context, projectId, teamId string) error {
	return r.Database().WithContext(ctx).Where("project_id = ? AND team_id = ?", projectId, teamId).
		Delete(&model.ProjectTeamAccess{}).Error
}
