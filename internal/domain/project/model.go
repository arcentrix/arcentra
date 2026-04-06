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
)

// Project represents a CI/CD project.
type Project struct {
	ID             uint64            `json:"id"`
	ProjectID      string            `json:"projectId"`
	OrgID          string            `json:"orgId"`
	Name           string            `json:"name"`
	DisplayName    string            `json:"displayName"`
	Namespace      string            `json:"namespace"`
	Description    string            `json:"description"`
	RepoURL        string            `json:"repoUrl"`
	RepoType       string            `json:"repoType"`
	DefaultBranch  string            `json:"defaultBranch"`
	AuthType       AuthType          `json:"authType"`
	Credential     string            `json:"-"`
	TriggerMode    int               `json:"triggerMode"`
	WebhookSecret  string            `json:"-"`
	CronExpr       string            `json:"cronExpr"`
	BuildConfig    json.RawMessage   `json:"buildConfig"`
	EnvVars        json.RawMessage   `json:"envVars"`
	Settings       json.RawMessage   `json:"settings"`
	Tags           string            `json:"tags"`
	Language       string            `json:"language"`
	Framework      string            `json:"framework"`
	Status         ProjectStatus     `json:"status"`
	Visibility     ProjectVisibility `json:"visibility"`
	AccessLevel    string            `json:"accessLevel"`
	CreatedBy      string            `json:"createdBy"`
	IsEnabled      bool              `json:"isEnabled"`
	Icon           string            `json:"icon"`
	Homepage       string            `json:"homepage"`
	TotalPipelines int               `json:"totalPipelines"`
	TotalBuilds    int               `json:"totalBuilds"`
	SuccessBuilds  int               `json:"successBuilds"`
	FailedBuilds   int               `json:"failedBuilds"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// ProjectMember represents the membership of a user in a project.
type ProjectMember struct {
	ID        uint64    `json:"id"`
	ProjectID string    `json:"projectId"`
	UserID    string    `json:"userId"`
	RoleID    string    `json:"roleId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProjectTeamAccess represents a team's access level on a project.
type ProjectTeamAccess struct {
	ID          uint64          `json:"id"`
	ProjectID   string          `json:"projectId"`
	TeamID      string          `json:"teamId"`
	AccessLevel TeamAccessLevel `json:"accessLevel"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// Secret represents a secret credential.
type Secret struct {
	ID          uint64    `json:"id"`
	SecretID    string    `json:"secretId"`
	Name        string    `json:"name"`
	SecretType  string    `json:"secretType"`
	SecretValue string    `json:"-"`
	Description string    `json:"description"`
	Scope       string    `json:"scope"`
	ScopeID     string    `json:"scopeId"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// GeneralSettings represents a system-wide configuration entry.
type GeneralSettings struct {
	ID          uint64          `json:"id"`
	SettingsID  string          `json:"settingsId"`
	Category    string          `json:"category"`
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	Data        json.RawMessage `json:"data"`
	Schema      json.RawMessage `json:"schema"`
	Description string          `json:"description"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}
