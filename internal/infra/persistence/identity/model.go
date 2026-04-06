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

package identity

import (
	"encoding/json"
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/identity"
	"gorm.io/datatypes"
)

// ---------------------------------------------------------------------------
// UserPO
// ---------------------------------------------------------------------------

type UserPO struct {
	ID           uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID       string    `gorm:"column:user_id" json:"userId"`
	Username     string    `gorm:"column:username" json:"username"`
	FullName     string    `gorm:"column:full_name" json:"fullName"`
	Password     string    `gorm:"column:password" json:"password,omitempty"`
	Avatar       string    `gorm:"column:avatar" json:"avatar"`
	Email        string    `gorm:"column:email" json:"email"`
	Phone        string    `gorm:"column:phone" json:"phone"`
	IsEnabled    int       `gorm:"column:is_enabled" json:"isEnabled"`
	IsSuperAdmin int       `gorm:"column:is_super_admin" json:"isSuperAdmin"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (UserPO) TableName() string { return "t_user" }

func (po *UserPO) ToDomain() *domain.User {
	return &domain.User{
		ID:           po.ID,
		UserID:       po.UserID,
		Username:     po.Username,
		FullName:     po.FullName,
		Password:     po.Password,
		Avatar:       po.Avatar,
		Email:        po.Email,
		Phone:        po.Phone,
		IsEnabled:    po.IsEnabled == 1,
		IsSuperAdmin: po.IsSuperAdmin == 1,
		CreatedAt:    po.CreatedAt,
		UpdatedAt:    po.UpdatedAt,
	}
}

func UserPOFromDomain(u *domain.User) *UserPO {
	isEnabled := 0
	if u.IsEnabled {
		isEnabled = 1
	}
	isSuperAdmin := 0
	if u.IsSuperAdmin {
		isSuperAdmin = 1
	}
	return &UserPO{
		ID:           u.ID,
		UserID:       u.UserID,
		Username:     u.Username,
		FullName:     u.FullName,
		Password:     u.Password,
		Avatar:       u.Avatar,
		Email:        u.Email,
		Phone:        u.Phone,
		IsEnabled:    isEnabled,
		IsSuperAdmin: isSuperAdmin,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// UserExtPO
// ---------------------------------------------------------------------------

type UserExtPO struct {
	ID               uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	UserID           string     `gorm:"column:user_id"`
	Timezone         string     `gorm:"column:timezone"`
	LastLoginAt      *time.Time `gorm:"column:last_login_at"`
	InvitationStatus string     `gorm:"column:invitation_status"`
	InvitedBy        string     `gorm:"column:invited_by"`
	InvitedAt        *time.Time `gorm:"column:invited_at"`
	AcceptedAt       *time.Time `gorm:"column:accepted_at"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (UserExtPO) TableName() string { return "t_user_ext" }

func (po *UserExtPO) ToDomain() *domain.UserExt {
	return &domain.UserExt{
		ID:               po.ID,
		UserID:           po.UserID,
		Timezone:         po.Timezone,
		LastLoginAt:      po.LastLoginAt,
		InvitationStatus: domain.InvitationStatus(po.InvitationStatus),
		InvitedBy:        po.InvitedBy,
		InvitedAt:        po.InvitedAt,
		AcceptedAt:       po.AcceptedAt,
		CreatedAt:        po.CreatedAt,
		UpdatedAt:        po.UpdatedAt,
	}
}

func UserExtPOFromDomain(ext *domain.UserExt) *UserExtPO {
	return &UserExtPO{
		ID:               ext.ID,
		UserID:           ext.UserID,
		Timezone:         ext.Timezone,
		LastLoginAt:      ext.LastLoginAt,
		InvitationStatus: string(ext.InvitationStatus),
		InvitedBy:        ext.InvitedBy,
		InvitedAt:        ext.InvitedAt,
		AcceptedAt:       ext.AcceptedAt,
		CreatedAt:        ext.CreatedAt,
		UpdatedAt:        ext.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// RolePO
// ---------------------------------------------------------------------------

type RolePO struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	RoleID      string    `gorm:"column:role_id"`
	Name        string    `gorm:"column:name"`
	DisplayName string    `gorm:"column:display_name"`
	Description string    `gorm:"column:description"`
	IsEnabled   int       `gorm:"column:is_enabled"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (RolePO) TableName() string { return "t_role" }

func (po *RolePO) ToDomain() *domain.Role {
	return &domain.Role{
		ID:          po.ID,
		RoleID:      po.RoleID,
		Name:        po.Name,
		DisplayName: po.DisplayName,
		Description: po.Description,
		IsEnabled:   po.IsEnabled == 1,
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}
}

func RolePOFromDomain(r *domain.Role) *RolePO {
	isEnabled := 0
	if r.IsEnabled {
		isEnabled = 1
	}
	return &RolePO{
		ID:          r.ID,
		RoleID:      r.RoleID,
		Name:        r.Name,
		DisplayName: r.DisplayName,
		Description: r.Description,
		IsEnabled:   isEnabled,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// MenuPO
// ---------------------------------------------------------------------------

type MenuPO struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	MenuID      string    `gorm:"column:menu_id"`
	ParentID    string    `gorm:"column:parent_id"`
	Name        string    `gorm:"column:name"`
	Path        string    `gorm:"column:path"`
	Component   string    `gorm:"column:component"`
	Icon        string    `gorm:"column:icon"`
	Order       int       `gorm:"column:order"`
	IsVisible   int       `gorm:"column:is_visible"`
	IsEnabled   int       `gorm:"column:is_enabled"`
	Description string    `gorm:"column:description"`
	Meta        string    `gorm:"column:meta"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (MenuPO) TableName() string { return "t_menu" }

func (po *MenuPO) ToDomain() *domain.Menu {
	return &domain.Menu{
		ID:          po.ID,
		MenuID:      po.MenuID,
		ParentID:    po.ParentID,
		Name:        po.Name,
		Path:        po.Path,
		Component:   po.Component,
		Icon:        po.Icon,
		Order:       po.Order,
		IsVisible:   po.IsVisible == 1,
		IsEnabled:   po.IsEnabled == 1,
		Description: po.Description,
		Meta:        po.Meta,
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}
}

func MenuPOFromDomain(m *domain.Menu) *MenuPO {
	isVisible := 0
	if m.IsVisible {
		isVisible = 1
	}
	isEnabled := 0
	if m.IsEnabled {
		isEnabled = 1
	}
	return &MenuPO{
		ID:          m.ID,
		MenuID:      m.MenuID,
		ParentID:    m.ParentID,
		Name:        m.Name,
		Path:        m.Path,
		Component:   m.Component,
		Icon:        m.Icon,
		Order:       m.Order,
		IsVisible:   isVisible,
		IsEnabled:   isEnabled,
		Description: m.Description,
		Meta:        m.Meta,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// TeamPO
// ---------------------------------------------------------------------------

type TeamPO struct {
	ID            uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	TeamID        string         `gorm:"column:team_id"`
	OrgID         string         `gorm:"column:org_id"`
	Name          string         `gorm:"column:name"`
	DisplayName   string         `gorm:"column:display_name"`
	Description   string         `gorm:"column:description"`
	Avatar        string         `gorm:"column:avatar"`
	ParentTeamID  string         `gorm:"column:parent_team_id"`
	Path          string         `gorm:"column:path"`
	Level         int            `gorm:"column:level"`
	Settings      datatypes.JSON `gorm:"column:settings"`
	Visibility    int            `gorm:"column:visibility"`
	IsEnabled     int            `gorm:"column:is_enabled"`
	TotalMembers  int            `gorm:"column:total_members"`
	TotalProjects int            `gorm:"column:total_projects"`
	CreatedAt     time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time      `gorm:"column:updated_at;autoUpdateTime"`
}

func (TeamPO) TableName() string { return "t_team" }

func (po *TeamPO) ToDomain() *domain.Team {
	var settings json.RawMessage
	if len(po.Settings) > 0 {
		settings = json.RawMessage(po.Settings)
	}
	return &domain.Team{
		ID:            po.ID,
		TeamID:        po.TeamID,
		OrgID:         po.OrgID,
		Name:          po.Name,
		DisplayName:   po.DisplayName,
		Description:   po.Description,
		Avatar:        po.Avatar,
		ParentTeamID:  po.ParentTeamID,
		Path:          po.Path,
		Level:         po.Level,
		Settings:      settings,
		Visibility:    domain.TeamVisibility(po.Visibility),
		IsEnabled:     po.IsEnabled == 1,
		TotalMembers:  po.TotalMembers,
		TotalProjects: po.TotalProjects,
		CreatedAt:     po.CreatedAt,
		UpdatedAt:     po.UpdatedAt,
	}
}

func TeamPOFromDomain(t *domain.Team) *TeamPO {
	var settings datatypes.JSON
	if t.Settings != nil {
		settings = datatypes.JSON(t.Settings)
	}
	isEnabled := 0
	if t.IsEnabled {
		isEnabled = 1
	}
	return &TeamPO{
		ID:            t.ID,
		TeamID:        t.TeamID,
		OrgID:         t.OrgID,
		Name:          t.Name,
		DisplayName:   t.DisplayName,
		Description:   t.Description,
		Avatar:        t.Avatar,
		ParentTeamID:  t.ParentTeamID,
		Path:          t.Path,
		Level:         t.Level,
		Settings:      settings,
		Visibility:    int(t.Visibility),
		IsEnabled:     isEnabled,
		TotalMembers:  t.TotalMembers,
		TotalProjects: t.TotalProjects,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// TeamMemberPO
// ---------------------------------------------------------------------------

type TeamMemberPO struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	TeamID    string    `gorm:"column:team_id"`
	UserID    string    `gorm:"column:user_id"`
	RoleID    string    `gorm:"column:role_id"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (TeamMemberPO) TableName() string { return "t_team_member" }

func (po *TeamMemberPO) ToDomain() *domain.TeamMember {
	return &domain.TeamMember{
		ID:        po.ID,
		TeamID:    po.TeamID,
		UserID:    po.UserID,
		RoleID:    po.RoleID,
		CreatedAt: po.CreatedAt,
		UpdatedAt: po.UpdatedAt,
	}
}

func TeamMemberPOFromDomain(m *domain.TeamMember) *TeamMemberPO {
	return &TeamMemberPO{
		ID:        m.ID,
		TeamID:    m.TeamID,
		UserID:    m.UserID,
		RoleID:    m.RoleID,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// UserRoleBindingPO
// ---------------------------------------------------------------------------

type UserRoleBindingPO struct {
	ID        int       `gorm:"column:id;primaryKey;autoIncrement"`
	BindingID string    `gorm:"column:binding_id"`
	UserID    string    `gorm:"column:user_id"`
	RoleID    string    `gorm:"column:role_id"`
	GrantedBy *string   `gorm:"column:granted_by"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (UserRoleBindingPO) TableName() string { return "t_user_role_binding" }

func (po *UserRoleBindingPO) ToDomain() *domain.UserRoleBinding {
	return &domain.UserRoleBinding{
		ID:        po.ID,
		BindingID: po.BindingID,
		UserID:    po.UserID,
		RoleID:    po.RoleID,
		GrantedBy: po.GrantedBy,
		CreatedAt: po.CreatedAt,
		UpdatedAt: po.UpdatedAt,
	}
}

func UserRoleBindingPOFromDomain(b *domain.UserRoleBinding) *UserRoleBindingPO {
	return &UserRoleBindingPO{
		ID:        b.ID,
		BindingID: b.BindingID,
		UserID:    b.UserID,
		RoleID:    b.RoleID,
		GrantedBy: b.GrantedBy,
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// RoleMenuBindingPO
// ---------------------------------------------------------------------------

type RoleMenuBindingPO struct {
	ID           uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	RoleMenuID   string    `gorm:"column:role_menu_id"`
	RoleID       string    `gorm:"column:role_id"`
	MenuID       string    `gorm:"column:menu_id"`
	ResourceID   string    `gorm:"column:resource_id"`
	IsVisible    int       `gorm:"column:is_visible"`
	IsAccessible int       `gorm:"column:is_accessible"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (RoleMenuBindingPO) TableName() string { return "t_role_menu_binding" }

func (po *RoleMenuBindingPO) ToDomain() *domain.RoleMenuBinding {
	return &domain.RoleMenuBinding{
		ID:           po.ID,
		RoleMenuID:   po.RoleMenuID,
		RoleID:       po.RoleID,
		MenuID:       po.MenuID,
		ResourceID:   po.ResourceID,
		IsVisible:    po.IsVisible == 1,
		IsAccessible: po.IsAccessible == 1,
		CreatedAt:    po.CreatedAt,
		UpdatedAt:    po.UpdatedAt,
	}
}

func RoleMenuBindingPOFromDomain(b *domain.RoleMenuBinding) *RoleMenuBindingPO {
	isVisible := 0
	if b.IsVisible {
		isVisible = 1
	}
	isAccessible := 0
	if b.IsAccessible {
		isAccessible = 1
	}
	return &RoleMenuBindingPO{
		ID:           b.ID,
		RoleMenuID:   b.RoleMenuID,
		RoleID:       b.RoleID,
		MenuID:       b.MenuID,
		ResourceID:   b.ResourceID,
		IsVisible:    isVisible,
		IsAccessible: isAccessible,
		CreatedAt:    b.CreatedAt,
		UpdatedAt:    b.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// IdentityProviderPO
// ---------------------------------------------------------------------------

type IdentityProviderPO struct {
	ID           uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	ProviderID   string         `gorm:"column:provider_id"`
	Name         string         `gorm:"column:name"`
	ProviderType string         `gorm:"column:provider_type"`
	Config       datatypes.JSON `gorm:"column:config"`
	Description  string         `gorm:"column:description"`
	Priority     int            `gorm:"column:priority"`
	IsEnabled    int            `gorm:"column:is_enabled"`
	CreatedAt    time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time      `gorm:"column:updated_at;autoUpdateTime"`
}

func (IdentityProviderPO) TableName() string { return "t_identity" }

func (po *IdentityProviderPO) ToDomain() *domain.IdentityProvider {
	return &domain.IdentityProvider{
		ID:           po.ID,
		ProviderID:   po.ProviderID,
		Name:         po.Name,
		ProviderType: domain.ProviderType(po.ProviderType),
		Config:       json.RawMessage(po.Config),
		Description:  po.Description,
		Priority:     po.Priority,
		IsEnabled:    po.IsEnabled == 1,
		CreatedAt:    po.CreatedAt,
		UpdatedAt:    po.UpdatedAt,
	}
}

func IdentityProviderPOFromDomain(p *domain.IdentityProvider) *IdentityProviderPO {
	isEnabled := 0
	if p.IsEnabled {
		isEnabled = 1
	}
	return &IdentityProviderPO{
		ID:           p.ID,
		ProviderID:   p.ProviderID,
		Name:         p.Name,
		ProviderType: string(p.ProviderType),
		Config:       datatypes.JSON(p.Config),
		Description:  p.Description,
		Priority:     p.Priority,
		IsEnabled:    isEnabled,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
}
