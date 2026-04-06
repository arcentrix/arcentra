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

	"github.com/arcentrix/arcentra/internal/domain/project"
	"github.com/google/uuid"
)

func (uc *ManageProjectUseCase) CreateProjectFull(
	ctx context.Context,
	orgID, name, displayName, description, repoURL, repoType, defaultBranch string,
	authType int, credential string, visibility int, createdBy string,
) (*project.Project, error) {
	return uc.CreateProject(ctx, CreateProjectInput{
		OrgID:         orgID,
		Name:          name,
		DisplayName:   displayName,
		Description:   description,
		RepoURL:       repoURL,
		RepoType:      repoType,
		DefaultBranch: defaultBranch,
		AuthType:      authType,
		Credential:    credential,
		Visibility:    visibility,
		CreatedBy:     createdBy,
	})
}

func (uc *ManageProjectUseCase) GetProjectsByUserID(ctx context.Context, userID string, page, size int) ([]*project.Project, int64, error) {
	if uc.memberRepo == nil {
		return uc.repo.ListByUser(ctx, userID, page, size)
	}
	return uc.repo.ListByUser(ctx, userID, page, size)
}

func (uc *ManageProjectUseCase) GetProjectMembers(ctx context.Context, projectID string) ([]project.ProjectMember, error) {
	if uc.memberRepo == nil {
		return nil, fmt.Errorf("project member repository not configured")
	}
	return uc.memberRepo.ListByProject(ctx, projectID)
}

func (uc *ManageProjectUseCase) AddProjectMember(ctx context.Context, projectID, userID, roleID string) error {
	if uc.memberRepo == nil {
		return fmt.Errorf("project member repository not configured")
	}
	return uc.memberRepo.Add(ctx, &project.ProjectMember{
		ProjectID: projectID,
		UserID:    userID,
		RoleID:    roleID,
	})
}

func (uc *ManageProjectUseCase) UpdateProjectMemberRole(ctx context.Context, projectID, userID, roleID string) error {
	if uc.memberRepo == nil {
		return fmt.Errorf("project member repository not configured")
	}
	return uc.memberRepo.UpdateRole(ctx, projectID, userID, roleID)
}

func (uc *ManageProjectUseCase) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	if uc.memberRepo == nil {
		return fmt.Errorf("project member repository not configured")
	}
	return uc.memberRepo.Remove(ctx, projectID, userID)
}

func (uc *ManageSecretUseCase) UpdateSecret(ctx context.Context, secretID string, updates map[string]any) error {
	secret, err := uc.repo.Get(ctx, secretID)
	if err != nil {
		return fmt.Errorf("get secret: %w", err)
	}
	if name, ok := updates["name"].(string); ok && name != "" {
		secret.Name = name
	}
	if desc, ok := updates["description"].(string); ok {
		secret.Description = desc
	}
	if val, ok := updates["secretValue"].(string); ok && val != "" {
		secret.SecretValue = val
	}
	return uc.repo.Update(ctx, secret)
}

func (uc *ManageSecretUseCase) GetSecretValue(ctx context.Context, secretID string) (string, error) {
	return uc.repo.GetValue(ctx, secretID)
}

func (uc *ManageSecretUseCase) ListSecretsFiltered(
	ctx context.Context,
	page, size int,
	secretType, scope, scopeID, createdBy string,
) ([]*project.Secret, int64, error) {
	return uc.repo.List(ctx, page, size, secretType, scope, scopeID, createdBy)
}

func (uc *ManageSecretUseCase) GetSecretsByScope(ctx context.Context, scope, scopeID string) ([]*project.Secret, error) {
	return uc.repo.ListByScope(ctx, scope, scopeID)
}

func (uc *ManageSettingsUseCase) GetCategories(ctx context.Context) ([]string, error) {
	return uc.repo.GetCategories(ctx)
}

func (uc *ManageSettingsUseCase) HandleWebhook(_ context.Context, projectID string, headers map[string]string, body []byte) (any, error) {
	return nil, fmt.Errorf("SCM webhook handling not yet migrated to use case layer")
}

func (uc *ManageSettingsUseCase) CreateStorageConfig(_ context.Context, data map[string]any) (any, error) {
	return nil, fmt.Errorf("storage config management not yet migrated to use case layer")
}

func (uc *ManageSettingsUseCase) ListStorageConfigs(_ context.Context) (any, error) {
	return nil, fmt.Errorf("storage config management not yet migrated to use case layer")
}

func (uc *ManageSettingsUseCase) GetStorageConfig(ctx context.Context, storageID string) (any, error) {
	return nil, fmt.Errorf("storage config management not yet migrated to use case layer")
}

func (uc *ManageSettingsUseCase) UpdateStorageConfig(ctx context.Context, data map[string]any) (any, error) {
	return nil, fmt.Errorf("storage config management not yet migrated to use case layer")
}

func (uc *ManageSettingsUseCase) DeleteStorageConfig(ctx context.Context, storageID string) error {
	return fmt.Errorf("storage config management not yet migrated to use case layer")
}

func (uc *ManageSettingsUseCase) SetDefaultStorageConfig(ctx context.Context, storageID string) error {
	return fmt.Errorf("storage config management not yet migrated to use case layer")
}

func (uc *ManageSettingsUseCase) GetDefaultStorageConfig(ctx context.Context) (any, error) {
	return nil, fmt.Errorf("storage config management not yet migrated to use case layer")
}

// suppress unused import
var _ = uuid.New
