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

// ITeamMemberRepository defines team member persistence with context support.
type ITeamMemberRepository interface {
	Get(ctx context.Context, teamId, userId string) (*model.TeamMember, error)
	ListTeamMembers(ctx context.Context, teamId string) ([]model.TeamMember, error)
	ListUserTeams(ctx context.Context, userId string) ([]model.TeamMember, error)
	AddTeamMember(ctx context.Context, member *model.TeamMember) error
	UpdateTeamMemberRole(ctx context.Context, teamId, userId, role string) error
	RemoveTeamMember(ctx context.Context, teamId, userId string) error
}

type TeamMemberRepo struct {
	database.IDatabase
}

func NewTeamMemberRepo(db database.IDatabase) ITeamMemberRepository {
	return &TeamMemberRepo{IDatabase: db}
}

// Get returns team member by teamId and userId.
func (r *TeamMemberRepo) Get(ctx context.Context, teamId, userId string) (*model.TeamMember, error) {
	var member model.TeamMember
	err := r.Database().WithContext(ctx).Select("id", "team_id", "user_id", "role_id", "created_at", "updated_at").
		Where("team_id = ? AND user_id = ?", teamId, userId).First(&member).Error
	return &member, err
}

// ListTeamMembers lists team members.
func (r *TeamMemberRepo) ListTeamMembers(ctx context.Context, teamId string) ([]model.TeamMember, error) {
	var members []model.TeamMember
	err := r.Database().WithContext(ctx).Select("id", "team_id", "user_id", "role_id", "created_at", "updated_at").
		Where("team_id = ?", teamId).Find(&members).Error
	return members, err
}

// ListUserTeams lists user's teams.
func (r *TeamMemberRepo) ListUserTeams(ctx context.Context, userId string) ([]model.TeamMember, error) {
	var members []model.TeamMember
	err := r.Database().WithContext(ctx).Select("id", "team_id", "user_id", "role_id", "created_at", "updated_at").
		Where("user_id = ?", userId).Find(&members).Error
	return members, err
}

// AddTeamMember adds a team member.
func (r *TeamMemberRepo) AddTeamMember(ctx context.Context, member *model.TeamMember) error {
	return r.Database().WithContext(ctx).Create(member).Error
}

// UpdateTeamMemberRole updates team member role.
func (r *TeamMemberRepo) UpdateTeamMemberRole(ctx context.Context, teamId, userId, role string) error {
	return r.Database().WithContext(ctx).Model(&model.TeamMember{}).
		Where("team_id = ? AND user_id = ?", teamId, userId).
		Update("role", role).Error
}

// RemoveTeamMember removes a team member.
func (r *TeamMemberRepo) RemoveTeamMember(ctx context.Context, teamId, userId string) error {
	return r.Database().WithContext(ctx).Where("team_id = ? AND user_id = ?", teamId, userId).
		Delete(&model.TeamMember{}).Error
}
