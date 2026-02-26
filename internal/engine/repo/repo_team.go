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
	"gorm.io/gorm"
)

// ITeamRepository defines team persistence with context support.
type ITeamRepository interface {
	Create(ctx context.Context, t *model.Team) error
	Get(ctx context.Context, teamId string) (*model.Team, error)
	GetByName(ctx context.Context, orgId, name string) (*model.Team, error)
	Update(ctx context.Context, teamId string, updates map[string]interface{}) error
	Delete(ctx context.Context, teamId string) error
	List(ctx context.Context, query *model.TeamQueryReq) ([]*model.Team, int64, error)
	ListByOrg(ctx context.Context, orgId string) ([]*model.Team, error)
	ListSubTeams(ctx context.Context, parentTeamId string) ([]*model.Team, error)
	Exists(ctx context.Context, teamId string) (bool, error)
	NameExists(ctx context.Context, orgId, name string, excludeTeamId ...string) (bool, error)
	UpdatePath(ctx context.Context, teamId, path string, level int) error
	IncrementMembers(ctx context.Context, teamId string, delta int) error
	IncrementProjects(ctx context.Context, teamId string, delta int) error
	UpdateStatistics(ctx context.Context, teamId string) error
	BuildPath(ctx context.Context, parentTeamId string) (string, int, error)
	BatchGet(ctx context.Context, teamIds []string) ([]*model.Team, error)
	ListByUser(ctx context.Context, userId string) ([]*model.Team, error)
}

type TeamRepo struct {
	database.IDatabase
}

func NewTeamRepo(db database.IDatabase) ITeamRepository {
	return &TeamRepo{IDatabase: db}
}

// Create creates a new team.
func (r *TeamRepo) Create(ctx context.Context, t *model.Team) error {
	return r.Database().WithContext(ctx).Create(t).Error
}

// Update updates team by teamId.
func (r *TeamRepo) Update(ctx context.Context, teamId string, updates map[string]interface{}) error {
	return r.Database().WithContext(ctx).Model(&model.Team{}).
		Where("team_id = ?", teamId).
		Updates(updates).Error
}

// Delete deletes team by teamId.
func (r *TeamRepo) Delete(ctx context.Context, teamId string) error {
	return r.Database().WithContext(ctx).Where("team_id = ?", teamId).Delete(&model.Team{}).Error
}

// Get returns team by teamId.
func (r *TeamRepo) Get(ctx context.Context, teamId string) (*model.Team, error) {
	var t model.Team
	err := r.Database().WithContext(ctx).Select("id", "team_id", "org_id", "name", "display_name", "description", "avatar", "parent_team_id", "path", "level", "settings", "visibility", "is_enabled", "total_members", "total_projects", "created_at", "updated_at").
		Where("team_id = ?", teamId).First(&t).Error
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetByName returns team by orgId and name.
func (r *TeamRepo) GetByName(ctx context.Context, orgId, name string) (*model.Team, error) {
	var t model.Team
	err := r.Database().WithContext(ctx).Select("id", "team_id", "org_id", "name", "display_name", "description", "avatar", "parent_team_id", "path", "level", "settings", "visibility", "is_enabled", "total_members", "total_projects", "created_at", "updated_at").
		Where("org_id = ? AND name = ?", orgId, name).First(&t).Error
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// List lists teams with query filters.
func (r *TeamRepo) List(ctx context.Context, query *model.TeamQueryReq) ([]*model.Team, int64, error) {
	var teams []*model.Team
	var total int64

	db := r.Database().WithContext(ctx).Model(&model.Team{})

	// 条件查询
	if query.OrgId != "" {
		db = db.Where("org_id = ?", query.OrgId)
	}
	if query.Name != "" {
		db = db.Where("name LIKE ?", "%"+query.Name+"%")
	}
	if query.ParentTeamId != "" {
		db = db.Where("parent_team_id = ?", query.ParentTeamId)
	}
	if query.Visibility != nil {
		db = db.Where("visibility = ?", *query.Visibility)
	}
	if query.IsEnabled != nil {
		db = db.Where("is_enabled = ?", *query.IsEnabled)
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页
	if query.Page > 0 && query.PageSize > 0 {
		offset := (query.Page - 1) * query.PageSize
		db = db.Offset(offset).Limit(query.PageSize)
	} else {
		// 默认分页
		db = db.Limit(100)
	}

	// 查询结果，指定字段排除创建和更新时间
	err := db.Select("team_id", "org_id", "name", "display_name", "description", "avatar", "parent_team_id", "path", "level", "settings", "visibility", "is_enabled", "total_members", "total_projects").
		Order("team_id DESC").
		Find(&teams).Error
	return teams, total, err
}

// ListByOrg lists teams by orgId.
func (r *TeamRepo) ListByOrg(ctx context.Context, orgId string) ([]*model.Team, error) {
	var teams []*model.Team
	err := r.Database().WithContext(ctx).
		Select("team_id", "org_id", "name", "display_name", "description", "avatar", "parent_team_id", "path", "level", "settings", "visibility", "is_enabled", "total_members", "total_projects").
		Where("org_id = ? AND is_enabled = ?", orgId, 1).
		Order("level ASC, team_id DESC").
		Find(&teams).Error
	return teams, err
}

// ListSubTeams lists sub-teams by parentTeamId.
func (r *TeamRepo) ListSubTeams(ctx context.Context, parentTeamId string) ([]*model.Team, error) {
	var teams []*model.Team
	err := r.Database().WithContext(ctx).
		Select("team_id", "org_id", "name", "display_name", "description", "avatar", "parent_team_id", "path", "level", "settings", "visibility", "is_enabled", "total_members", "total_projects").
		Where("parent_team_id = ? AND is_enabled = ?", parentTeamId, 1).
		Order("team_id DESC").
		Find(&teams).Error
	return teams, err
}

// Exists checks if team exists by teamId.
func (r *TeamRepo) Exists(ctx context.Context, teamId string) (bool, error) {
	var count int64
	err := r.Database().WithContext(ctx).Model(&model.Team{}).
		Where("team_id = ?", teamId).
		Count(&count).Error
	return count > 0, err
}

// NameExists checks if team name exists in org.
func (r *TeamRepo) NameExists(ctx context.Context, orgId, name string, excludeTeamId ...string) (bool, error) {
	query := r.Database().WithContext(ctx).Model(&model.Team{}).
		Where("org_id = ? AND name = ?", orgId, name)

	if len(excludeTeamId) > 0 && excludeTeamId[0] != "" {
		query = query.Where("team_id != ?", excludeTeamId[0])
	}

	var count int64
	err := query.Count(&count).Error
	return count > 0, err
}

// UpdatePath updates team path and level.
func (r *TeamRepo) UpdatePath(ctx context.Context, teamId, path string, level int) error {
	return r.Database().WithContext(ctx).Model(&model.Team{}).
		Where("team_id = ?", teamId).
		Updates(map[string]interface{}{
			"path":  path,
			"level": level,
		}).Error
}

// IncrementMembers increments team member count.
func (r *TeamRepo) IncrementMembers(ctx context.Context, teamId string, delta int) error {
	return r.Database().WithContext(ctx).Model(&model.Team{}).
		Where("team_id = ?", teamId).
		Update("total_members", gorm.Expr("total_members + ?", delta)).Error
}

// IncrementProjects increments team project count.
func (r *TeamRepo) IncrementProjects(ctx context.Context, teamId string, delta int) error {
	return r.Database().WithContext(ctx).Model(&model.Team{}).
		Where("team_id = ?", teamId).
		Update("total_projects", gorm.Expr("total_projects + ?", delta)).Error
}

// UpdateStatistics updates team member and project counts.
func (r *TeamRepo) UpdateStatistics(ctx context.Context, teamId string) error {
	var memberCount int64
	if err := r.Database().WithContext(ctx).Model(&model.TeamMember{}).
		Where("team_id = ?", teamId).
		Count(&memberCount).Error; err != nil {
		return err
	}

	// 更新项目数量（假设有团队项目关联表）
	var projectCount int64
	r.Database().WithContext(ctx).Table("t_project_team_relation").
		Where("team_id = ?", teamId).
		Count(&projectCount)

	return r.Database().WithContext(ctx).Model(&model.Team{}).
		Where("team_id = ?", teamId).
		Updates(map[string]interface{}{
			"total_members":  memberCount,
			"total_projects": projectCount,
		}).Error
}

// BuildPath builds team path from parent.
func (r *TeamRepo) BuildPath(ctx context.Context, parentTeamId string) (string, int, error) {
	if parentTeamId == "" {
		return "/", 0, nil
	}

	parent, err := r.Get(ctx, parentTeamId)
	if err != nil {
		return "", 0, fmt.Errorf("parent team not found: %w", err)
	}

	path := strings.TrimSuffix(parent.Path, "/") + "/" + parentTeamId + "/"
	level := parent.Level + 1

	return path, level, nil
}

// ConvertSettingsToJSON 将 settings map 转换为 JSON
func ConvertSettingsToJSON(settings map[string]interface{}) (datatypes.JSON, error) {
	if settings == nil {
		return datatypes.JSON("{}"), nil
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// BatchGet returns teams by teamIds.
func (r *TeamRepo) BatchGet(ctx context.Context, teamIds []string) ([]*model.Team, error) {
	if len(teamIds) == 0 {
		return []*model.Team{}, nil
	}

	var teams []*model.Team
	err := r.Database().WithContext(ctx).Select("id", "team_id", "org_id", "name", "display_name", "description", "avatar", "parent_team_id", "path", "level", "settings", "visibility", "is_enabled", "total_members", "total_projects", "created_at", "updated_at").
		Where("team_id IN ?", teamIds).Find(&teams).Error
	return teams, err
}

// ListByUser lists teams for user by userId.
func (r *TeamRepo) ListByUser(ctx context.Context, userId string) ([]*model.Team, error) {
	var teams []*model.Team
	err := r.Database().WithContext(ctx).Table("t_team t").
		Select("t.team_id", "t.org_id", "t.name", "t.display_name", "t.description", "t.avatar", "t.parent_team_id", "t.path", "t.level", "t.settings", "t.visibility", "t.is_enabled", "t.total_members", "t.total_projects").
		Joins("JOIN t_team_member tm ON t.team_id = tm.team_id").
		Where("tm.user_id = ? AND t.is_enabled = ?", userId, 1).
		Order("t.team_id DESC").
		Find(&teams).Error
	return teams, err
}
