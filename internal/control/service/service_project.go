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

package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

type ProjectService struct {
	projectRepo repo.IProjectRepository
}

func NewProjectService(projectRepo repo.IProjectRepository) *ProjectService {
	return &ProjectService{
		projectRepo: projectRepo,
	}
}

// CreateProject creates a project.
func (s *ProjectService) CreateProject(ctx context.Context, req *model.CreateProjectReq, createdBy string) (*model.Project, error) {
	if req.OrgID == "" {
		return nil, errors.New("organization id cannot be empty")
	}

	exists, err := s.projectRepo.NameExists(ctx, req.OrgID, req.Name)
	if err != nil {
		log.Errorw("check project name failed", "orgID", req.OrgID, "name", req.Name, "error", err)
		return nil, fmt.Errorf("check project name failed: %w", err)
	}
	if exists {
		return nil, errors.New("project name already exists")
	}

	// 3. 转换 JSON 字段
	buildConfigJSON, err := repo.ConvertJSONToDatatypes(req.BuildConfig)
	if err != nil {
		log.Errorw("convert build config failed", "error", err)
		return nil, fmt.Errorf("convert build config failed: %w", err)
	}

	envVarsJSON, err := repo.ConvertJSONToDatatypes(req.EnvVars)
	if err != nil {
		log.Errorw("convert env vars failed", "error", err)
		return nil, fmt.Errorf("convert env vars failed: %w", err)
	}

	settingsJSON, err := repo.ConvertJSONToDatatypes(req.Settings)
	if err != nil {
		log.Errorw("convert settings failed", "error", err)
		return nil, fmt.Errorf("convert settings failed: %w", err)
	}

	// 4. 生成命名空间（org_name/project_name，这里简化处理）
	namespace := req.OrgID + "/" + req.Name

	// 5. 设置默认值
	defaultBranch := req.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	triggerMode := req.TriggerMode
	if triggerMode == 0 {
		triggerMode = model.TriggerModeManual
	}
	visibility := req.Visibility
	if visibility == 0 && req.Visibility != 0 {
		visibility = model.VisibilityPrivate
	}
	accessLevel := req.AccessLevel
	if accessLevel == "" {
		accessLevel = model.AccessLevelTeam
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Name
	}

	// 6. 创建项目实体
	project := &model.Project{
		ProjectID:     id.GetUUID(),
		OrgID:         req.OrgID,
		Name:          req.Name,
		DisplayName:   displayName,
		Namespace:     namespace,
		Description:   req.Description,
		RepoURL:       req.RepoURL,
		RepoType:      req.RepoType,
		DefaultBranch: defaultBranch,
		AuthType:      req.AuthType,
		Credential:    req.Credential,
		TriggerMode:   triggerMode,
		WebhookSecret: req.WebhookSecret,
		CronExpr:      req.CronExpr,
		BuildConfig:   buildConfigJSON,
		EnvVars:       envVarsJSON,
		Settings:      settingsJSON,
		Tags:          req.Tags,
		Language:      req.Language,
		Framework:     req.Framework,
		Status:        model.ProjectStatusActive,
		Visibility:    visibility,
		AccessLevel:   accessLevel,
		CreatedBy:     createdBy,
		IsEnabled:     1,
		Icon:          req.Icon,
		Homepage:      req.Homepage,
	}

	if err := s.projectRepo.Create(ctx, project); err != nil {
		log.Errorw("create project failed", "name", project.Name, "error", err)
		return nil, fmt.Errorf("create project failed: %w", err)
	}

	log.Infow("success create project", "name", project.Name, "projectID", project.ProjectID)

	return project, nil
}

// UpdateProject updates a project.
func (s *ProjectService) UpdateProject(ctx context.Context, projectID string, req *model.UpdateProjectReq) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectID)
	if err != nil {
		log.Errorw("check project exists failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return nil, errors.New("project not found")
	}

	// 2. 构建更新字段
	updates := make(map[string]interface{})

	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.RepoURL != nil {
		updates["repo_url"] = *req.RepoURL
	}
	if req.DefaultBranch != nil {
		updates["default_branch"] = *req.DefaultBranch
	}
	if req.AuthType != nil {
		updates["auth_type"] = *req.AuthType
	}
	if req.Credential != nil {
		updates["credential"] = *req.Credential
	}
	if req.TriggerMode != nil {
		updates["trigger_mode"] = *req.TriggerMode
	}
	if req.WebhookSecret != nil {
		updates["webhook_secret"] = *req.WebhookSecret
	}
	if req.CronExpr != nil {
		updates["cron_expr"] = *req.CronExpr
	}
	if req.BuildConfig != nil {
		buildConfigJSON, convertErr := repo.ConvertJSONToDatatypes(req.BuildConfig)
		if convertErr != nil {
			return nil, fmt.Errorf("convert build config failed: %w", convertErr)
		}
		updates["build_config"] = buildConfigJSON
	}
	if req.EnvVars != nil {
		envVarsJSON, convertErr := repo.ConvertJSONToDatatypes(req.EnvVars)
		if convertErr != nil {
			return nil, fmt.Errorf("convert env vars failed: %w", convertErr)
		}
		updates["env_vars"] = envVarsJSON
	}
	if req.Settings != nil {
		settingsJSON, convertErr := repo.ConvertJSONToDatatypes(req.Settings)
		if convertErr != nil {
			return nil, fmt.Errorf("convert settings failed: %w", convertErr)
		}
		updates["settings"] = settingsJSON
	}
	if req.Tags != nil {
		updates["tags"] = *req.Tags
	}
	if req.Language != nil {
		updates["language"] = *req.Language
	}
	if req.Framework != nil {
		updates["framework"] = *req.Framework
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.Visibility != nil {
		updates["visibility"] = *req.Visibility
	}
	if req.AccessLevel != nil {
		updates["access_level"] = *req.AccessLevel
	}
	if req.Icon != nil {
		updates["icon"] = *req.Icon
	}
	if req.Homepage != nil {
		updates["homepage"] = *req.Homepage
	}
	if req.IsEnabled != nil {
		updates["is_enabled"] = *req.IsEnabled
	}

	if len(updates) > 0 {
		if err = s.projectRepo.Update(ctx, projectID, updates); err != nil {
			log.Errorw("update project failed", "projectID", projectID, "error", err)
			return nil, fmt.Errorf("update project failed: %w", err)
		}
	}

	project, err := s.projectRepo.Get(ctx, projectID)
	if err != nil {
		log.Errorw("get project failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	log.Infow("success update project", "projectID", projectID)

	return project, nil
}

// DeleteProject deletes a project.
func (s *ProjectService) DeleteProject(ctx context.Context, projectID string) error {
	exists, err := s.projectRepo.Exists(ctx, projectID)
	if err != nil {
		log.Errorw("check project exists failed", "projectID", projectID, "error", err)
		return fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return errors.New("project not found")
	}

	if err := s.projectRepo.Delete(ctx, projectID); err != nil {
		log.Errorw("delete project failed", "projectID", projectID, "error", err)
		return fmt.Errorf("delete project failed: %w", err)
	}

	log.Infow("success delete project", "projectID", projectID)

	return nil
}

// GetProjectByID returns project by projectID.
func (s *ProjectService) GetProjectByID(ctx context.Context, projectID string) (*model.Project, error) {
	project, err := s.projectRepo.Get(ctx, projectID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("project not found")
		}
		log.Errorw("get project failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}
	return project, nil
}

// ListProjects lists projects with query.
func (s *ProjectService) ListProjects(ctx context.Context, query *model.ProjectQueryReq) ([]*model.Project, int64, error) {
	projects, total, err := s.projectRepo.List(ctx, query)
	if err != nil {
		log.Errorw("list projects failed", "error", err)
		return nil, 0, fmt.Errorf("list projects failed: %w", err)
	}
	return projects, total, nil
}

// GetProjectsByOrgID lists projects by orgID.
func (s *ProjectService) GetProjectsByOrgID(
	ctx context.Context,
	orgID string,
	pageNum, pageSize int,
	status *int,
) ([]*model.Project, int64, error) {
	projects, total, err := s.projectRepo.ListByOrg(ctx, orgID, pageNum, pageSize, status)
	if err != nil {
		log.Errorw("get projects by org id failed", "orgID", orgID, "error", err)
		return nil, 0, fmt.Errorf("get projects by org id failed: %w", err)
	}
	return projects, total, nil
}

// GetProjectsByUserID lists projects for user.
func (s *ProjectService) GetProjectsByUserID(
	ctx context.Context,
	userID string,
	pageNum, pageSize int,
	orgID, role string,
) ([]*model.Project, int64, error) {
	projects, total, err := s.projectRepo.ListByUser(ctx, userID, pageNum, pageSize, orgID, role)
	if err != nil {
		log.Errorw("get projects by user id failed", "userID", userID, "error", err)
		return nil, 0, fmt.Errorf("get projects by user id failed: %w", err)
	}
	return projects, total, nil
}

// UpdateProjectStatistics updates project statistics.
func (s *ProjectService) UpdateProjectStatistics(
	ctx context.Context,
	projectID string,
	stats *model.ProjectStatisticsReq,
) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectID)
	if err != nil {
		log.Errorw("check project exists failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return nil, errors.New("project not found")
	}

	if err = s.projectRepo.UpdateStatistics(ctx, projectID, stats); err != nil {
		log.Errorw("update project statistics failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("update project statistics failed: %w", err)
	}

	project, err := s.projectRepo.Get(ctx, projectID)
	if err != nil {
		log.Errorw("get project failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	return project, nil
}

// EnableProject enables a project.
func (s *ProjectService) EnableProject(ctx context.Context, projectID string) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectID)
	if err != nil {
		log.Errorw("check project exists failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return nil, errors.New("project not found")
	}

	if err = s.projectRepo.Enable(ctx, projectID); err != nil {
		log.Errorw("enable project failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("enable project failed: %w", err)
	}

	project, err := s.projectRepo.Get(ctx, projectID)
	if err != nil {
		log.Errorw("get project failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	return project, nil
}

// DisableProject disables a project.
func (s *ProjectService) DisableProject(ctx context.Context, projectID string) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectID)
	if err != nil {
		log.Errorw("check project exists failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return nil, errors.New("project not found")
	}

	if err = s.projectRepo.Disable(ctx, projectID); err != nil {
		log.Errorw("disable project failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("disable project failed: %w", err)
	}

	project, err := s.projectRepo.Get(ctx, projectID)
	if err != nil {
		log.Errorw("get project failed", "projectID", projectID, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	return project, nil
}
