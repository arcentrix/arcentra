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
	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/database"
)

type ITeamMemberRepository interface {
	GetTeamMember(teamId, userId string) (*model.TeamMember, error)
	ListTeamMembers(teamId string) ([]model.TeamMember, error)
	ListUserTeams(userId string) ([]model.TeamMember, error)
	AddTeamMember(member *model.TeamMember) error
	UpdateTeamMemberRole(teamId, userId, role string) error
	RemoveTeamMember(teamId, userId string) error
}

type TeamMemberRepo struct {
	database.IDatabase
}

func NewTeamMemberRepo(db database.IDatabase) ITeamMemberRepository {
	return &TeamMemberRepo{IDatabase: db}
}

// GetTeamMember 获取团队成员
func (r *TeamMemberRepo) GetTeamMember(teamId, userId string) (*model.TeamMember, error) {
	var member model.TeamMember
	err := r.Database().Select("id", "team_id", "user_id", "role_id", "created_at", "updated_at").
		Where("team_id = ? AND user_id = ?", teamId, userId).First(&member).Error
	return &member, err
}

// ListTeamMembers 列出团队成员
func (r *TeamMemberRepo) ListTeamMembers(teamId string) ([]model.TeamMember, error) {
	var members []model.TeamMember
	err := r.Database().Select("id", "team_id", "user_id", "role_id", "created_at", "updated_at").
		Where("team_id = ?", teamId).Find(&members).Error
	return members, err
}

// ListUserTeams 列出用户所在的团队
func (r *TeamMemberRepo) ListUserTeams(userId string) ([]model.TeamMember, error) {
	var members []model.TeamMember
	err := r.Database().Select("id", "team_id", "user_id", "role_id", "created_at", "updated_at").
		Where("user_id = ?", userId).Find(&members).Error
	return members, err
}

// AddTeamMember 添加团队成员
func (r *TeamMemberRepo) AddTeamMember(member *model.TeamMember) error {
	return r.Database().Create(member).Error
}

// UpdateTeamMemberRole 更新团队成员角色
func (r *TeamMemberRepo) UpdateTeamMemberRole(teamId, userId, role string) error {
	return r.Database().Model(&model.TeamMember{}).
		Where("team_id = ? AND user_id = ?", teamId, userId).
		Update("role", role).Error
}

// RemoveTeamMember 移除团队成员
func (r *TeamMemberRepo) RemoveTeamMember(teamId, userId string) error {
	return r.Database().Where("team_id = ? AND user_id = ?", teamId, userId).
		Delete(&model.TeamMember{}).Error
}
