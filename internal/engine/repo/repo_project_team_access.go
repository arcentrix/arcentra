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
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

type IProjectTeamAccessRepository interface {
	GetProjectTeamAccess(projectId, teamId string) (*model.ProjectTeamAccess, error)
	ListProjectTeams(projectId string) ([]model.ProjectTeamAccess, error)
	ListTeamProjects(teamId string) ([]model.ProjectTeamAccess, error)
	GrantTeamAccess(access *model.ProjectTeamAccess) error
	UpdateTeamAccessLevel(projectId, teamId, accessLevel string) error
	RevokeTeamAccess(projectId, teamId string) error
}

type ProjectTeamAccessRepo struct {
	database.IDatabase
}

func NewProjectTeamAccessRepo(db database.IDatabase) IProjectTeamAccessRepository {
	return &ProjectTeamAccessRepo{IDatabase: db}
}

// GetProjectTeamAccess 获取项目团队访问权限
func (r *ProjectTeamAccessRepo) GetProjectTeamAccess(projectId, teamId string) (*model.ProjectTeamAccess, error) {
	var access model.ProjectTeamAccess
	err := r.Database().Select("id", "project_id", "team_id", "access_level", "created_at", "updated_at").
		Where("project_id = ? AND team_id = ?", projectId, teamId).First(&access).Error
	return &access, err
}

// ListProjectTeams 列出项目的所有团队
func (r *ProjectTeamAccessRepo) ListProjectTeams(projectId string) ([]model.ProjectTeamAccess, error) {
	var accesses []model.ProjectTeamAccess
	err := r.Database().Select("id", "project_id", "team_id", "access_level", "created_at", "updated_at").
		Where("project_id = ?", projectId).Find(&accesses).Error
	return accesses, err
}

// ListTeamProjects 列出团队可访问的所有项目
func (r *ProjectTeamAccessRepo) ListTeamProjects(teamId string) ([]model.ProjectTeamAccess, error) {
	var accesses []model.ProjectTeamAccess
	err := r.Database().Select("id", "project_id", "team_id", "access_level", "created_at", "updated_at").
		Where("team_id = ?", teamId).Find(&accesses).Error
	return accesses, err
}

// GrantTeamAccess 授予团队访问权限
func (r *ProjectTeamAccessRepo) GrantTeamAccess(access *model.ProjectTeamAccess) error {
	return r.Database().Create(access).Error
}

// UpdateTeamAccessLevel 更新团队访问级别
func (r *ProjectTeamAccessRepo) UpdateTeamAccessLevel(projectId, teamId, accessLevel string) error {
	return r.Database().Model(&model.ProjectTeamAccess{}).
		Where("project_id = ? AND team_id = ?", projectId, teamId).
		Update("access_level", accessLevel).Error
}

// RevokeTeamAccess 撤销团队访问权限
func (r *ProjectTeamAccessRepo) RevokeTeamAccess(projectId, teamId string) error {
	return r.Database().Where("project_id = ? AND team_id = ?", projectId, teamId).
		Delete(&model.ProjectTeamAccess{}).Error
}
