// Copyright 2025 Arcentra Team
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
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

type IProjectMemberRepository interface {
	GetProjectMember(projectId, userId string) (*model.ProjectMember, error)
	ListProjectMembers(projectId string) ([]model.ProjectMember, error)
	AddProjectMember(member *model.ProjectMember) error
	UpdateProjectMemberRole(projectId, userId, role string) error
	RemoveProjectMember(projectId, userId string) error
	GetUserProjects(userId string) ([]model.ProjectMember, error)
}

type ProjectMemberRepo struct {
	database.IDatabase
}

func NewProjectMemberRepo(db database.IDatabase) IProjectMemberRepository {
	return &ProjectMemberRepo{IDatabase: db}
}

// GetProjectMember 获取项目成员
func (r *ProjectMemberRepo) GetProjectMember(projectId, userId string) (*model.ProjectMember, error) {
	var member model.ProjectMember
	err := r.Database().Select("id", "project_id", "user_id", "role_id", "created_at", "updated_at").
		Where("project_id = ? AND user_id = ?", projectId, userId).First(&member).Error
	return &member, err
}

// ListProjectMembers 列出项目成员
func (r *ProjectMemberRepo) ListProjectMembers(projectId string) ([]model.ProjectMember, error) {
	var members []model.ProjectMember
	err := r.Database().Select("id", "project_id", "user_id", "role_id", "created_at", "updated_at").
		Where("project_id = ?", projectId).Find(&members).Error
	return members, err
}

// AddProjectMember 添加项目成员
func (r *ProjectMemberRepo) AddProjectMember(member *model.ProjectMember) error {
	return r.Database().Create(member).Error
}

// UpdateProjectMemberRole 更新项目成员角色
func (r *ProjectMemberRepo) UpdateProjectMemberRole(projectId, userId, role string) error {
	return r.Database().Model(&model.ProjectMember{}).
		Where("project_id = ? AND user_id = ?", projectId, userId).
		Update("role", role).Error
}

// RemoveProjectMember 移除项目成员
func (r *ProjectMemberRepo) RemoveProjectMember(projectId, userId string) error {
	return r.Database().Where("project_id = ? AND user_id = ?", projectId, userId).
		Delete(&model.ProjectMember{}).Error
}

// GetUserProjects 获取用户的所有项目
func (r *ProjectMemberRepo) GetUserProjects(userId string) ([]model.ProjectMember, error) {
	var members []model.ProjectMember
	err := r.Database().Select("id", "project_id", "user_id", "role_id", "created_at", "updated_at").
		Where("user_id = ?", userId).Find(&members).Error
	return members, err
}
