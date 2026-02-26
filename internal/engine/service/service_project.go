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

	"github.com/arcentrix/arcentra/internal/engine/model"
	projectrepo "github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

type ProjectService struct {
	projectRepo projectrepo.IProjectRepository
}

func NewProjectService(projectRepo projectrepo.IProjectRepository) *ProjectService {
	return &ProjectService{
		projectRepo: projectRepo,
	}
}

// CreateProject creates a project.
func (s *ProjectService) CreateProject(ctx context.Context, req *model.CreateProjectReq, createdBy string) (*model.Project, error) {
	if req.OrgId == "" {
		return nil, errors.New("organization id cannot be empty")
	}

	exists, err := s.projectRepo.NameExists(ctx, req.OrgId, req.Name)
	if err != nil {
		log.Errorw("check project name failed", "orgId", req.OrgId, "name", req.Name, "error", err)
		return nil, fmt.Errorf("check project name failed: %w", err)
	}
	if exists {
		return nil, errors.New("project name already exists")
	}

	// 3. 转换 JSON 字段
	buildConfigJSON, err := projectrepo.ConvertJSONToDatatypes(req.BuildConfig)
	if err != nil {
		log.Errorw("convert build config failed", "error", err)
		return nil, fmt.Errorf("convert build config failed: %w", err)
	}

	envVarsJSON, err := projectrepo.ConvertJSONToDatatypes(req.EnvVars)
	if err != nil {
		log.Errorw("convert env vars failed", "error", err)
		return nil, fmt.Errorf("convert env vars failed: %w", err)
	}

	settingsJSON, err := projectrepo.ConvertJSONToDatatypes(req.Settings)
	if err != nil {
		log.Errorw("convert settings failed", "error", err)
		return nil, fmt.Errorf("convert settings failed: %w", err)
	}

	// 4. 生成命名空间（org_name/project_name，这里简化处理）
	namespace := req.OrgId + "/" + req.Name

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
		ProjectId:     id.GetUUID(),
		OrgId:         req.OrgId,
		Name:          req.Name,
		DisplayName:   displayName,
		Namespace:     namespace,
		Description:   req.Description,
		RepoUrl:       req.RepoUrl,
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

	log.Infow("success create project", "name", project.Name, "projectId", project.ProjectId)

	return project, nil
}

// UpdateProject updates a project.
func (s *ProjectService) UpdateProject(ctx context.Context, projectId string, req *model.UpdateProjectReq) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectId)
	if err != nil {
		log.Errorw("check project exists failed", "projectId", projectId, "error", err)
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
	if req.RepoUrl != nil {
		updates["repo_url"] = *req.RepoUrl
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
		buildConfigJSON, convertErr := projectrepo.ConvertJSONToDatatypes(req.BuildConfig)
		if convertErr != nil {
			return nil, fmt.Errorf("convert build config failed: %w", convertErr)
		}
		updates["build_config"] = buildConfigJSON
	}
	if req.EnvVars != nil {
		envVarsJSON, convertErr := projectrepo.ConvertJSONToDatatypes(req.EnvVars)
		if convertErr != nil {
			return nil, fmt.Errorf("convert env vars failed: %w", convertErr)
		}
		updates["env_vars"] = envVarsJSON
	}
	if req.Settings != nil {
		settingsJSON, convertErr := projectrepo.ConvertJSONToDatatypes(req.Settings)
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
		if err = s.projectRepo.Update(ctx, projectId, updates); err != nil {
			log.Errorw("update project failed", "projectId", projectId, "error", err)
			return nil, fmt.Errorf("update project failed: %w", err)
		}
	}

	project, err := s.projectRepo.Get(ctx, projectId)
	if err != nil {
		log.Errorw("get project failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	log.Infow("success update project", "projectId", projectId)

	return project, nil
}

// DeleteProject deletes a project.
func (s *ProjectService) DeleteProject(ctx context.Context, projectId string) error {
	exists, err := s.projectRepo.Exists(ctx, projectId)
	if err != nil {
		log.Errorw("check project exists failed", "projectId", projectId, "error", err)
		return fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return errors.New("project not found")
	}

	if err := s.projectRepo.Delete(ctx, projectId); err != nil {
		log.Errorw("delete project failed", "projectId", projectId, "error", err)
		return fmt.Errorf("delete project failed: %w", err)
	}

	log.Infow("success delete project", "projectId", projectId)

	return nil
}

// GetProjectById returns project by projectId.
func (s *ProjectService) GetProjectById(ctx context.Context, projectId string) (*model.Project, error) {
	project, err := s.projectRepo.Get(ctx, projectId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("project not found")
		}
		log.Errorw("get project failed", "projectId", projectId, "error", err)
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

// GetProjectsByOrgId lists projects by orgId.
func (s *ProjectService) GetProjectsByOrgId(ctx context.Context, orgId string, pageNum, pageSize int, status *int) ([]*model.Project, int64, error) {
	projects, total, err := s.projectRepo.ListByOrg(ctx, orgId, pageNum, pageSize, status)
	if err != nil {
		log.Errorw("get projects by org id failed", "orgId", orgId, "error", err)
		return nil, 0, fmt.Errorf("get projects by org id failed: %w", err)
	}
	return projects, total, nil
}

// GetProjectsByUserId lists projects for user.
func (s *ProjectService) GetProjectsByUserId(ctx context.Context, userId string, pageNum, pageSize int, orgId, role string) ([]*model.Project, int64, error) {
	projects, total, err := s.projectRepo.ListByUser(ctx, userId, pageNum, pageSize, orgId, role)
	if err != nil {
		log.Errorw("get projects by user id failed", "userId", userId, "error", err)
		return nil, 0, fmt.Errorf("get projects by user id failed: %w", err)
	}
	return projects, total, nil
}

// UpdateProjectStatistics updates project statistics.
func (s *ProjectService) UpdateProjectStatistics(ctx context.Context, projectId string, stats *model.ProjectStatisticsReq) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectId)
	if err != nil {
		log.Errorw("check project exists failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return nil, errors.New("project not found")
	}

	if err = s.projectRepo.UpdateStatistics(ctx, projectId, stats); err != nil {
		log.Errorw("update project statistics failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("update project statistics failed: %w", err)
	}

	project, err := s.projectRepo.Get(ctx, projectId)
	if err != nil {
		log.Errorw("get project failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	return project, nil
}

// EnableProject enables a project.
func (s *ProjectService) EnableProject(ctx context.Context, projectId string) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectId)
	if err != nil {
		log.Errorw("check project exists failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return nil, errors.New("project not found")
	}

	if err = s.projectRepo.Enable(ctx, projectId); err != nil {
		log.Errorw("enable project failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("enable project failed: %w", err)
	}

	project, err := s.projectRepo.Get(ctx, projectId)
	if err != nil {
		log.Errorw("get project failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	return project, nil
}

// DisableProject disables a project.
func (s *ProjectService) DisableProject(ctx context.Context, projectId string) (*model.Project, error) {
	exists, err := s.projectRepo.Exists(ctx, projectId)
	if err != nil {
		log.Errorw("check project exists failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("check project exists failed: %w", err)
	}
	if !exists {
		return nil, errors.New("project not found")
	}

	if err = s.projectRepo.Disable(ctx, projectId); err != nil {
		log.Errorw("disable project failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("disable project failed: %w", err)
	}

	project, err := s.projectRepo.Get(ctx, projectId)
	if err != nil {
		log.Errorw("get project failed", "projectId", projectId, "error", err)
		return nil, fmt.Errorf("get project failed: %w", err)
	}

	return project, nil
}
