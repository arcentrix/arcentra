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

package repo

import (
	"context"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

// TemplateLibraryQuery defines query parameters for listing template libraries.
type TemplateLibraryQuery struct {
	Scope    string
	ScopeID  string
	Name     string
	Page     int
	PageSize int
}

// TemplateQuery defines query parameters for listing templates.
type TemplateQuery struct {
	Scope    string
	ScopeID  string
	Category string
	Name     string
	Tags     string
	Page     int
	PageSize int
}

// IPipelineTemplateRepository defines persistence methods for pipeline
// template libraries and templates.
type IPipelineTemplateRepository interface {
	// Library CRUD
	CreateLibrary(ctx context.Context, lib *model.PipelineTemplateLibrary) error
	UpdateLibrary(ctx context.Context, libraryID string, updates map[string]any) error
	GetLibrary(ctx context.Context, libraryID string) (*model.PipelineTemplateLibrary, error)
	DeleteLibrary(ctx context.Context, libraryID string) error
	ListLibraries(ctx context.Context, query *TemplateLibraryQuery) ([]*model.PipelineTemplateLibrary, int64, error)
	GetLibraryByName(ctx context.Context, name, scope, scopeID string) (*model.PipelineTemplateLibrary, error)

	// Template CRUD
	UpsertTemplate(ctx context.Context, tmpl *model.PipelineTemplate) error
	GetTemplate(ctx context.Context, templateID string) (*model.PipelineTemplate, error)
	GetTemplateByVersion(ctx context.Context, libraryID, name, version string) (*model.PipelineTemplate, error)
	GetLatestTemplateByName(ctx context.Context, name, scope, scopeID string) (*model.PipelineTemplate, error)
	FindTemplateByNameAndLibrary(ctx context.Context, name, libraryName, scope, scopeID string) (*model.PipelineTemplate, error)
	ListTemplates(ctx context.Context, query *TemplateQuery) ([]*model.PipelineTemplate, int64, error)
	ListTemplateVersions(ctx context.Context, libraryID, name string) ([]*model.PipelineTemplate, error)
	DeleteTemplate(ctx context.Context, templateID string) error
	DeleteTemplatesByLibrary(ctx context.Context, libraryID string) error
	ResetLatestFlag(ctx context.Context, libraryID, name string) error
	ListCategories(ctx context.Context) ([]string, error)
}

// PipelineTemplateRepo implements IPipelineTemplateRepository using GORM.
type PipelineTemplateRepo struct {
	database.IDatabase
}

// NewPipelineTemplateRepo creates a pipeline template repository.
func NewPipelineTemplateRepo(db database.IDatabase) IPipelineTemplateRepository {
	return &PipelineTemplateRepo{IDatabase: db}
}

// ---------------------------------------------------------------------------
// Library operations
// ---------------------------------------------------------------------------

// CreateLibrary persists a new template library.
func (r *PipelineTemplateRepo) CreateLibrary(ctx context.Context, lib *model.PipelineTemplateLibrary) error {
	return r.Database().WithContext(ctx).Create(lib).Error
}

// UpdateLibrary patches a template library by libraryID.
func (r *PipelineTemplateRepo) UpdateLibrary(ctx context.Context, libraryID string, updates map[string]any) error {
	return r.Database().WithContext(ctx).
		Model(&model.PipelineTemplateLibrary{}).
		Where("library_id = ?", libraryID).
		Updates(updates).Error
}

// GetLibrary returns a library by libraryID.
func (r *PipelineTemplateRepo) GetLibrary(ctx context.Context, libraryID string) (*model.PipelineTemplateLibrary, error) {
	var one model.PipelineTemplateLibrary
	if err := r.Database().WithContext(ctx).
		Where("library_id = ?", libraryID).
		First(&one).Error; err != nil {
		return nil, err
	}
	return &one, nil
}

// DeleteLibrary removes a library by libraryID.
func (r *PipelineTemplateRepo) DeleteLibrary(ctx context.Context, libraryID string) error {
	return r.Database().WithContext(ctx).
		Where("library_id = ?", libraryID).
		Delete(&model.PipelineTemplateLibrary{}).Error
}

// ListLibraries returns paginated libraries matching the query.
func (r *PipelineTemplateRepo) ListLibraries(ctx context.Context, query *TemplateLibraryQuery) ([]*model.PipelineTemplateLibrary, int64, error) {
	if query == nil {
		query = &TemplateLibraryQuery{}
	}
	normalizePage(&query.Page, &query.PageSize)

	tx := r.Database().WithContext(ctx).Model(&model.PipelineTemplateLibrary{})
	if query.Scope != "" {
		tx = tx.Where("scope = ?", query.Scope)
	}
	if query.ScopeID != "" {
		tx = tx.Where("scope_id = ?", query.ScopeID)
	}
	if strings.TrimSpace(query.Name) != "" {
		tx = tx.Where("name LIKE ?", "%"+strings.TrimSpace(query.Name)+"%")
	}

	total, err := Count(tx)
	if err != nil {
		return nil, 0, err
	}

	var list []*model.PipelineTemplateLibrary
	err = tx.Order("created_at DESC").
		Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetLibraryByName finds a library by name within the given scope.
func (r *PipelineTemplateRepo) GetLibraryByName(ctx context.Context, name, scope, scopeID string) (*model.PipelineTemplateLibrary, error) {
	var one model.PipelineTemplateLibrary
	err := r.Database().WithContext(ctx).
		Where("name = ? AND scope = ? AND scope_id = ?", name, scope, scopeID).
		First(&one).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &one, nil
}

// ---------------------------------------------------------------------------
// Template operations
// ---------------------------------------------------------------------------

// UpsertTemplate creates or updates a template record keyed by (library_id, name, version).
func (r *PipelineTemplateRepo) UpsertTemplate(ctx context.Context, tmpl *model.PipelineTemplate) error {
	var existing model.PipelineTemplate
	err := r.Database().WithContext(ctx).
		Where("library_id = ? AND name = ? AND version = ?", tmpl.LibraryID, tmpl.Name, tmpl.Version).
		First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.Database().WithContext(ctx).Create(tmpl).Error
		}
		return err
	}
	return r.Database().WithContext(ctx).
		Model(&existing).
		Updates(map[string]any{
			"description":  tmpl.Description,
			"category":     tmpl.Category,
			"tags":         tmpl.Tags,
			"icon":         tmpl.Icon,
			"readme":       tmpl.Readme,
			"params":       tmpl.Params,
			"spec_content": tmpl.SpecContent,
			"commit_sha":   tmpl.CommitSha,
			"scope":        tmpl.Scope,
			"scope_id":     tmpl.ScopeID,
			"is_latest":    tmpl.IsLatest,
			"is_published": tmpl.IsPublished,
		}).Error
}

// GetTemplate returns a template by templateID.
func (r *PipelineTemplateRepo) GetTemplate(ctx context.Context, templateID string) (*model.PipelineTemplate, error) {
	var one model.PipelineTemplate
	if err := r.Database().WithContext(ctx).
		Where("template_id = ?", templateID).
		First(&one).Error; err != nil {
		return nil, err
	}
	return &one, nil
}

// GetTemplateByVersion returns a specific version of a template.
func (r *PipelineTemplateRepo) GetTemplateByVersion(ctx context.Context, libraryID, name, version string) (*model.PipelineTemplate, error) {
	var one model.PipelineTemplate
	err := r.Database().WithContext(ctx).
		Where("library_id = ? AND name = ? AND version = ?", libraryID, name, version).
		First(&one).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &one, nil
}

// GetLatestTemplateByName returns the latest version of a template by name,
// searching across all libraries visible to the given scope.
func (r *PipelineTemplateRepo) GetLatestTemplateByName(ctx context.Context, name, scope, scopeID string) (*model.PipelineTemplate, error) {
	var one model.PipelineTemplate
	tx := r.Database().WithContext(ctx).
		Where("name = ? AND is_latest = 1 AND is_published = 1", name)
	tx = applyScopeFilter(tx, scope, scopeID)
	err := tx.Order("created_at DESC").First(&one).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &one, nil
}

// FindTemplateByNameAndLibrary finds the latest template by name within a
// specific library (matched by library name) and scope.
func (r *PipelineTemplateRepo) FindTemplateByNameAndLibrary(ctx context.Context, name, libraryName, scope, scopeID string) (*model.PipelineTemplate, error) {
	var one model.PipelineTemplate
	tx := r.Database().WithContext(ctx).
		Table("t_pipeline_template t").
		Joins("JOIN t_pipeline_template_library l ON t.library_id = l.library_id").
		Where("t.name = ? AND l.name = ? AND t.is_latest = 1 AND t.is_published = 1", name, libraryName)
	tx = applyScopeFilterAlias(tx, "t", scope, scopeID)
	err := tx.Select("t.*").First(&one).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &one, nil
}

// ListTemplates returns paginated templates matching the query.
func (r *PipelineTemplateRepo) ListTemplates(ctx context.Context, query *TemplateQuery) ([]*model.PipelineTemplate, int64, error) {
	if query == nil {
		query = &TemplateQuery{}
	}
	normalizePage(&query.Page, &query.PageSize)

	tx := r.Database().WithContext(ctx).Model(&model.PipelineTemplate{}).
		Where("is_published = 1")
	if query.Scope != "" || query.ScopeID != "" {
		tx = applyScopeFilter(tx, query.Scope, query.ScopeID)
	}
	if strings.TrimSpace(query.Category) != "" {
		tx = tx.Where("category = ?", strings.TrimSpace(query.Category))
	}
	if strings.TrimSpace(query.Name) != "" {
		tx = tx.Where("name LIKE ?", "%"+strings.TrimSpace(query.Name)+"%")
	}

	total, err := Count(tx)
	if err != nil {
		return nil, 0, err
	}

	var list []*model.PipelineTemplate
	err = tx.Order("created_at DESC").
		Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// ListTemplateVersions returns all versions of a template.
func (r *PipelineTemplateRepo) ListTemplateVersions(ctx context.Context, libraryID, name string) ([]*model.PipelineTemplate, error) {
	var list []*model.PipelineTemplate
	err := r.Database().WithContext(ctx).
		Where("library_id = ? AND name = ?", libraryID, name).
		Order("created_at DESC").
		Find(&list).Error
	return list, err
}

// DeleteTemplate removes a single template by templateID.
func (r *PipelineTemplateRepo) DeleteTemplate(ctx context.Context, templateID string) error {
	return r.Database().WithContext(ctx).
		Where("template_id = ?", templateID).
		Delete(&model.PipelineTemplate{}).Error
}

// DeleteTemplatesByLibrary removes all templates belonging to a library.
func (r *PipelineTemplateRepo) DeleteTemplatesByLibrary(ctx context.Context, libraryID string) error {
	return r.Database().WithContext(ctx).
		Where("library_id = ?", libraryID).
		Delete(&model.PipelineTemplate{}).Error
}

// ResetLatestFlag clears is_latest for all versions of a template.
func (r *PipelineTemplateRepo) ResetLatestFlag(ctx context.Context, libraryID, name string) error {
	return r.Database().WithContext(ctx).
		Model(&model.PipelineTemplate{}).
		Where("library_id = ? AND name = ? AND is_latest = 1", libraryID, name).
		Update("is_latest", 0).Error
}

// ListCategories returns distinct non-empty categories across all published templates.
func (r *PipelineTemplateRepo) ListCategories(ctx context.Context) ([]string, error) {
	var categories []string
	err := r.Database().WithContext(ctx).
		Model(&model.PipelineTemplate{}).
		Where("is_published = 1 AND category != ''").
		Distinct("category").
		Pluck("category", &categories).Error
	return categories, err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func normalizePage(page, pageSize *int) {
	if *page <= 0 {
		*page = 1
	}
	if *pageSize <= 0 {
		*pageSize = 20
	}
	if *pageSize > 100 {
		*pageSize = 100
	}
}

// applyScopeFilter adds WHERE conditions so that templates visible to the
// caller's scope are returned: system-level templates are always visible,
// plus organization/project-scoped ones matching the given scopeID.
func applyScopeFilter(tx *gorm.DB, scope, scopeID string) *gorm.DB {
	if scope == "" {
		return tx.Where("scope = ?", model.TemplateScopeSystem)
	}
	switch scope {
	case model.TemplateScopeProject:
		return tx.Where("(scope = ?) OR (scope = ? AND scope_id = ?) OR (scope = ? AND scope_id = ?)",
			model.TemplateScopeSystem,
			model.TemplateScopeOrganization, scopeID,
			model.TemplateScopeProject, scopeID,
		)
	case model.TemplateScopeOrganization:
		return tx.Where("(scope = ?) OR (scope = ? AND scope_id = ?)",
			model.TemplateScopeSystem,
			model.TemplateScopeOrganization, scopeID,
		)
	default:
		return tx.Where("scope = ?", model.TemplateScopeSystem)
	}
}

// applyScopeFilterAlias is the same as applyScopeFilter but uses a table alias prefix.
func applyScopeFilterAlias(tx *gorm.DB, alias, scope, scopeID string) *gorm.DB {
	if scope == "" {
		return tx.Where(alias+".scope = ?", model.TemplateScopeSystem)
	}
	switch scope {
	case model.TemplateScopeProject:
		return tx.Where("("+alias+".scope = ?) OR ("+alias+".scope = ? AND "+alias+".scope_id = ?) OR ("+alias+".scope = ? AND "+alias+".scope_id = ?)",
			model.TemplateScopeSystem,
			model.TemplateScopeOrganization, scopeID,
			model.TemplateScopeProject, scopeID,
		)
	case model.TemplateScopeOrganization:
		return tx.Where("("+alias+".scope = ?) OR ("+alias+".scope = ? AND "+alias+".scope_id = ?)",
			model.TemplateScopeSystem,
			model.TemplateScopeOrganization, scopeID,
		)
	default:
		return tx.Where(alias+".scope = ?", model.TemplateScopeSystem)
	}
}
