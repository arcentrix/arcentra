// Copyright 2026 Arcentra Authors.
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
	"time"

	"gorm.io/datatypes"
)

// PipelineTemplateLibrary represents a registered Git repository that serves
// as a template library source. Templates are synced from the repository
// into the database for fast indexing and querying.
type PipelineTemplateLibrary struct {
	BaseModel
	LibraryID       string     `gorm:"column:library_id;type:varchar(36);not null;uniqueIndex:uk_library_id" json:"libraryId"`
	Name            string     `gorm:"column:name;type:varchar(255);not null" json:"name"`
	Description     string     `gorm:"column:description;type:text" json:"description"`
	RepoURL         string     `gorm:"column:repo_url;type:varchar(512);not null" json:"repoUrl"`
	DefaultRef      string     `gorm:"column:default_ref;type:varchar(255);not null;default:main" json:"defaultRef"`
	AuthType        int        `gorm:"column:auth_type;type:tinyint;not null;default:0" json:"authType"`
	CredentialID    string     `gorm:"column:credential_id;type:varchar(36);not null;default:''" json:"credentialId"`
	Scope           string     `gorm:"column:scope;type:varchar(32);not null;default:system;index:idx_scope" json:"scope"`
	ScopeID         string     `gorm:"column:scope_id;type:varchar(36);not null;default:'';index:idx_scope" json:"scopeId"`
	SyncInterval    int        `gorm:"column:sync_interval;not null;default:0" json:"syncInterval"`
	LastSyncStatus  int        `gorm:"column:last_sync_status;type:tinyint;not null;default:0" json:"lastSyncStatus"`
	LastSyncMessage string     `gorm:"column:last_sync_message;type:text" json:"lastSyncMessage"`
	LastSyncedAt    *time.Time `gorm:"column:last_synced_at" json:"lastSyncedAt"`
	TemplateDir     string     `gorm:"column:template_dir;type:varchar(255);not null;default:templates" json:"templateDir"`
	CreatedBy       string     `gorm:"column:created_by;type:varchar(36);not null;default:''" json:"createdBy"`
	IsEnabled       int        `gorm:"column:is_enabled;type:tinyint(1);not null;default:1" json:"isEnabled"`
}

// TableName returns the database table name.
func (PipelineTemplateLibrary) TableName() string {
	return "pipeline_template_library"
}

// PipelineTemplate represents a single template entry synced from a library
// Git repository. Each version of a template corresponds to one record.
type PipelineTemplate struct {
	BaseModel
	TemplateID  string         `gorm:"column:template_id;type:varchar(36);not null;uniqueIndex:uk_template_id" json:"templateId"`
	LibraryID   string         `gorm:"column:library_id;type:varchar(36);not null;uniqueIndex:uk_lib_name_ver" json:"libraryId"`
	Name        string         `gorm:"column:name;type:varchar(255);not null;uniqueIndex:uk_lib_name_ver" json:"name"`
	Description string         `gorm:"column:description;type:text" json:"description"`
	Category    string         `gorm:"column:category;type:varchar(128);not null;default:'';index:idx_category" json:"category"`
	Tags        datatypes.JSON `gorm:"column:tags;type:json" json:"tags"`
	Icon        string         `gorm:"column:icon;type:varchar(512);not null;default:''" json:"icon"`
	Readme      string         `gorm:"column:readme;type:text" json:"readme"`
	Params      datatypes.JSON `gorm:"column:params;type:json" json:"params"`
	SpecContent string         `gorm:"column:spec_content;type:mediumtext;not null" json:"specContent"`
	Version     string         `gorm:"column:version;type:varchar(64);not null;uniqueIndex:uk_lib_name_ver" json:"version"`
	CommitSha   string         `gorm:"column:commit_sha;type:varchar(64);not null;default:''" json:"commitSha"`
	Scope       string         `gorm:"column:scope;type:varchar(32);not null;default:system;index:idx_scope" json:"scope"`
	ScopeID     string         `gorm:"column:scope_id;type:varchar(36);not null;default:'';index:idx_scope" json:"scopeId"`
	IsLatest    int            `gorm:"column:is_latest;type:tinyint(1);not null;default:0;index:idx_latest" json:"isLatest"`
	IsPublished int            `gorm:"column:is_published;type:tinyint(1);not null;default:1" json:"isPublished"`
}

// TableName returns the database table name.
func (PipelineTemplate) TableName() string {
	return "pipeline_template"
}

// TemplateParam describes a single parameter that a template accepts.
// Stored as JSON array in PipelineTemplate.Params.
type TemplateParam struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // string / boolean / number / enum
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required"`
	Options     []string `json:"options,omitempty"` // valid values when Type is enum
}

// Template library scope constants.
const (
	TemplateScopeSystem       = "system"
	TemplateScopeOrganization = "organization"
	TemplateScopeProject      = "project"
)

// Template library auth type constants.
const (
	TemplateAuthNone     = 0
	TemplateAuthToken    = 1
	TemplateAuthPassword = 2
	TemplateAuthSSHKey   = 3
)

// Template library sync status constants.
const (
	TemplateSyncStatusUnknown = 0
	TemplateSyncStatusSuccess = 1
	TemplateSyncStatusFailed  = 2
	TemplateSyncStatusSyncing = 3
)

// Template category constants.
const (
	TemplateCategoryCI     = "ci"
	TemplateCategoryCD     = "cd"
	TemplateCategoryBuild  = "build"
	TemplateCategoryTest   = "test"
	TemplateCategoryDeploy = "deploy"
	TemplateCategoryCustom = "custom"
)
