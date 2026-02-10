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

package model

import (
	"github.com/bytedance/sonic"
	"gorm.io/datatypes"
)

// Team team entity
type Team struct {
	BaseModel
	TeamId       string         `gorm:"column:team_id" json:"teamId"`              // team unique identifier
	OrgId        string         `gorm:"column:org_id" json:"orgId"`                // organization id
	Name         string         `gorm:"column:name" json:"name"`                   // team name
	DisplayName  string         `gorm:"column:display_name" json:"displayName"`    // team display name
	Description  string         `gorm:"column:description" json:"description"`     // team description
	Avatar       string         `gorm:"column:avatar" json:"avatar"`               // team avatar
	ParentTeamId string         `gorm:"column:parent_team_id" json:"parentTeamId"` // parent team id(supports nested)
	Path         string         `gorm:"column:path" json:"path"`                   // team path(for hierarchical relationship)
	Level        int            `gorm:"column:level" json:"level"`                 // team level
	Settings     datatypes.JSON `gorm:"column:settings;type:json" json:"settings"` // team settings
	Visibility   int            `gorm:"column:visibility" json:"visibility"`       // visibility: 0-private, 1-internal, 2-public
	IsEnabled    int            `gorm:"column:is_enabled" json:"isEnabled"`        // 0: disabled, 1: enabled

	// statistics fields
	TotalMembers  int `gorm:"column:total_members" json:"totalMembers"`   // total members
	TotalProjects int `gorm:"column:total_projects" json:"totalProjects"` // total projects
}

func (Team) TableName() string {
	return "t_team"
}

// TeamSettings team settings struct
type TeamSettings struct {
	DefaultRole       string `json:"default_role"`        // default role
	AllowMemberInvite bool   `json:"allow_member_invite"` // allow member invite
	RequireApproval   bool   `json:"require_approval"`    // require approval
	MaxMembers        int    `json:"max_members"`         // max members
}

// TeamVisibility team visibility enum
const (
	TeamVisibilityPrivate  = 0 // private(only members can see)
	TeamVisibilityInternal = 1 // internal(only organization members can see)
	TeamVisibilityPublic   = 2 // public(everyone can see)
)

// CreateTeamReq create team request
type CreateTeamReq struct {
	OrgId        string                 `json:"orgId" validate:"required"`
	Name         string                 `json:"name" validate:"required,min=2,max=64"`
	DisplayName  string                 `json:"displayName"`
	Description  string                 `json:"description"`
	Avatar       string                 `json:"avatar"`
	ParentTeamId string                 `json:"parentTeamId"`
	Settings     map[string]interface{} `json:"settings"`
	Visibility   int                    `json:"visibility"` // 0-private, 1-internal, 2-public
}

// UpdateTeamReq update team request
type UpdateTeamReq struct {
	Name        *string                `json:"name,omitempty"`
	DisplayName *string                `json:"displayName,omitempty"`
	Description *string                `json:"description,omitempty"`
	Avatar      *string                `json:"avatar,omitempty"`
	Settings    map[string]interface{} `json:"settings,omitempty"`
	Visibility  *int                   `json:"visibility,omitempty"`
	IsEnabled   *int                   `json:"isEnabled,omitempty"`
}

// TeamQueryReq query team request
type TeamQueryReq struct {
	OrgId        string `json:"orgId" form:"orgId"`
	Name         string `json:"name" form:"name"`
	ParentTeamId string `json:"parentTeamId" form:"parentTeamId"`
	Visibility   *int   `json:"visibility" form:"visibility"`
	IsEnabled    *int   `json:"isEnabled" form:"isEnabled"`
	Page         int    `json:"page" form:"page"`
	PageSize     int    `json:"pageSize" form:"pageSize"`
}

// TeamResp team response
type TeamResp struct {
	TeamId        string                 `json:"teamId"`
	OrgId         string                 `json:"orgId"`
	Name          string                 `json:"name"`
	DisplayName   string                 `json:"displayName"`
	Description   string                 `json:"description"`
	Avatar        string                 `json:"avatar"`
	ParentTeamId  string                 `json:"parentTeamId"`
	Path          string                 `json:"path"`
	Level         int                    `json:"level"`
	Settings      map[string]interface{} `json:"settings"`
	Visibility    int                    `json:"visibility"`
	IsEnabled     int                    `json:"isEnabled"`
	TotalMembers  int                    `json:"totalMembers"`
	TotalProjects int                    `json:"totalProjects"`
	CreatedAt     string                 `json:"createdAt"`
	UpdatedAt     string                 `json:"updatedAt"`
}

// TeamListResp team list response
type TeamListResp struct {
	Teams      []*TeamResp `json:"teams"`
	Total      int64       `json:"total"`
	Page       int         `json:"page"`
	PageSize   int         `json:"pageSize"`
	TotalPages int         `json:"totalPages"`
}

func ToTeamResp(team *Team) *TeamResp {
	if team == nil {
		return nil
	}

	resp := &TeamResp{
		TeamId:        team.TeamId,
		OrgId:         team.OrgId,
		Name:          team.Name,
		DisplayName:   team.DisplayName,
		Description:   team.Description,
		Avatar:        team.Avatar,
		ParentTeamId:  team.ParentTeamId,
		Path:          team.Path,
		Level:         team.Level,
		Visibility:    team.Visibility,
		IsEnabled:     team.IsEnabled,
		TotalMembers:  team.TotalMembers,
		TotalProjects: team.TotalProjects,
		CreatedAt:     team.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:     team.UpdatedAt.Format("2006-01-02 15:04:05"),
	}

	// parse Settings JSON
	if len(team.Settings) > 0 {
		settings := make(map[string]interface{})
		if err := sonic.Unmarshal(team.Settings, &settings); err == nil {
			resp.Settings = settings
		}
	}

	return resp
}
