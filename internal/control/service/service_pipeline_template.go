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

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	tmpl "github.com/arcentrix/arcentra/internal/pkg/pipeline/template"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/bytedance/sonic"
	"go.yaml.in/yaml/v3"
	"gorm.io/datatypes"
)

// PipelineTemplateService manages pipeline template libraries and templates.
// It implements tmpl.ITemplateResolver so it can be injected into the
// include expansion pipeline.
type PipelineTemplateService struct {
	templateRepo repo.IPipelineTemplateRepository
	secretRepo   repo.ISecretRepository
}

// NewPipelineTemplateService creates a PipelineTemplateService.
func NewPipelineTemplateService(
	templateRepo repo.IPipelineTemplateRepository,
	secretRepo repo.ISecretRepository,
) *PipelineTemplateService {
	return &PipelineTemplateService{
		templateRepo: templateRepo,
		secretRepo:   secretRepo,
	}
}

// ---------------------------------------------------------------------------
// Library management
// ---------------------------------------------------------------------------

// RegisterLibrary registers a new Git repository as a template library.
func (s *PipelineTemplateService) RegisterLibrary(ctx context.Context, lib *model.PipelineTemplateLibrary) error {
	if strings.TrimSpace(lib.Name) == "" {
		return fmt.Errorf("library name is required")
	}
	if strings.TrimSpace(lib.RepoURL) == "" {
		return fmt.Errorf("repo url is required")
	}
	if lib.LibraryID == "" {
		lib.LibraryID = id.GetUild()
	}
	if lib.DefaultRef == "" {
		lib.DefaultRef = "main"
	}
	if lib.TemplateDir == "" {
		lib.TemplateDir = "templates"
	}
	if lib.Scope == "" {
		lib.Scope = model.TemplateScopeSystem
	}
	return s.templateRepo.CreateLibrary(ctx, lib)
}

// GetLibrary returns a library by ID.
func (s *PipelineTemplateService) GetLibrary(ctx context.Context, libraryID string) (*model.PipelineTemplateLibrary, error) {
	return s.templateRepo.GetLibrary(ctx, libraryID)
}

// UpdateLibrary patches a library.
func (s *PipelineTemplateService) UpdateLibrary(ctx context.Context, libraryID string, updates map[string]any) error {
	return s.templateRepo.UpdateLibrary(ctx, libraryID, updates)
}

// DeleteLibrary removes a library and all its cached templates.
func (s *PipelineTemplateService) DeleteLibrary(ctx context.Context, libraryID string) error {
	if err := s.templateRepo.DeleteTemplatesByLibrary(ctx, libraryID); err != nil {
		return fmt.Errorf("delete templates: %w", err)
	}
	return s.templateRepo.DeleteLibrary(ctx, libraryID)
}

// ListLibraries lists libraries matching the query.
func (s *PipelineTemplateService) ListLibraries(ctx context.Context, query *repo.TemplateLibraryQuery) ([]*model.PipelineTemplateLibrary, int64, error) {
	return s.templateRepo.ListLibraries(ctx, query)
}

// ---------------------------------------------------------------------------
// Sync
// ---------------------------------------------------------------------------

// SyncLibrary synchronises a library from its Git repository to the DB cache.
func (s *PipelineTemplateService) SyncLibrary(ctx context.Context, libraryID string) error {
	lib, err := s.templateRepo.GetLibrary(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("get library: %w", err)
	}

	_ = s.templateRepo.UpdateLibrary(ctx, libraryID, map[string]any{
		"last_sync_status": model.TemplateSyncStatusSyncing,
	})

	syncErr := s.doSync(ctx, lib)

	now := time.Now()
	updates := map[string]any{"last_synced_at": &now}
	if syncErr != nil {
		updates["last_sync_status"] = model.TemplateSyncStatusFailed
		updates["last_sync_message"] = syncErr.Error()
		_ = s.templateRepo.UpdateLibrary(ctx, libraryID, updates)
		return syncErr
	}
	updates["last_sync_status"] = model.TemplateSyncStatusSuccess
	updates["last_sync_message"] = ""
	_ = s.templateRepo.UpdateLibrary(ctx, libraryID, updates)
	return nil
}

// doSync performs the actual Git clone + discover + DB upsert logic.
func (s *PipelineTemplateService) doSync(ctx context.Context, lib *model.PipelineTemplateLibrary) error {
	auth, err := s.resolveAuth(ctx, lib)
	if err != nil {
		return err
	}

	req := tmpl.SyncRequest{
		RepoURL:     lib.RepoURL,
		Ref:         lib.DefaultRef,
		Auth:        auth,
		TemplateDir: lib.TemplateDir,
	}

	headSha, _, discovered, err := tmpl.CloneAndDiscover(req)
	if err != nil {
		return err
	}

	version := tmpl.BranchVersion(lib.DefaultRef)

	for _, dt := range discovered {
		paramsJSON, _ := sonic.Marshal(dt.Manifest.Params)
		tagsJSON, _ := sonic.Marshal(dt.Manifest.Tags)

		if err := s.templateRepo.ResetLatestFlag(ctx, lib.LibraryID, dt.Manifest.Name); err != nil {
			log.Warnw("reset latest flag failed", "library", lib.LibraryID, "template", dt.Manifest.Name, "error", err)
		}

		t := &model.PipelineTemplate{
			TemplateID:  id.GetUild(),
			LibraryID:   lib.LibraryID,
			Name:        dt.Manifest.Name,
			Description: dt.Manifest.Description,
			Category:    dt.Manifest.Category,
			Tags:        datatypes.JSON(tagsJSON),
			Icon:        dt.Manifest.Icon,
			Readme:      dt.Readme,
			Params:      datatypes.JSON(paramsJSON),
			SpecContent: dt.SpecContent,
			Version:     version,
			CommitSha:   headSha,
			Scope:       lib.Scope,
			ScopeID:     lib.ScopeID,
			IsLatest:    1,
			IsPublished: 1,
		}
		if err := s.templateRepo.UpsertTemplate(ctx, t); err != nil {
			log.Warnw("upsert template failed", "library", lib.LibraryID, "template", dt.Manifest.Name, "error", err)
		}
	}

	log.Infow("library sync completed", "libraryId", lib.LibraryID, "templates", len(discovered), "commit", headSha)
	return nil
}

// ---------------------------------------------------------------------------
// Template queries
// ---------------------------------------------------------------------------

// GetTemplate returns a single template by ID.
func (s *PipelineTemplateService) GetTemplate(ctx context.Context, templateID string) (*model.PipelineTemplate, error) {
	return s.templateRepo.GetTemplate(ctx, templateID)
}

// ListTemplates returns templates matching the query.
func (s *PipelineTemplateService) ListTemplates(ctx context.Context, query *repo.TemplateQuery) ([]*model.PipelineTemplate, int64, error) {
	return s.templateRepo.ListTemplates(ctx, query)
}

// ListTemplateVersions returns all versions of a template.
func (s *PipelineTemplateService) ListTemplateVersions(ctx context.Context, templateID string) ([]*model.PipelineTemplate, error) {
	t, err := s.templateRepo.GetTemplate(ctx, templateID)
	if err != nil {
		return nil, err
	}
	return s.templateRepo.ListTemplateVersions(ctx, t.LibraryID, t.Name)
}

// ListCategories returns distinct template categories.
func (s *PipelineTemplateService) ListCategories(ctx context.Context) ([]string, error) {
	return s.templateRepo.ListCategories(ctx)
}

// ---------------------------------------------------------------------------
// Template write-back (edit -> push to Git -> update DB)
// ---------------------------------------------------------------------------

// SaveTemplateRequest holds the data for saving a template back to Git.
type SaveTemplateRequest struct {
	TemplateID    string
	SpecContent   string
	Name          string
	Description   string
	Category      string
	Tags          []string
	Params        []tmpl.ParamSchema
	CommitMessage string
	Editor        string
}

// SaveTemplate edits a template and pushes changes back to the Git repository.
func (s *PipelineTemplateService) SaveTemplate(ctx context.Context, req SaveTemplateRequest) (string, error) {
	t, err := s.templateRepo.GetTemplate(ctx, req.TemplateID)
	if err != nil {
		return "", fmt.Errorf("get template: %w", err)
	}
	lib, err := s.templateRepo.GetLibrary(ctx, t.LibraryID)
	if err != nil {
		return "", fmt.Errorf("get library: %w", err)
	}

	auth, err := s.resolveAuth(ctx, lib)
	if err != nil {
		return "", err
	}

	workdir, err := os.MkdirTemp("", "arcentra-template-save-*")
	if err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if cloneErr := scm.Clone(scm.GitCloneRequest{
		Workdir: workdir,
		RepoURL: lib.RepoURL,
		Branch:  lib.DefaultRef,
		Auth:    auth,
	}); cloneErr != nil {
		return "", fmt.Errorf("clone repo: %w", cloneErr)
	}

	tmplDir := filepath.Join(workdir, lib.TemplateDir, t.Name)
	if mkErr := os.MkdirAll(tmplDir, 0o755); mkErr != nil {
		return "", fmt.Errorf("mkdir: %w", mkErr)
	}

	manifest := tmpl.TemplateManifest{
		Name:        orDefault(req.Name, t.Name),
		Description: orDefault(req.Description, t.Description),
		Category:    orDefault(req.Category, t.Category),
		Tags:        req.Tags,
		Params:      req.Params,
	}
	manifestBytes, _ := yaml.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(tmplDir, "template.yaml"), manifestBytes, 0o644); err != nil {
		return "", fmt.Errorf("write template.yaml: %w", err)
	}

	specContent := req.SpecContent
	if specContent == "" {
		specContent = t.SpecContent
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "spec.yaml"), []byte(specContent), 0o644); err != nil {
		return "", fmt.Errorf("write spec.yaml: %w", err)
	}

	if err := scm.Add(scm.GitAddRequest{Workdir: workdir, FilePath: "."}); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}
	commitMsg := req.CommitMessage
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("chore: update template %s", t.Name)
	}
	if err := scm.Commit(scm.GitCommitRequest{Workdir: workdir, Message: commitMsg, Author: req.Editor}); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	if err := scm.Push(scm.GitPushRequest{Workdir: workdir, Remote: "origin", Branch: lib.DefaultRef, Auth: auth}); err != nil {
		return "", fmt.Errorf("git push: %w", err)
	}

	commitSha, _ := scm.HeadSHA(scm.GitHeadSHARequest{Workdir: workdir})

	paramsJSON, _ := sonic.Marshal(manifest.Params)
	tagsJSON, _ := sonic.Marshal(manifest.Tags)
	_ = s.templateRepo.UpsertTemplate(ctx, &model.PipelineTemplate{
		TemplateID:  t.TemplateID,
		LibraryID:   t.LibraryID,
		Name:        manifest.Name,
		Description: manifest.Description,
		Category:    manifest.Category,
		Tags:        datatypes.JSON(tagsJSON),
		Params:      datatypes.JSON(paramsJSON),
		SpecContent: specContent,
		Version:     t.Version,
		CommitSha:   commitSha,
		Scope:       t.Scope,
		ScopeID:     t.ScopeID,
		IsLatest:    t.IsLatest,
		IsPublished: t.IsPublished,
	})

	return commitSha, nil
}

// CreateTemplateRequest holds data for creating a new template in a library.
type CreateTemplateRequest struct {
	LibraryID     string
	Name          string
	Description   string
	Category      string
	Tags          []string
	Params        []tmpl.ParamSchema
	SpecContent   string
	CommitMessage string
	Editor        string
}

// CreateTemplateInLibrary creates a new template directory in the Git repository and pushes it.
func (s *PipelineTemplateService) CreateTemplateInLibrary(ctx context.Context, req CreateTemplateRequest) (*model.PipelineTemplate, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("template name is required")
	}
	if strings.TrimSpace(req.SpecContent) == "" {
		return nil, fmt.Errorf("spec content is required")
	}

	lib, err := s.templateRepo.GetLibrary(ctx, req.LibraryID)
	if err != nil {
		return nil, fmt.Errorf("get library: %w", err)
	}

	auth, err := s.resolveAuth(ctx, lib)
	if err != nil {
		return nil, err
	}

	workdir, err := os.MkdirTemp("", "arcentra-template-create-*")
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if cloneErr := scm.Clone(scm.GitCloneRequest{
		Workdir: workdir,
		RepoURL: lib.RepoURL,
		Branch:  lib.DefaultRef,
		Auth:    auth,
	}); cloneErr != nil {
		return nil, fmt.Errorf("clone repo: %w", cloneErr)
	}

	tmplDir := filepath.Join(workdir, lib.TemplateDir, req.Name)
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	manifest := tmpl.TemplateManifest{
		Name:        req.Name,
		Description: req.Description,
		Category:    req.Category,
		Tags:        req.Tags,
		Params:      req.Params,
	}
	manifestBytes, _ := yaml.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(tmplDir, "template.yaml"), manifestBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write template.yaml: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "spec.yaml"), []byte(req.SpecContent), 0o644); err != nil {
		return nil, fmt.Errorf("write spec.yaml: %w", err)
	}

	if err := scm.Add(scm.GitAddRequest{Workdir: workdir, FilePath: "."}); err != nil {
		return nil, fmt.Errorf("git add: %w", err)
	}
	commitMsg := req.CommitMessage
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("feat: add template %s", req.Name)
	}
	if err := scm.Commit(scm.GitCommitRequest{Workdir: workdir, Message: commitMsg, Author: req.Editor}); err != nil {
		return nil, fmt.Errorf("git commit: %w", err)
	}
	if err := scm.Push(scm.GitPushRequest{Workdir: workdir, Remote: "origin", Branch: lib.DefaultRef, Auth: auth}); err != nil {
		return nil, fmt.Errorf("git push: %w", err)
	}

	commitSha, _ := scm.HeadSHA(scm.GitHeadSHARequest{Workdir: workdir})

	version := tmpl.BranchVersion(lib.DefaultRef)
	paramsJSON, _ := sonic.Marshal(req.Params)
	tagsJSON, _ := sonic.Marshal(req.Tags)

	_ = s.templateRepo.ResetLatestFlag(ctx, lib.LibraryID, req.Name)

	t := &model.PipelineTemplate{
		TemplateID:  id.GetUild(),
		LibraryID:   lib.LibraryID,
		Name:        req.Name,
		Description: req.Description,
		Category:    req.Category,
		Tags:        datatypes.JSON(tagsJSON),
		Params:      datatypes.JSON(paramsJSON),
		SpecContent: req.SpecContent,
		Version:     version,
		CommitSha:   commitSha,
		Scope:       lib.Scope,
		ScopeID:     lib.ScopeID,
		IsLatest:    1,
		IsPublished: 1,
	}
	if err := s.templateRepo.UpsertTemplate(ctx, t); err != nil {
		return nil, fmt.Errorf("save template: %w", err)
	}
	return t, nil
}

// DeleteTemplateFromLibrary removes a template directory from the Git repo and DB.
func (s *PipelineTemplateService) DeleteTemplateFromLibrary(ctx context.Context, templateID, commitMessage, editor string) error {
	t, err := s.templateRepo.GetTemplate(ctx, templateID)
	if err != nil {
		return fmt.Errorf("get template: %w", err)
	}
	lib, err := s.templateRepo.GetLibrary(ctx, t.LibraryID)
	if err != nil {
		return fmt.Errorf("get library: %w", err)
	}

	auth, err := s.resolveAuth(ctx, lib)
	if err != nil {
		return err
	}

	workdir, err := os.MkdirTemp("", "arcentra-template-delete-*")
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if cloneErr := scm.Clone(scm.GitCloneRequest{
		Workdir: workdir,
		RepoURL: lib.RepoURL,
		Branch:  lib.DefaultRef,
		Auth:    auth,
	}); cloneErr != nil {
		return fmt.Errorf("clone repo: %w", cloneErr)
	}

	tmplDir := filepath.Join(workdir, lib.TemplateDir, t.Name)
	if err := os.RemoveAll(tmplDir); err != nil {
		return fmt.Errorf("remove template dir: %w", err)
	}

	if err := scm.Add(scm.GitAddRequest{Workdir: workdir, FilePath: "."}); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("chore: remove template %s", t.Name)
	}
	if err := scm.Commit(scm.GitCommitRequest{Workdir: workdir, Message: commitMessage, Author: editor}); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if err := scm.Push(scm.GitPushRequest{Workdir: workdir, Remote: "origin", Branch: lib.DefaultRef, Auth: auth}); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	return s.templateRepo.DeleteTemplate(ctx, templateID)
}

// ---------------------------------------------------------------------------
// Template instantiation
// ---------------------------------------------------------------------------

// InstantiateRequest holds data for generating a pipeline spec from a template.
type InstantiateRequest struct {
	TemplateID string
	Version    string
	Params     map[string]any
}

// InstantiateTemplate renders a template with the given parameters and
// returns the generated pipeline spec content (YAML).
func (s *PipelineTemplateService) InstantiateTemplate(ctx context.Context, req InstantiateRequest) (string, error) {
	t, err := s.templateRepo.GetTemplate(ctx, req.TemplateID)
	if err != nil {
		return "", fmt.Errorf("get template: %w", err)
	}

	if req.Version != "" && req.Version != t.Version {
		versioned, vErr := s.templateRepo.GetTemplateByVersion(ctx, t.LibraryID, t.Name, req.Version)
		if vErr != nil || versioned == nil {
			return "", fmt.Errorf("version %s not found for template %s", req.Version, t.Name)
		}
		t = versioned
	}

	var schema []tmpl.ParamSchema
	if len(t.Params) > 0 {
		_ = sonic.Unmarshal(t.Params, &schema)
	}

	rendered, err := tmpl.RenderSpec(t.SpecContent, req.Params, schema)
	if err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}

	return rendered, nil
}

// ---------------------------------------------------------------------------
// ITemplateResolver implementation (for include expansion)
// ---------------------------------------------------------------------------

// ResolveTemplate implements tmpl.ITemplateResolver. It looks up a template
// by name/version/library within the visible scope, renders it with params,
// and returns the rendered spec content.
func (s *PipelineTemplateService) ResolveTemplate(ctx context.Context, name, version, library string, params map[string]any, scope, scopeID string) (string, error) {
	var t *model.PipelineTemplate
	var err error

	if library != "" {
		t, err = s.templateRepo.FindTemplateByNameAndLibrary(ctx, name, library, scope, scopeID)
	} else if version != "" {
		t, err = s.findTemplateByNameVersionScope(ctx, name, version, scope, scopeID)
	} else {
		t, err = s.templateRepo.GetLatestTemplateByName(ctx, name, scope, scopeID)
	}
	if err != nil {
		return "", fmt.Errorf("resolve template %q: %w", name, err)
	}
	if t == nil {
		return "", fmt.Errorf("template %q not found", name)
	}

	if version != "" && version != t.Version {
		versioned, vErr := s.templateRepo.GetTemplateByVersion(ctx, t.LibraryID, t.Name, version)
		if vErr != nil || versioned == nil {
			return "", fmt.Errorf("version %s not found for template %s", version, name)
		}
		t = versioned
	}

	var schema []tmpl.ParamSchema
	if len(t.Params) > 0 {
		_ = sonic.Unmarshal(t.Params, &schema)
	}

	return tmpl.RenderSpec(t.SpecContent, params, schema)
}

// findTemplateByNameVersionScope searches for a template with a specific version across visible scopes.
func (s *PipelineTemplateService) findTemplateByNameVersionScope(ctx context.Context, name, version, scope, scopeID string) (*model.PipelineTemplate, error) {
	libs, _, err := s.templateRepo.ListLibraries(ctx, &repo.TemplateLibraryQuery{
		Scope:    scope,
		ScopeID:  scopeID,
		PageSize: 100,
	})
	if err != nil {
		return nil, err
	}
	for _, lib := range libs {
		t, tErr := s.templateRepo.GetTemplateByVersion(ctx, lib.LibraryID, name, version)
		if tErr == nil && t != nil {
			return t, nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// resolveAuth builds GitAuth credentials for a library by reading the
// referenced secret from the secret repository.
func (s *PipelineTemplateService) resolveAuth(ctx context.Context, lib *model.PipelineTemplateLibrary) (scm.GitAuth, error) {
	if lib.AuthType == model.TemplateAuthNone || lib.CredentialID == "" {
		return scm.GitAuth{}, nil
	}
	if s.secretRepo == nil {
		return scm.GitAuth{}, nil
	}
	secret, err := s.secretRepo.Get(ctx, lib.CredentialID)
	if err != nil {
		return scm.GitAuth{}, fmt.Errorf("resolve credential %s: %w", lib.CredentialID, err)
	}
	if secret == nil {
		return scm.GitAuth{}, nil
	}
	switch lib.AuthType {
	case model.TemplateAuthToken:
		return scm.GitAuth{Token: secret.SecretValue}, nil
	case model.TemplateAuthPassword:
		parts := strings.SplitN(secret.SecretValue, ":", 2)
		if len(parts) == 2 {
			return scm.GitAuth{Username: parts[0], Password: parts[1]}, nil
		}
		return scm.GitAuth{Token: secret.SecretValue}, nil
	case model.TemplateAuthSSHKey:
		return scm.GitAuth{SSHKey: secret.SecretValue}, nil
	default:
		return scm.GitAuth{}, nil
	}
}

func orDefault(val, def string) string {
	if strings.TrimSpace(val) != "" {
		return val
	}
	return def
}
