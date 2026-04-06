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

package project

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/arcentrix/arcentra/internal/domain/project"
)

// ManageProjectUseCase coordinates project lifecycle operations via IProjectRepository.
type ManageProjectUseCase struct {
	repo       project.IProjectRepository
	memberRepo project.IProjectMemberRepository
}

func NewManageProjectUseCase(repo project.IProjectRepository, memberRepo project.IProjectMemberRepository) *ManageProjectUseCase {
	return &ManageProjectUseCase{repo: repo, memberRepo: memberRepo}
}

func (uc *ManageProjectUseCase) CreateProject(ctx context.Context, in CreateProjectInput) (*project.Project, error) {
	projectID := uuid.New().String()
	p := &project.Project{
		ProjectID:     projectID,
		OrgID:         in.OrgID,
		Name:          in.Name,
		DisplayName:   in.DisplayName,
		Description:   in.Description,
		RepoURL:       in.RepoURL,
		RepoType:      in.RepoType,
		DefaultBranch: in.DefaultBranch,
		AuthType:      project.AuthType(in.AuthType),
		Credential:    in.Credential,
		Visibility:    project.ProjectVisibility(in.Visibility),
		CreatedBy:     in.CreatedBy,
		Status:        project.ProjectStatusActive,
		IsEnabled:     true,
	}
	if err := uc.repo.Create(ctx, p); err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

func (uc *ManageProjectUseCase) GetProject(ctx context.Context, projectID string) (*project.Project, error) {
	return uc.repo.Get(ctx, projectID)
}

func (uc *ManageProjectUseCase) UpdateProject(ctx context.Context, projectID string, updates map[string]any) error {
	return uc.repo.Update(ctx, projectID, updates)
}

func (uc *ManageProjectUseCase) DeleteProject(ctx context.Context, projectID string) error {
	return uc.repo.Delete(ctx, projectID)
}

func (uc *ManageProjectUseCase) ListProjects(ctx context.Context, orgID string, page, size int) ([]*project.Project, int64, error) {
	return uc.repo.List(ctx, orgID, page, size, nil)
}

// ManageSecretUseCase coordinates secret operations via ISecretRepository.
type ManageSecretUseCase struct {
	repo project.ISecretRepository
}

func NewManageSecretUseCase(repo project.ISecretRepository) *ManageSecretUseCase {
	return &ManageSecretUseCase{repo: repo}
}

func (uc *ManageSecretUseCase) CreateSecret(ctx context.Context, in CreateSecretInput) (*project.Secret, error) {
	secretID := in.SecretID
	if secretID == "" {
		secretID = uuid.New().String()
	}
	s := &project.Secret{
		SecretID:    secretID,
		Name:        in.Name,
		SecretType:  in.SecretType,
		SecretValue: in.SecretValue,
		Description: in.Description,
		Scope:       in.Scope,
		ScopeID:     in.ScopeID,
		CreatedBy:   in.CreatedBy,
	}
	if err := uc.repo.Create(ctx, s); err != nil {
		return nil, fmt.Errorf("create secret: %w", err)
	}
	return s, nil
}

func (uc *ManageSecretUseCase) GetSecret(ctx context.Context, secretID string) (*project.Secret, error) {
	return uc.repo.Get(ctx, secretID)
}

func (uc *ManageSecretUseCase) DeleteSecret(ctx context.Context, secretID string) error {
	return uc.repo.Delete(ctx, secretID)
}

func (uc *ManageSecretUseCase) ListSecrets(ctx context.Context, page, size int, scope, scopeID string) ([]*project.Secret, int64, error) {
	return uc.repo.List(ctx, page, size, "", scope, scopeID, "")
}

// ManageSettingsUseCase coordinates general settings via IGeneralSettingsRepository.
type ManageSettingsUseCase struct {
	repo project.IGeneralSettingsRepository
}

func NewManageSettingsUseCase(repo project.IGeneralSettingsRepository) *ManageSettingsUseCase {
	return &ManageSettingsUseCase{repo: repo}
}

func (uc *ManageSettingsUseCase) GetSettings(ctx context.Context, settingsID string) (*project.GeneralSettings, error) {
	return uc.repo.Get(ctx, settingsID)
}

func (uc *ManageSettingsUseCase) GetSettingsByName(ctx context.Context, category, name string) (*project.GeneralSettings, error) {
	return uc.repo.GetByName(ctx, category, name)
}

func (uc *ManageSettingsUseCase) UpdateSettings(ctx context.Context, s *project.GeneralSettings) error {
	return uc.repo.Update(ctx, s)
}

func (uc *ManageSettingsUseCase) ListSettings(
	ctx context.Context,
	page, size int,
	category string,
) ([]*project.GeneralSettings, int64, error) {
	return uc.repo.List(ctx, page, size, category)
}
