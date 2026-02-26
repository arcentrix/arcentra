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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/datatypes"
)

// IProjectRepository defines project persistence with context support for timeout, tracing and cancellation.
type IProjectRepository interface {
	Create(ctx context.Context, p *model.Project) error
	Get(ctx context.Context, projectId string) (*model.Project, error)
	GetByName(ctx context.Context, orgId, name string) (*model.Project, error)
	Update(ctx context.Context, projectId string, updates map[string]interface{}) error
	Delete(ctx context.Context, projectId string) error
	List(ctx context.Context, query *model.ProjectQueryReq) ([]*model.Project, int64, error)
	ListByOrg(ctx context.Context, orgId string, pageNum, pageSize int, status *int) ([]*model.Project, int64, error)
	ListByUser(ctx context.Context, userId string, pageNum, pageSize int, orgId, role string) ([]*model.Project, int64, error)
	Exists(ctx context.Context, projectId string) (bool, error)
	NameExists(ctx context.Context, orgId, name string, excludeProjectId ...string) (bool, error)
	UpdateStatistics(ctx context.Context, projectId string, stats *model.ProjectStatisticsReq) error
	Enable(ctx context.Context, projectId string) error
	Disable(ctx context.Context, projectId string) error
}

type ProjectRepo struct {
	database.IDatabase
}

// NewProjectRepo creates a project repository.
func NewProjectRepo(db database.IDatabase) IProjectRepository {
	return &ProjectRepo{IDatabase: db}
}

// Create creates a new project.
func (r *ProjectRepo) Create(ctx context.Context, p *model.Project) error {
	return r.Database().WithContext(ctx).Create(p).Error
}

// Update updates project by projectId.
func (r *ProjectRepo) Update(ctx context.Context, projectId string, updates map[string]interface{}) error {
	return r.Database().WithContext(ctx).Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Updates(updates).Error
}

// Delete soft-deletes project by projectId (sets status to disabled).
func (r *ProjectRepo) Delete(ctx context.Context, projectId string) error {
	return r.Database().WithContext(ctx).Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Update("status", model.ProjectStatusDisabled).Error
}

// Get returns project by projectId.
func (r *ProjectRepo) Get(ctx context.Context, projectId string) (*model.Project, error) {
	var p model.Project
	err := r.Database().WithContext(ctx).
		Where("project_id = ?", projectId).
		First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetByName returns project by orgId and name.
func (r *ProjectRepo) GetByName(ctx context.Context, orgId, name string) (*model.Project, error) {
	var p model.Project
	err := r.Database().WithContext(ctx).
		Where("org_id = ? AND name = ?", orgId, name).
		First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// List lists projects with query filters.
func (r *ProjectRepo) List(ctx context.Context, query *model.ProjectQueryReq) ([]*model.Project, int64, error) {
	var projects []*model.Project
	var total int64

	db := r.Database().WithContext(ctx).Model(&model.Project{})

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

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

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

	err := db.Order("created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&projects).Error

	return projects, total, err
}

// ListByOrg lists projects by orgId with pagination.
func (r *ProjectRepo) ListByOrg(ctx context.Context, orgId string, pageNum, pageSize int, status *int) ([]*model.Project, int64, error) {
	var projects []*model.Project
	var total int64

	db := r.Database().WithContext(ctx).Model(&model.Project{}).
		Where("org_id = ?", orgId)

	if status != nil {
		db = db.Where("status = ?", *status)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

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

	err := db.Order("created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&projects).Error

	return projects, total, err
}

// ListByUser lists projects for user by userId with pagination.
func (r *ProjectRepo) ListByUser(ctx context.Context, userId string, pageNum, pageSize int, orgId, role string) ([]*model.Project, int64, error) {
	var projects []*model.Project
	var total int64

	db := r.Database().WithContext(ctx).Table("t_project").
		Joins("INNER JOIN t_project_member ON t_project.project_id = t_project_member.project_id").
		Where("t_project_member.user_id = ?", userId)

	if orgId != "" {
		db = db.Where("t_project.org_id = ?", orgId)
	}
	if role != "" {
		db = db.Where("t_project_member.role_id = ?", role)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

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

	err := db.Select("t_project.*").
		Order("t_project.created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&projects).Error

	return projects, total, err
}

// Exists checks if project exists by projectId.
func (r *ProjectRepo) Exists(ctx context.Context, projectId string) (bool, error) {
	var count int64
	err := r.Database().WithContext(ctx).Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Count(&count).Error
	return count > 0, err
}

// NameExists checks if project name exists in org.
func (r *ProjectRepo) NameExists(ctx context.Context, orgId, name string, excludeProjectId ...string) (bool, error) {
	var count int64
	db := r.Database().WithContext(ctx).Model(&model.Project{}).
		Where("org_id = ? AND name = ?", orgId, name)
	if len(excludeProjectId) > 0 && excludeProjectId[0] != "" {
		db = db.Where("project_id != ?", excludeProjectId[0])
	}
	err := db.Count(&count).Error
	return count > 0, err
}

// UpdateStatistics updates project statistics.
func (r *ProjectRepo) UpdateStatistics(ctx context.Context, projectId string, stats *model.ProjectStatisticsReq) error {
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
	return r.Database().WithContext(ctx).Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Updates(updates).Error
}

// Enable enables project by projectId.
func (r *ProjectRepo) Enable(ctx context.Context, projectId string) error {
	return r.Database().WithContext(ctx).Model(&model.Project{}).
		Where("project_id = ?", projectId).
		Update("is_enabled", 1).Error
}

// Disable disables project by projectId.
func (r *ProjectRepo) Disable(ctx context.Context, projectId string) error {
	return r.Database().WithContext(ctx).Model(&model.Project{}).
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
	return jsonBytes, nil
}
