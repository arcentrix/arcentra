// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

// ProjectSecretRequest 是项目凭据的写入请求，Value 不得写入日志或响应
// 新建及轮换以外的操作拒绝 Value，元数据编辑不会更改凭据
type ProjectSecretRequest struct {
	Name        string  `json:"name,omitempty"`
	SecretType  string  `json:"secretType,omitempty"`
	Description *string `json:"description,omitempty"`
	Version     string  `json:"version,omitempty"`
	Value       *string `json:"value,omitempty"`
}

var projectSecretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validProjectSecretValue(value string) bool {
	if len(value) == 0 || len(value) > 65536 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || strings.TrimSpace(value) == "" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	if strings.Trim(normalized, "*[]<>_ ") == "masked" || normalized == "[redacted]" || normalized == "<redacted>" || normalized == "redacted" || normalized == "configured" || normalized == "[configured]" || strings.Trim(normalized, "*•● ") == "" {
		return false
	}
	return true
}

func validateProjectSecretRequest(action string, in ProjectSecretRequest) error {
	if action == "list" {
		return nil
	}
	if action == "create" {
		if !projectSecretName.MatchString(in.Name) || in.Version != "" || in.Value == nil || !validProjectSecretValue(*in.Value) {
			return repo.ErrProjectSecretInvalid
		}
		switch in.SecretType {
		case "password", "token", "ssh_key", "env":
		default:
			return repo.ErrProjectSecretInvalid
		}
	} else {
		version, err := strconv.ParseUint(in.Version, 10, 64)
		if err != nil || version == 0 || strconv.FormatUint(version, 10) != in.Version || in.Name != "" || in.SecretType != "" {
			return repo.ErrProjectSecretInvalid
		}
		switch action {
		case "update":
			if in.Value != nil || in.Description == nil {
				return repo.ErrProjectSecretInvalid
			}
		case "rotate":
			if in.Description != nil || in.Value == nil || !validProjectSecretValue(*in.Value) {
				return repo.ErrProjectSecretInvalid
			}
		case "disable":
			if in.Description != nil || in.Value != nil {
				return repo.ErrProjectSecretInvalid
			}
		default:
			return repo.ErrProjectSecretInvalid
		}
	}
	if in.Description != nil && (!utf8.ValidString(*in.Description) || utf8.RuneCountInString(*in.Description) > 4096 || strings.ContainsRune(*in.Description, '\x00')) {
		return repo.ErrProjectSecretInvalid
	}
	return nil
}

// ProjectSecrets 只返回元数据，凭据仅在当前授权事务中加密
func (ss *SecretService) ProjectSecrets(ctx context.Context, repositories *repo.Repositories, projectID, actor, action, secretID string, in ProjectSecretRequest, authorize repo.ProjectSecretAuthorize) (*repo.ProjectSecretList, *repo.ProjectSecretView, error) {
	if ss == nil {
		return nil, nil, repo.ErrProjectSecretUnavailable
	}
	if err := validateProjectSecretRequest(action, in); err != nil {
		return nil, nil, err
	}
	encrypt := func() (string, error) {
		if in.Value == nil {
			return "", repo.ErrProjectSecretInvalid
		}
		value, err := ss.encrypt(*in.Value)
		if err != nil {
			return "", repo.ErrProjectSecretUnavailable
		}
		return value, nil
	}
	return repositories.ProjectSecrets(ctx, projectID, actor, action, secretID, repo.ProjectSecretMutation{Version: in.Version, Name: in.Name, SecretType: in.SecretType, Description: in.Description}, len(ss.encryptKey) == 32, encrypt, authorize)
}

// ProjectSecretResolution 是一次执行的内存快照，禁止序列化或写入运行元数据
// Values 仅供执行；Versions 仅供派发时的元数据复核，不进入 Copilot 上下文
type ProjectSecretResolution struct {
	Values   map[string]string `json:"-"`
	Versions map[string]string `json:"-"`
}

type projectSecretSnapshotReader interface {
	ReadProjectSecretSnapshot(context.Context, string, bool, func([]model.Secret) error) error
}

// ResolveProjectSecrets 一次完整读取并解密当前项目集合，任何失败都不返回部分结果
func (ss *SecretService) ResolveProjectSecrets(ctx context.Context, projectID string) (*ProjectSecretResolution, error) {
	if ss == nil || ss.secretRepo == nil {
		return nil, repo.ErrProjectSecretUnavailable
	}
	reader, ok := ss.secretRepo.(projectSecretSnapshotReader)
	if !ok {
		return nil, repo.ErrProjectSecretUnavailable
	}
	result := &ProjectSecretResolution{Values: map[string]string{}, Versions: map[string]string{}}
	err := reader.ReadProjectSecretSnapshot(ctx, projectID, true, func(rows []model.Secret) error {
		bytes := 0
		for _, row := range rows {
			if !projectSecretName.MatchString(row.Name) {
				return repo.ErrProjectSecretUnavailable
			}
			result.Versions[row.SecretID] = secretResolutionVersion(row)
			if row.IsEnabled != 1 {
				continue
			}
			if len(row.SecretValue) > 128*1024 {
				return repo.ErrProjectSecretUnavailable
			}
			value, err := ss.decrypt(row.SecretValue)
			if err != nil || !validProjectSecretValue(value) {
				return repo.ErrProjectSecretUnavailable
			}
			bytes += len(value)
			if bytes > 1024*1024 {
				return repo.ErrProjectSecretUnavailable
			}
			result.Values[row.Name] = value
		}
		return nil
	})
	if err != nil {
		return nil, repo.ErrProjectSecretUnavailable
	}
	return result, nil
}

func secretResolutionVersion(row model.Secret) string {
	return strconv.FormatUint(row.Version, 10) + ":" + strconv.Itoa(row.IsEnabled) + ":" + row.Name
}

// ValidateProjectSecretResolution 在派发前复核完整版本集合，变更时拒绝而非替换审批输入
func (ss *SecretService) ValidateProjectSecretResolution(ctx context.Context, projectID string, versions map[string]string) error {
	if ss == nil || ss.secretRepo == nil || versions == nil {
		return repo.ErrProjectSecretUnavailable
	}
	reader, ok := ss.secretRepo.(projectSecretSnapshotReader)
	if !ok {
		return repo.ErrProjectSecretUnavailable
	}
	err := reader.ReadProjectSecretSnapshot(ctx, projectID, false, func(rows []model.Secret) error {
		if len(rows) != len(versions) {
			return repo.ErrProjectSecretConflict
		}
		for _, row := range rows {
			if versions[row.SecretID] != secretResolutionVersion(row) {
				return repo.ErrProjectSecretConflict
			}
		}
		return nil
	})
	if err != nil {
		return repo.ErrProjectSecretConflict
	}
	return nil
}
