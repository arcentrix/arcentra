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

// IProjectMemberRepository defines project member persistence with context support.
type IProjectMemberRepository interface {
	Get(ctx context.Context, projectId, userId string) (*model.ProjectMember, error)
	ListProjectMembers(ctx context.Context, projectId string) ([]model.ProjectMember, error)
	AddProjectMember(ctx context.Context, member *model.ProjectMember) error
	UpdateProjectMemberRole(ctx context.Context, projectId, userId, role string) error
	RemoveProjectMember(ctx context.Context, projectId, userId string) error
	GetUserProjects(ctx context.Context, userId string) ([]model.ProjectMember, error)
}

type ProjectMemberRepo struct {
	database.IDatabase
}

func NewProjectMemberRepo(db database.IDatabase) IProjectMemberRepository {
	return &ProjectMemberRepo{IDatabase: db}
}

// Get returns project member by projectId and userId.
func (r *ProjectMemberRepo) Get(ctx context.Context, projectId, userId string) (*model.ProjectMember, error) {
	var member model.ProjectMember
	err := r.Database().WithContext(ctx).Select("id", "project_id", "user_id", "role_id", "created_at", "updated_at").
		Where("project_id = ? AND user_id = ?", projectId, userId).First(&member).Error
	return &member, err
}

// ListProjectMembers lists project members.
func (r *ProjectMemberRepo) ListProjectMembers(ctx context.Context, projectId string) ([]model.ProjectMember, error) {
	var members []model.ProjectMember
	err := r.Database().WithContext(ctx).Select("id", "project_id", "user_id", "role_id", "created_at", "updated_at").
		Where("project_id = ?", projectId).Find(&members).Error
	return members, err
}

// AddProjectMember adds a project member.
func (r *ProjectMemberRepo) AddProjectMember(ctx context.Context, member *model.ProjectMember) error {
	return r.Database().WithContext(ctx).Create(member).Error
}

// UpdateProjectMemberRole updates project member role.
func (r *ProjectMemberRepo) UpdateProjectMemberRole(ctx context.Context, projectId, userId, role string) error {
	return r.Database().WithContext(ctx).Model(&model.ProjectMember{}).
		Where("project_id = ? AND user_id = ?", projectId, userId).
		Update("role", role).Error
}

// RemoveProjectMember removes a project member.
func (r *ProjectMemberRepo) RemoveProjectMember(ctx context.Context, projectId, userId string) error {
	return r.Database().WithContext(ctx).Where("project_id = ? AND user_id = ?", projectId, userId).
		Delete(&model.ProjectMember{}).Error
}

// GetUserProjects returns user's projects.
func (r *ProjectMemberRepo) GetUserProjects(ctx context.Context, userId string) ([]model.ProjectMember, error) {
	var members []model.ProjectMember
	err := r.Database().WithContext(ctx).Select("id", "project_id", "user_id", "role_id", "created_at", "updated_at").
		Where("user_id = ?", userId).Find(&members).Error
	return members, err
}
