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

package project

import (
	"encoding/json"
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/project"
	"gorm.io/datatypes"
)

// ProjectPO is the GORM persistence object for the t_project table.
type ProjectPO struct {
	ID             uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID      string         `gorm:"column:project_id"`
	OrgID          string         `gorm:"column:org_id"`
	Name           string         `gorm:"column:name"`
	DisplayName    string         `gorm:"column:display_name"`
	Namespace      string         `gorm:"column:namespace"`
	Description    string         `gorm:"column:description"`
	RepoURL        string         `gorm:"column:repo_url"`
	RepoType       string         `gorm:"column:repo_type"`
	DefaultBranch  string         `gorm:"column:default_branch"`
	AuthType       int            `gorm:"column:auth_type"`
	Credential     string         `gorm:"column:credential"`
	TriggerMode    int            `gorm:"column:trigger_mode"`
	WebhookSecret  string         `gorm:"column:webhook_secret"`
	CronExpr       string         `gorm:"column:cron_expr"`
	BuildConfig    datatypes.JSON `gorm:"column:build_config"`
	EnvVars        datatypes.JSON `gorm:"column:env_vars"`
	Settings       datatypes.JSON `gorm:"column:settings"`
	Tags           string         `gorm:"column:tags"`
	Language       string         `gorm:"column:language"`
	Framework      string         `gorm:"column:framework"`
	Status         int            `gorm:"column:status"`
	Visibility     int            `gorm:"column:visibility"`
	AccessLevel    string         `gorm:"column:access_level"`
	CreatedBy      string         `gorm:"column:created_by"`
	IsEnabled      int            `gorm:"column:is_enabled"`
	Icon           string         `gorm:"column:icon"`
	Homepage       string         `gorm:"column:homepage"`
	TotalPipelines int            `gorm:"column:total_pipelines"`
	TotalBuilds    int            `gorm:"column:total_builds"`
	SuccessBuilds  int            `gorm:"column:success_builds"`
	FailedBuilds   int            `gorm:"column:failed_builds"`
	CreatedAt      time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time      `gorm:"column:updated_at;autoUpdateTime"`
}

func (ProjectPO) TableName() string { return "t_project" }

// ToDomain converts the persistence object to a domain entity.
func (po *ProjectPO) ToDomain() *domain.Project {
	return &domain.Project{
		ID:             po.ID,
		ProjectID:      po.ProjectID,
		OrgID:          po.OrgID,
		Name:           po.Name,
		DisplayName:    po.DisplayName,
		Namespace:      po.Namespace,
		Description:    po.Description,
		RepoURL:        po.RepoURL,
		RepoType:       po.RepoType,
		DefaultBranch:  po.DefaultBranch,
		AuthType:       domain.AuthType(po.AuthType),
		Credential:     po.Credential,
		TriggerMode:    po.TriggerMode,
		WebhookSecret:  po.WebhookSecret,
		CronExpr:       po.CronExpr,
		BuildConfig:    json.RawMessage(po.BuildConfig),
		EnvVars:        json.RawMessage(po.EnvVars),
		Settings:       json.RawMessage(po.Settings),
		Tags:           po.Tags,
		Language:       po.Language,
		Framework:      po.Framework,
		Status:         domain.ProjectStatus(po.Status),
		Visibility:     domain.ProjectVisibility(po.Visibility),
		AccessLevel:    po.AccessLevel,
		CreatedBy:      po.CreatedBy,
		IsEnabled:      po.IsEnabled == 1,
		Icon:           po.Icon,
		Homepage:       po.Homepage,
		TotalPipelines: po.TotalPipelines,
		TotalBuilds:    po.TotalBuilds,
		SuccessBuilds:  po.SuccessBuilds,
		FailedBuilds:   po.FailedBuilds,
		CreatedAt:      po.CreatedAt,
		UpdatedAt:      po.UpdatedAt,
	}
}

// ProjectPOFromDomain creates a persistence object from a domain entity.
func ProjectPOFromDomain(p *domain.Project) *ProjectPO {
	isEnabled := 0
	if p.IsEnabled {
		isEnabled = 1
	}

	return &ProjectPO{
		ID:             p.ID,
		ProjectID:      p.ProjectID,
		OrgID:          p.OrgID,
		Name:           p.Name,
		DisplayName:    p.DisplayName,
		Namespace:      p.Namespace,
		Description:    p.Description,
		RepoURL:        p.RepoURL,
		RepoType:       p.RepoType,
		DefaultBranch:  p.DefaultBranch,
		AuthType:       int(p.AuthType),
		Credential:     p.Credential,
		TriggerMode:    p.TriggerMode,
		WebhookSecret:  p.WebhookSecret,
		CronExpr:       p.CronExpr,
		BuildConfig:    datatypes.JSON(p.BuildConfig),
		EnvVars:        datatypes.JSON(p.EnvVars),
		Settings:       datatypes.JSON(p.Settings),
		Tags:           p.Tags,
		Language:       p.Language,
		Framework:      p.Framework,
		Status:         int(p.Status),
		Visibility:     int(p.Visibility),
		AccessLevel:    p.AccessLevel,
		CreatedBy:      p.CreatedBy,
		IsEnabled:      isEnabled,
		Icon:           p.Icon,
		Homepage:       p.Homepage,
		TotalPipelines: p.TotalPipelines,
		TotalBuilds:    p.TotalBuilds,
		SuccessBuilds:  p.SuccessBuilds,
		FailedBuilds:   p.FailedBuilds,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

// ProjectMemberPO is the GORM persistence object for the t_project_member table.
type ProjectMemberPO struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID string    `gorm:"column:project_id"`
	UserID    string    `gorm:"column:user_id"`
	RoleID    string    `gorm:"column:role_id"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (ProjectMemberPO) TableName() string { return "t_project_member" }

// ToDomain converts the persistence object to a domain entity.
func (po *ProjectMemberPO) ToDomain() *domain.ProjectMember {
	return &domain.ProjectMember{
		ID:        po.ID,
		ProjectID: po.ProjectID,
		UserID:    po.UserID,
		RoleID:    po.RoleID,
		CreatedAt: po.CreatedAt,
		UpdatedAt: po.UpdatedAt,
	}
}

// ProjectMemberPOFromDomain creates a persistence object from a domain entity.
func ProjectMemberPOFromDomain(m *domain.ProjectMember) *ProjectMemberPO {
	return &ProjectMemberPO{
		ID:        m.ID,
		ProjectID: m.ProjectID,
		UserID:    m.UserID,
		RoleID:    m.RoleID,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// ProjectTeamAccessPO is the GORM persistence object for the t_project_team_access table.
type ProjectTeamAccessPO struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID   string    `gorm:"column:project_id"`
	TeamID      string    `gorm:"column:team_id"`
	AccessLevel string    `gorm:"column:access_level"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (ProjectTeamAccessPO) TableName() string { return "t_project_team_access" }

// ToDomain converts the persistence object to a domain entity.
func (po *ProjectTeamAccessPO) ToDomain() *domain.ProjectTeamAccess {
	return &domain.ProjectTeamAccess{
		ID:          po.ID,
		ProjectID:   po.ProjectID,
		TeamID:      po.TeamID,
		AccessLevel: domain.TeamAccessLevel(po.AccessLevel),
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}
}

// ProjectTeamAccessPOFromDomain creates a persistence object from a domain entity.
func ProjectTeamAccessPOFromDomain(a *domain.ProjectTeamAccess) *ProjectTeamAccessPO {
	return &ProjectTeamAccessPO{
		ID:          a.ID,
		ProjectID:   a.ProjectID,
		TeamID:      a.TeamID,
		AccessLevel: string(a.AccessLevel),
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}

// SecretPO is the GORM persistence object for the t_secret table.
type SecretPO struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	SecretID    string    `gorm:"column:secret_id"`
	Name        string    `gorm:"column:name"`
	SecretType  string    `gorm:"column:secret_type"`
	SecretValue string    `gorm:"column:secret_value"`
	Description string    `gorm:"column:description"`
	Scope       string    `gorm:"column:scope"`
	ScopeID     string    `gorm:"column:scope_id"`
	CreatedBy   string    `gorm:"column:created_by"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (SecretPO) TableName() string { return "t_secret" }

// ToDomain converts the persistence object to a domain entity.
func (po *SecretPO) ToDomain() *domain.Secret {
	return &domain.Secret{
		ID:          po.ID,
		SecretID:    po.SecretID,
		Name:        po.Name,
		SecretType:  po.SecretType,
		SecretValue: po.SecretValue,
		Description: po.Description,
		Scope:       po.Scope,
		ScopeID:     po.ScopeID,
		CreatedBy:   po.CreatedBy,
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}
}

// SecretPOFromDomain creates a persistence object from a domain entity.
func SecretPOFromDomain(s *domain.Secret) *SecretPO {
	return &SecretPO{
		ID:          s.ID,
		SecretID:    s.SecretID,
		Name:        s.Name,
		SecretType:  s.SecretType,
		SecretValue: s.SecretValue,
		Description: s.Description,
		Scope:       s.Scope,
		ScopeID:     s.ScopeID,
		CreatedBy:   s.CreatedBy,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
}

// GeneralSettingsPO is the GORM persistence object for the t_general_settings table.
type GeneralSettingsPO struct {
	ID          uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	SettingsID  string         `gorm:"column:settings_id"`
	Category    string         `gorm:"column:category"`
	Name        string         `gorm:"column:name"`
	DisplayName string         `gorm:"column:display_name"`
	Data        datatypes.JSON `gorm:"column:data"`
	Schema      datatypes.JSON `gorm:"column:schema"`
	Description string         `gorm:"column:description"`
	CreatedAt   time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;autoUpdateTime"`
}

func (GeneralSettingsPO) TableName() string { return "t_general_settings" }

// ToDomain converts the persistence object to a domain entity.
func (po *GeneralSettingsPO) ToDomain() *domain.GeneralSettings {
	return &domain.GeneralSettings{
		ID:          po.ID,
		SettingsID:  po.SettingsID,
		Category:    po.Category,
		Name:        po.Name,
		DisplayName: po.DisplayName,
		Data:        json.RawMessage(po.Data),
		Schema:      json.RawMessage(po.Schema),
		Description: po.Description,
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}
}

// GeneralSettingsPOFromDomain creates a persistence object from a domain entity.
func GeneralSettingsPOFromDomain(gs *domain.GeneralSettings) *GeneralSettingsPO {
	return &GeneralSettingsPO{
		ID:          gs.ID,
		SettingsID:  gs.SettingsID,
		Category:    gs.Category,
		Name:        gs.Name,
		DisplayName: gs.DisplayName,
		Data:        datatypes.JSON(gs.Data),
		Schema:      datatypes.JSON(gs.Schema),
		Description: gs.Description,
		CreatedAt:   gs.CreatedAt,
		UpdatedAt:   gs.UpdatedAt,
	}
}
