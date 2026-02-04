// Copyright 2025 Arcade Team
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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/database"
	"gorm.io/datatypes"
)

type IProjectRepository interface {
	CreateProject(p *model.Project) error
	UpdateProject(projectId string, updates map[string]interface{}) error
	DeleteProject(projectId string) error
	GetProjectById(projectId string) (*model.Project, error)
	GetProjectByName(orgId, name string) (*model.Project, error)
	ListProjects(query *model.ProjectQueryReq) ([]*model.Project, int64, error)
	GetProjectsByOrgId(orgId string, pageNum, pageSize int, status *int) ([]*model.Project, int64, error)
	GetProjectsByUserId(userId string, pageNum, pageSize int, orgId, role string) ([]*model.Project, int64, error)
	CheckProjectExists(projectId string) (bool, error)
	CheckProjectNameExists(orgId, name string, excludeProjectId ...string) (bool, error)
	UpdateProjectStatistics(projectId string, stats *model.ProjectStatisticsReq) error
	EnableProject(projectId string) error
	DisableProject(projectId string) error
}

type ProjectRepo struct {
	database.IDatabase
}

func NewProjectRepo(db database.IDatabase) IProjectRepository {
	return &ProjectRepo{IDatabase: db}
}

// CreateProject 创建项目
func (r *ProjectRepo) CreateProject(p *model.Project) error {
	return r.Database().Create(p).Error
}

// UpdateProject 更新项目
func (r *ProjectRepo) UpdateProject(projectId string, updates map[string]interface{}) error {
	return r.Database().Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Updates(updates).Error
}

// DeleteProject 删除项目（软删除，将状态设置为禁用）
func (r *ProjectRepo) DeleteProject(projectId string) error {
	return r.Database().Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Update("status", model.ProjectStatusDisabled).Error
}

// GetProjectById 根据项目ID获取项目信息
func (r *ProjectRepo) GetProjectById(projectId string) (*model.Project, error) {
	var p model.Project
	err := r.Database().
		Where("project_id = ?", projectId).
		First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjectByName 根据组织ID和项目名称获取项目
func (r *ProjectRepo) GetProjectByName(orgId, name string) (*model.Project, error) {
	var p model.Project
	err := r.Database().
		Where("org_id = ? AND name = ?", orgId, name).
		First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProjects 查询项目列表
func (r *ProjectRepo) ListProjects(query *model.ProjectQueryReq) ([]*model.Project, int64, error) {
	var projects []*model.Project
	var total int64

	// 构建查询
	db := r.Database().Model(&model.Project{})

	// 应用筛选条件
	if query.OrgId != "" {
		db = db.Where("org_id = ?", query.OrgId)
	}
	if query.Name != "" {
		db = db.Where("name LIKE ?", "%"+query.Name+"%")
	}
	if query.Language != "" {
		db = db.Where("language = ?", query.Language)
	}
	if query.Status != nil {
		db = db.Where("status = ?", *query.Status)
	}
	if query.Visibility != nil {
		db = db.Where("visibility = ?", *query.Visibility)
	}
	if query.Tags != "" {
		tags := strings.Split(query.Tags, ",")
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				db = db.Where("tags LIKE ?", "%"+tag+"%")
			}
		}
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页
	pageNum := query.PageNum
	if pageNum <= 0 {
		pageNum = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (pageNum - 1) * pageSize

	// 查询列表
	err := db.Order("created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&projects).Error

	return projects, total, err
}

// GetProjectsByOrgId 根据组织ID获取项目列表
func (r *ProjectRepo) GetProjectsByOrgId(orgId string, pageNum, pageSize int, status *int) ([]*model.Project, int64, error) {
	var projects []*model.Project
	var total int64

	db := r.Database().Model(&model.Project{}).
		Where("org_id = ?", orgId)

	if status != nil {
		db = db.Where("status = ?", *status)
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (pageNum - 1) * pageSize

	// 查询列表
	err := db.Order("created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&projects).Error

	return projects, total, err
}

// GetProjectsByUserId 获取用户的项目列表
func (r *ProjectRepo) GetProjectsByUserId(userId string, pageNum, pageSize int, orgId, role string) ([]*model.Project, int64, error) {
	var projects []*model.Project
	var total int64

	// 通过项目成员表关联查询
	db := r.Database().Table("t_project").
		Joins("INNER JOIN t_project_member ON t_project.project_id = t_project_member.project_id").
		Where("t_project_member.user_id = ?", userId)

	if orgId != "" {
		db = db.Where("t_project.org_id = ?", orgId)
	}
	if role != "" {
		db = db.Where("t_project_member.role_id = ?", role)
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (pageNum - 1) * pageSize

	// 查询列表
	err := db.Select("t_project.*").
		Order("t_project.created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&projects).Error

	return projects, total, err
}

// CheckProjectExists 检查项目是否存在
func (r *ProjectRepo) CheckProjectExists(projectId string) (bool, error) {
	var count int64
	err := r.Database().Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Count(&count).Error
	return count > 0, err
}

// CheckProjectNameExists 检查项目名称是否已存在
func (r *ProjectRepo) CheckProjectNameExists(orgId, name string, excludeProjectId ...string) (bool, error) {
	var count int64
	db := r.Database().Model(&model.Project{}).
		Where("org_id = ? AND name = ?", orgId, name)
	if len(excludeProjectId) > 0 && excludeProjectId[0] != "" {
		db = db.Where("project_id != ?", excludeProjectId[0])
	}
	err := db.Count(&count).Error
	return count > 0, err
}

// UpdateProjectStatistics 更新项目统计信息
func (r *ProjectRepo) UpdateProjectStatistics(projectId string, stats *model.ProjectStatisticsReq) error {
	updates := make(map[string]interface{})
	if stats.TotalPipelines != nil {
		updates["total_pipelines"] = *stats.TotalPipelines
	}
	if stats.TotalBuilds != nil {
		updates["total_builds"] = *stats.TotalBuilds
	}
	if stats.SuccessBuilds != nil {
		updates["success_builds"] = *stats.SuccessBuilds
	}
	if stats.FailedBuilds != nil {
		updates["failed_builds"] = *stats.FailedBuilds
	}
	if len(updates) == 0 {
		return nil
	}
	return r.Database().Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Updates(updates).Error
}

// EnableProject 启用项目
func (r *ProjectRepo) EnableProject(projectId string) error {
	return r.Database().Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Update("is_enabled", 1).Error
}

// DisableProject 禁用项目
func (r *ProjectRepo) DisableProject(projectId string) error {
	return r.Database().Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Update("is_enabled", 0).Error
}

// ConvertJSONToDatatypes 将 map 转换为 datatypes.JSON
func ConvertJSONToDatatypes(data map[string]interface{}) (datatypes.JSON, error) {
	if data == nil {
		return nil, nil
	}
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal json failed: %w", err)
	}
	return datatypes.JSON(jsonBytes), nil
}
