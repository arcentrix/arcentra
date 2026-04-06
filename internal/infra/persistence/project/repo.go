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
	"context"

	domain "github.com/arcentrix/arcentra/internal/domain/project"
	"github.com/arcentrix/arcentra/pkg/store/database"
	"gorm.io/gorm"
)

// Compile-time interface assertions.
var (
	_ domain.IProjectRepository           = (*ProjectRepo)(nil)
	_ domain.IProjectMemberRepository     = (*ProjectMemberRepo)(nil)
	_ domain.IProjectTeamAccessRepository = (*ProjectTeamAccessRepo)(nil)
	_ domain.ISecretRepository            = (*SecretRepo)(nil)
	_ domain.IGeneralSettingsRepository   = (*GeneralSettingsRepo)(nil)
)

var projectSelectFields = []string{
	"id", "project_id", "org_id", "name", "display_name", "namespace",
	"description", "repo_url", "repo_type", "default_branch",
	"auth_type", "trigger_mode", "cron_expr",
	"build_config", "env_vars", "settings",
	"tags", "language", "framework",
	"status", "visibility", "access_level", "created_by",
	"is_enabled", "icon", "homepage",
	"total_pipelines", "total_builds", "success_builds", "failed_builds",
	"created_at", "updated_at",
}

// ---------------------------------------------------------------------------
// ProjectRepo
// ---------------------------------------------------------------------------

// ProjectRepo implements domain.IProjectRepository.
type ProjectRepo struct {
	db database.IDatabase
}

// NewProjectRepo creates a new ProjectRepo.
func NewProjectRepo(db database.IDatabase) *ProjectRepo {
	return &ProjectRepo{db: db}
}

// Create inserts a new project record.
func (r *ProjectRepo) Create(ctx context.Context, p *domain.Project) error {
	po := ProjectPOFromDomain(p)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	p.ID = po.ID
	p.CreatedAt = po.CreatedAt
	p.UpdatedAt = po.UpdatedAt
	return nil
}

// Get retrieves a project by its business ID.
func (r *ProjectRepo) Get(ctx context.Context, projectID string) (*domain.Project, error) {
	var po ProjectPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(projectSelectFields).
		Where("project_id = ?", projectID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// GetByName retrieves a project by org and name.
func (r *ProjectRepo) GetByName(ctx context.Context, orgID, name string) (*domain.Project, error) {
	var po ProjectPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(projectSelectFields).
		Where("org_id = ? AND name = ?", orgID, name).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// Update patches specific fields of a project by business ID.
func (r *ProjectRepo) Update(ctx context.Context, projectID string, updates map[string]any) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectPO{}.TableName()).
		Where("project_id = ?", projectID).
		Updates(updates).Error
}

// Delete removes a project by business ID.
func (r *ProjectRepo) Delete(ctx context.Context, projectID string) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectPO{}.TableName()).
		Where("project_id = ?", projectID).
		Delete(&ProjectPO{}).Error
}

// List returns paginated projects within an organization, optionally filtered by status.
func (r *ProjectRepo) List(ctx context.Context, orgID string, page, size int, status *domain.ProjectStatus) ([]*domain.Project, int64, error) {
	var pos []ProjectPO
	var count int64
	tbl := ProjectPO{}.TableName()
	offset := (page - 1) * size

	q := r.db.Database().WithContext(ctx).Table(tbl).Where("org_id = ?", orgID)
	if status != nil {
		q = q.Where("status = ?", int(*status))
	}

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Select(projectSelectFields).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	projects := make([]*domain.Project, len(pos))
	for i := range pos {
		projects[i] = pos[i].ToDomain()
	}
	return projects, count, nil
}

// ListByUser returns paginated projects accessible to a specific user.
func (r *ProjectRepo) ListByUser(ctx context.Context, userID string, page, size int) ([]*domain.Project, int64, error) {
	var pos []ProjectPO
	var count int64
	tbl := ProjectPO{}.TableName()
	offset := (page - 1) * size

	subQuery := r.db.Database().WithContext(ctx).
		Table(ProjectMemberPO{}.TableName()).
		Select("project_id").
		Where("user_id = ?", userID)

	q := r.db.Database().WithContext(ctx).Table(tbl).Where("project_id IN (?)", subQuery)

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Select(projectSelectFields).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	projects := make([]*domain.Project, len(pos))
	for i := range pos {
		projects[i] = pos[i].ToDomain()
	}
	return projects, count, nil
}

// Exists checks whether a project with the given business ID exists.
func (r *ProjectRepo) Exists(ctx context.Context, projectID string) (bool, error) {
	var count int64
	if err := r.db.Database().WithContext(ctx).
		Table(ProjectPO{}.TableName()).
		Where("project_id = ?", projectID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// NameExists checks whether a project name is already taken within an org,
// optionally excluding a specific project.
func (r *ProjectRepo) NameExists(ctx context.Context, orgID, name string, excludeProjectID ...string) (bool, error) {
	var count int64
	q := r.db.Database().WithContext(ctx).
		Table(ProjectPO{}.TableName()).
		Where("org_id = ? AND name = ?", orgID, name)
	if len(excludeProjectID) > 0 && excludeProjectID[0] != "" {
		q = q.Where("project_id != ?", excludeProjectID[0])
	}
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateStatistics atomically updates the pipeline/build counters for a project.
func (r *ProjectRepo) UpdateStatistics(ctx context.Context, projectID string, totalPipelines, totalBuilds, successBuilds, failedBuilds *int) error {
	updates := make(map[string]any)
	if totalPipelines != nil {
		updates["total_pipelines"] = *totalPipelines
	}
	if totalBuilds != nil {
		updates["total_builds"] = *totalBuilds
	}
	if successBuilds != nil {
		updates["success_builds"] = *successBuilds
	}
	if failedBuilds != nil {
		updates["failed_builds"] = *failedBuilds
	}
	if len(updates) == 0 {
		return nil
	}
	return r.db.Database().WithContext(ctx).
		Table(ProjectPO{}.TableName()).
		Where("project_id = ?", projectID).
		Updates(updates).Error
}

// Enable sets is_enabled = 1 for the given project.
func (r *ProjectRepo) Enable(ctx context.Context, projectID string) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectPO{}.TableName()).
		Where("project_id = ?", projectID).
		Update("is_enabled", 1).Error
}

// Disable sets is_enabled = 0 for the given project.
func (r *ProjectRepo) Disable(ctx context.Context, projectID string) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectPO{}.TableName()).
		Where("project_id = ?", projectID).
		Update("is_enabled", 0).Error
}

// ---------------------------------------------------------------------------
// ProjectMemberRepo
// ---------------------------------------------------------------------------

// ProjectMemberRepo implements domain.IProjectMemberRepository.
type ProjectMemberRepo struct {
	db database.IDatabase
}

// NewProjectMemberRepo creates a new ProjectMemberRepo.
func NewProjectMemberRepo(db database.IDatabase) *ProjectMemberRepo {
	return &ProjectMemberRepo{db: db}
}

// Get retrieves a project member by composite key.
func (r *ProjectMemberRepo) Get(ctx context.Context, projectID, userID string) (*domain.ProjectMember, error) {
	var po ProjectMemberPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("project_id = ? AND user_id = ?", projectID, userID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// ListByProject returns all members belonging to a project.
func (r *ProjectMemberRepo) ListByProject(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	var pos []ProjectMemberPO
	if err := r.db.Database().WithContext(ctx).
		Table(ProjectMemberPO{}.TableName()).
		Where("project_id = ?", projectID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	members := make([]domain.ProjectMember, len(pos))
	for i := range pos {
		members[i] = *pos[i].ToDomain()
	}
	return members, nil
}

// ListByUser returns all project memberships for a user.
func (r *ProjectMemberRepo) ListByUser(ctx context.Context, userID string) ([]domain.ProjectMember, error) {
	var pos []ProjectMemberPO
	if err := r.db.Database().WithContext(ctx).
		Table(ProjectMemberPO{}.TableName()).
		Where("user_id = ?", userID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	members := make([]domain.ProjectMember, len(pos))
	for i := range pos {
		members[i] = *pos[i].ToDomain()
	}
	return members, nil
}

// Add creates a new project member record.
func (r *ProjectMemberRepo) Add(ctx context.Context, member *domain.ProjectMember) error {
	po := ProjectMemberPOFromDomain(member)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	member.ID = po.ID
	member.CreatedAt = po.CreatedAt
	member.UpdatedAt = po.UpdatedAt
	return nil
}

// UpdateRole updates the role of a project member.
func (r *ProjectMemberRepo) UpdateRole(ctx context.Context, projectID, userID, roleID string) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectMemberPO{}.TableName()).
		Where("project_id = ? AND user_id = ?", projectID, userID).
		Update("role_id", roleID).Error
}

// Remove deletes a project member record.
func (r *ProjectMemberRepo) Remove(ctx context.Context, projectID, userID string) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectMemberPO{}.TableName()).
		Where("project_id = ? AND user_id = ?", projectID, userID).
		Delete(&ProjectMemberPO{}).Error
}

// ---------------------------------------------------------------------------
// ProjectTeamAccessRepo
// ---------------------------------------------------------------------------

// ProjectTeamAccessRepo implements domain.IProjectTeamAccessRepository.
type ProjectTeamAccessRepo struct {
	db database.IDatabase
}

// NewProjectTeamAccessRepo creates a new ProjectTeamAccessRepo.
func NewProjectTeamAccessRepo(db database.IDatabase) *ProjectTeamAccessRepo {
	return &ProjectTeamAccessRepo{db: db}
}

// Get retrieves a team's project access record.
func (r *ProjectTeamAccessRepo) Get(ctx context.Context, projectID, teamID string) (*domain.ProjectTeamAccess, error) {
	var po ProjectTeamAccessPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("project_id = ? AND team_id = ?", projectID, teamID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// ListByProject returns all team access records for a project.
func (r *ProjectTeamAccessRepo) ListByProject(ctx context.Context, projectID string) ([]domain.ProjectTeamAccess, error) {
	var pos []ProjectTeamAccessPO
	if err := r.db.Database().WithContext(ctx).
		Table(ProjectTeamAccessPO{}.TableName()).
		Where("project_id = ?", projectID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	records := make([]domain.ProjectTeamAccess, len(pos))
	for i := range pos {
		records[i] = *pos[i].ToDomain()
	}
	return records, nil
}

// ListByTeam returns all project access records for a team.
func (r *ProjectTeamAccessRepo) ListByTeam(ctx context.Context, teamID string) ([]domain.ProjectTeamAccess, error) {
	var pos []ProjectTeamAccessPO
	if err := r.db.Database().WithContext(ctx).
		Table(ProjectTeamAccessPO{}.TableName()).
		Where("team_id = ?", teamID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	records := make([]domain.ProjectTeamAccess, len(pos))
	for i := range pos {
		records[i] = *pos[i].ToDomain()
	}
	return records, nil
}

// Grant creates a new team access record for a project.
func (r *ProjectTeamAccessRepo) Grant(ctx context.Context, access *domain.ProjectTeamAccess) error {
	po := ProjectTeamAccessPOFromDomain(access)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	access.ID = po.ID
	access.CreatedAt = po.CreatedAt
	access.UpdatedAt = po.UpdatedAt
	return nil
}

// UpdateLevel modifies the access level of a team on a project.
func (r *ProjectTeamAccessRepo) UpdateLevel(ctx context.Context, projectID, teamID string, level domain.TeamAccessLevel) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectTeamAccessPO{}.TableName()).
		Where("project_id = ? AND team_id = ?", projectID, teamID).
		Update("access_level", string(level)).Error
}

// Revoke removes a team's access from a project.
func (r *ProjectTeamAccessRepo) Revoke(ctx context.Context, projectID, teamID string) error {
	return r.db.Database().WithContext(ctx).
		Table(ProjectTeamAccessPO{}.TableName()).
		Where("project_id = ? AND team_id = ?", projectID, teamID).
		Delete(&ProjectTeamAccessPO{}).Error
}

// ---------------------------------------------------------------------------
// SecretRepo
// ---------------------------------------------------------------------------

var secretSelectFields = []string{
	"id", "secret_id", "name", "secret_type",
	"description", "scope", "scope_id", "created_by",
	"created_at", "updated_at",
}

// SecretRepo implements domain.ISecretRepository.
type SecretRepo struct {
	db database.IDatabase
}

// NewSecretRepo creates a new SecretRepo.
func NewSecretRepo(db database.IDatabase) *SecretRepo {
	return &SecretRepo{db: db}
}

// Create inserts a new secret record.
func (r *SecretRepo) Create(ctx context.Context, secret *domain.Secret) error {
	po := SecretPOFromDomain(secret)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	secret.ID = po.ID
	secret.CreatedAt = po.CreatedAt
	secret.UpdatedAt = po.UpdatedAt
	return nil
}

// Update persists changes to an existing secret.
func (r *SecretRepo) Update(ctx context.Context, secret *domain.Secret) error {
	po := SecretPOFromDomain(secret)
	return r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("secret_id = ?", po.SecretID).
		Updates(po).Error
}

// Get retrieves a secret by its business ID (without the secret value).
func (r *SecretRepo) Get(ctx context.Context, secretID string) (*domain.Secret, error) {
	var po SecretPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(secretSelectFields).
		Where("secret_id = ?", secretID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// List returns paginated secrets with optional filters.
func (r *SecretRepo) List(ctx context.Context, page, size int, secretType, scope, scopeID, createdBy string) ([]*domain.Secret, int64, error) {
	var pos []SecretPO
	var count int64
	tbl := SecretPO{}.TableName()
	offset := (page - 1) * size

	q := r.db.Database().WithContext(ctx).Table(tbl)
	if secretType != "" {
		q = q.Where("secret_type = ?", secretType)
	}
	if scope != "" {
		q = q.Where("scope = ?", scope)
	}
	if scopeID != "" {
		q = q.Where("scope_id = ?", scopeID)
	}
	if createdBy != "" {
		q = q.Where("created_by = ?", createdBy)
	}

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Select(secretSelectFields).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	secrets := make([]*domain.Secret, len(pos))
	for i := range pos {
		secrets[i] = pos[i].ToDomain()
	}
	return secrets, count, nil
}

// Delete removes a secret by its business ID.
func (r *SecretRepo) Delete(ctx context.Context, secretID string) error {
	return r.db.Database().WithContext(ctx).
		Table(SecretPO{}.TableName()).
		Where("secret_id = ?", secretID).
		Delete(&SecretPO{}).Error
}

// ListByScope returns all secrets matching a given scope.
func (r *SecretRepo) ListByScope(ctx context.Context, scope, scopeID string) ([]*domain.Secret, error) {
	var pos []SecretPO
	if err := r.db.Database().WithContext(ctx).
		Table(SecretPO{}.TableName()).
		Select(secretSelectFields).
		Where("scope = ? AND scope_id = ?", scope, scopeID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	secrets := make([]*domain.Secret, len(pos))
	for i := range pos {
		secrets[i] = pos[i].ToDomain()
	}
	return secrets, nil
}

// GetValue retrieves the encrypted secret value only.
func (r *SecretRepo) GetValue(ctx context.Context, secretID string) (string, error) {
	var po SecretPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select("secret_value").
		Where("secret_id = ?", secretID).
		First(&po).Error; err != nil {
		return "", err
	}
	return po.SecretValue, nil
}

// ---------------------------------------------------------------------------
// GeneralSettingsRepo
// ---------------------------------------------------------------------------

// GeneralSettingsRepo implements domain.IGeneralSettingsRepository.
type GeneralSettingsRepo struct {
	db database.IDatabase
}

// NewGeneralSettingsRepo creates a new GeneralSettingsRepo.
func NewGeneralSettingsRepo(db database.IDatabase) *GeneralSettingsRepo {
	return &GeneralSettingsRepo{db: db}
}

// Update persists changes to a general settings entry.
func (r *GeneralSettingsRepo) Update(ctx context.Context, settings *domain.GeneralSettings) error {
	po := GeneralSettingsPOFromDomain(settings)
	return r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("settings_id = ?", po.SettingsID).
		Updates(po).Error
}

// Get retrieves a settings entry by its business ID.
func (r *GeneralSettingsRepo) Get(ctx context.Context, settingsID string) (*domain.GeneralSettings, error) {
	var po GeneralSettingsPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("settings_id = ?", settingsID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// GetByName retrieves a settings entry by category and name.
func (r *GeneralSettingsRepo) GetByName(ctx context.Context, category, name string) (*domain.GeneralSettings, error) {
	var po GeneralSettingsPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("category = ? AND name = ?", category, name).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

// List returns paginated settings entries, optionally filtered by category.
func (r *GeneralSettingsRepo) List(ctx context.Context, page, size int, category string) ([]*domain.GeneralSettings, int64, error) {
	var pos []GeneralSettingsPO
	var count int64
	tbl := GeneralSettingsPO{}.TableName()
	offset := (page - 1) * size

	q := r.db.Database().WithContext(ctx).Table(tbl)
	if category != "" {
		q = q.Where("category = ?", category)
	}

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Offset(offset).Limit(size).
		Order("id ASC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	settings := make([]*domain.GeneralSettings, len(pos))
	for i := range pos {
		settings[i] = pos[i].ToDomain()
	}
	return settings, count, nil
}

// GetCategories returns all distinct category values.
func (r *GeneralSettingsRepo) GetCategories(ctx context.Context) ([]string, error) {
	var categories []string
	if err := r.db.Database().WithContext(ctx).
		Table(GeneralSettingsPO{}.TableName()).
		Distinct("category").
		Pluck("category", &categories).Error; err != nil {
		return nil, err
	}
	return categories, nil
}

// suppress unused import warnings
var _ *gorm.DB
