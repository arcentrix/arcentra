// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/model"
)

func validateCreateProject(req *model.CreateProjectReq, createdBy string) error {
	if req == nil {
		return errors.New("project request is required")
	}
	req.OrgID = strings.TrimSpace(req.OrgID)
	req.Name = strings.TrimSpace(req.Name)
	req.RepoURL = strings.TrimSpace(req.RepoURL)
	req.RepoType = strings.ToLower(strings.TrimSpace(req.RepoType))
	if req.OrgID == "" {
		return errors.New("organization id cannot be empty")
	}
	if strings.TrimSpace(createdBy) == "" {
		return errors.New("project creator is required")
	}
	if n := utf8.RuneCountInString(req.Name); n < 2 || n > 128 {
		return errors.New("project name must contain 2 to 128 characters")
	}
	if strings.ContainsAny(req.Name, "/\\\x00\r\n") {
		return errors.New("project name contains invalid characters")
	}
	if err := validateProjectRepoURL(req.RepoURL); err != nil {
		return err
	}
	switch req.RepoType {
	case model.RepoTypeGit, model.RepoTypeGitHub, model.RepoTypeGitLab, model.RepoTypeGitee, model.RepoTypeBitbucket, model.RepoTypeGitea, model.RepoTypeSVN:
	default:
		return errors.New("unsupported repository type")
	}
	if err := validateProjectAuthType(req.AuthType); err != nil {
		return err
	}
	if err := validateProjectVisibility(req.Visibility); err != nil {
		return err
	}
	if req.AccessLevel != "" {
		if err := validateProjectAccessLevel(req.AccessLevel); err != nil {
			return err
		}
	}
	return validateProjectTriggerMode(req.TriggerMode)
}

func validateUpdateProject(req *model.UpdateProjectReq) error {
	if req == nil {
		return errors.New("project request is required")
	}
	if req.RepoURL != nil {
		if err := validateProjectRepoURL(*req.RepoURL); err != nil {
			return err
		}
	}
	if req.AuthType != nil {
		if err := validateProjectAuthType(*req.AuthType); err != nil {
			return err
		}
	}
	if req.Visibility != nil {
		if err := validateProjectVisibility(*req.Visibility); err != nil {
			return err
		}
	}
	if req.AccessLevel != nil {
		if err := validateProjectAccessLevel(*req.AccessLevel); err != nil {
			return err
		}
	}
	if req.TriggerMode != nil {
		if err := validateProjectTriggerMode(*req.TriggerMode); err != nil {
			return err
		}
	}
	if req.Status != nil && (*req.Status < model.ProjectStatusInactive || *req.Status > model.ProjectStatusDisabled) {
		return errors.New("invalid project status")
	}
	if req.IsEnabled != nil && *req.IsEnabled != 0 && *req.IsEnabled != 1 {
		return errors.New("isEnabled must be 0 or 1")
	}
	if req.DefaultBranch != nil && strings.TrimSpace(*req.DefaultBranch) == "" {
		return errors.New("default branch cannot be empty")
	}
	return nil
}

func validateProjectAuthType(value int) error {
	if value < model.AuthTypeNone || value > model.AuthTypeSSHKey {
		return errors.New("invalid project authentication type")
	}
	return nil
}

func validateProjectVisibility(value int) error {
	if value < model.VisibilityPrivate || value > model.VisibilityPublic {
		return errors.New("invalid project visibility")
	}
	return nil
}

func validateProjectAccessLevel(value string) error {
	switch value {
	case model.AccessLevelOwner, model.AccessLevelTeam, model.AccessLevelOrg:
		return nil
	default:
		return errors.New("invalid project access level")
	}
}

func validateProjectTriggerMode(value int) error {
	const all = model.TriggerModeManual | model.TriggerModeWebhook | model.TriggerModeSchedule | model.TriggerModePush | model.TriggerModeMR | model.TriggerModeTag
	if value < 0 || value & ^all != 0 {
		return errors.New("invalid project trigger mode")
	}
	return nil
}

func validateProjectRepoURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("repository URL is required")
	}
	if strings.ContainsAny(value, "\x00\r\n\t ") {
		return errors.New("invalid repository URL")
	}
	// 支持 git@host:owner/repo.git 形式的 SSH 地址
	if !strings.Contains(value, "://") {
		userHost, path, ok := strings.Cut(value, ":")
		user, host, hasUser := strings.Cut(userHost, "@")
		if ok && hasUser && user != "" && host != "" && path != "" {
			return nil
		}
		return errors.New("repository URL must use HTTP(S), SSH, Git or SVN")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("invalid repository URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ssh", "git", "svn", "svn+ssh":
	default:
		return fmt.Errorf("unsupported repository URL scheme: %s", parsed.Scheme)
	}
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if hasPassword || parsed.Scheme == "http" || parsed.Scheme == "https" {
			return errors.New("repository URL must not contain credentials; use the credential field")
		}
	}
	return nil
}
