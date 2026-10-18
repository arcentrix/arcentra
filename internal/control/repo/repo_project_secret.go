// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/id"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrProjectSecretInvalid     = errors.New("invalid project secret request")
	ErrProjectSecretForbidden   = errors.New("current project secret access denied")
	ErrProjectSecretConflict    = errors.New("project secret changed or operation unavailable")
	ErrProjectSecretUnavailable = errors.New("project secrets unavailable")
)

// ProjectSecretLimit 限制完整项目集合，超限时拒绝返回部分数据
const ProjectSecretLimit = 1000

// ProjectSecretView 是不含凭据及密文的管理响应
// 名称、作用域及业务标识创建后不可更改
type ProjectSecretView struct {
	SecretID    string    `json:"secretId"`
	ProjectID   string    `json:"projectId"`
	Name        string    `json:"name"`
	SecretType  string    `json:"secretType"`
	Description string    `json:"description"`
	Version     string    `json:"version"`
	IsEnabled   int       `json:"isEnabled"`
	Configured  bool      `json:"configured"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	CanUpdate   bool      `json:"canUpdate"`
	CanRotate   bool      `json:"canRotate"`
	CanDisable  bool      `json:"canDisable"`
}

// ProjectSecretCapabilities 保存当前事务中的权限，不使用请求中的归属声明
type ProjectSecretCapabilities struct{ Read, Write, Delete bool }

// ProjectSecretList 包含完整有界元数据及当前能力
type ProjectSecretList struct {
	ProjectID            string              `json:"projectId"`
	List                 []ProjectSecretView `json:"list"`
	Total                int                 `json:"total"`
	CanCreate            bool                `json:"canCreate"`
	CanWrite             bool                `json:"canWrite"`
	CanDisable           bool                `json:"canDisable"`
	ProjectActive        bool                `json:"projectActive"`
	EncryptionConfigured bool                `json:"encryptionConfigured"`
}

// ProjectSecretMutation 仅在服务端使用，密文通过加密回调生成，禁止序列化
type ProjectSecretMutation struct {
	Version, Name, SecretType string
	Description               *string
}

// ProjectSecretAuthorize 从锁定的主库视图重建当前用户、团队、目录和授权
type ProjectSecretAuthorize func(context.Context, *Repositories, *model.Project) (ProjectSecretCapabilities, error)

var projectSecretMetadataColumns = []string{"id", "secret_id", "name", "secret_type", "description", "scope", "scope_id", "created_by", "version", "is_enabled", "created_at", "updated_at", "CASE WHEN secret_value IS NOT NULL AND secret_value <> '' THEN 1 ELSE 0 END AS configured", "CASE WHEN is_enabled = 1 THEN COALESCE(LENGTH(secret_value), 0) ELSE 0 END AS cipher_bytes"}

func secretMetadata(s model.Secret, configured bool, c ProjectSecretCapabilities, active, key bool) ProjectSecretView {
	enabled := s.IsEnabled == 1 && active
	return ProjectSecretView{SecretID: s.SecretID, ProjectID: s.ScopeID, Name: s.Name, SecretType: s.SecretType, Description: s.Description, Version: strconv.FormatUint(s.Version, 10), IsEnabled: s.IsEnabled, Configured: configured, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, CanUpdate: c.Write && enabled, CanRotate: c.Write && enabled && key, CanDisable: c.Delete && enabled}
}

// lockSecretProject 严格按组织再项目顺序锁定，旧无组织行的作用域仍可使用
func lockSecretProject(tx *gorm.DB, projectID string) (*model.Project, bool, error) {
	var scopes []model.Project
	if err := tx.Select("project_id", "org_id").Where("project_id = ?", projectID).Limit(2).Find(&scopes).Error; err != nil {
		return nil, false, err
	}
	if len(scopes) != 1 || scopes[0].ProjectID != projectID {
		return nil, false, gorm.ErrRecordNotFound
	}
	var organizations []model.Organization
	if err := tx.Select("id", "org_id", "status", "is_enabled").Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ?", scopes[0].OrgID).Limit(2).Find(&organizations).Error; err != nil {
		return nil, false, err
	}
	if len(organizations) > 1 || len(organizations) == 1 && organizations[0].OrgID != scopes[0].OrgID {
		return nil, false, ErrProjectSecretConflict
	}
	active := len(organizations) == 0 || organizations[0].Status == model.OrgStatusActive && organizations[0].IsEnabled == 1
	var projects []model.Project
	if err := tx.Select("id", "project_id", "org_id", "status", "is_enabled").Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ?", projectID).Limit(2).Find(&projects).Error; err != nil {
		return nil, false, err
	}
	if len(projects) != 1 || projects[0].ProjectID != projectID || projects[0].OrgID != scopes[0].OrgID {
		return nil, false, gorm.ErrRecordNotFound
	}
	p := projects[0]
	return &p, active && p.IsEnabled == 1 && p.Status == model.ProjectStatusActive, nil
}

type projectSecretMetadata struct {
	model.Secret
	Configured  bool
	CipherBytes int64
}

func loadProjectSecretMetadata(tx *gorm.DB, projectID string) ([]projectSecretMetadata, error) {
	var rows []projectSecretMetadata
	if err := tx.Model(&model.Secret{}).Select(projectSecretMetadataColumns).Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope = ? AND scope_id = ?", "project", projectID).Order("secret_id ASC").Limit(ProjectSecretLimit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > ProjectSecretLimit {
		return nil, ErrProjectSecretUnavailable
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if row.Scope != "project" || row.ScopeID != projectID || row.SecretID == "" || row.Version == 0 || ids[strings.ToLower(row.SecretID)] || names[strings.ToLower(row.Name)] || row.IsEnabled != 0 && row.IsEnabled != 1 {
			return nil, ErrProjectSecretUnavailable
		}
		ids[strings.ToLower(row.SecretID)] = true
		names[strings.ToLower(row.Name)] = true
	}
	return rows, nil
}

// ProjectSecrets 在同一主库事务中完成当前授权、严格 CAS、修改和成功审计
// 锁序为组织→项目→用户/团队/权限目录/授权→凭据；加密后才能写入，禁止记录输入
func (r *Repositories) ProjectSecrets(ctx context.Context, projectID, actor, action, secretID string, in ProjectSecretMutation, key bool, encrypt func() (string, error), authorize ProjectSecretAuthorize) (*ProjectSecretList, *ProjectSecretView, error) {
	if r == nil || r.database == nil || authorize == nil {
		return nil, nil, ErrProjectSecretUnavailable
	}
	if projectID == "" || len(projectID) > 64 || strings.TrimSpace(projectID) != projectID || actor == "" {
		return nil, nil, ErrProjectSecretInvalid
	}
	var list ProjectSecretList
	var detail *ProjectSecretView
	err := r.database.Database().WithContext(ctx).Clauses(database.Write()).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		p, active, err := lockSecretProject(tx, projectID)
		if err != nil {
			return err
		}
		view, current := NewScopedAuthorizationView(tx)
		caps, err := authorize(ctx, view, p)
		if err != nil {
			return err
		}
		if !caps.Read {
			return ErrProjectSecretForbidden
		}
		if action != "list" && ((action == "disable" && !caps.Delete) || (action != "disable" && !caps.Write)) {
			return ErrProjectSecretForbidden
		}
		rows, err := loadProjectSecretMetadata(tx, projectID)
		if err != nil {
			return err
		}
		list = ProjectSecretList{ProjectID: projectID, List: make([]ProjectSecretView, 0, len(rows)), Total: len(rows), CanCreate: caps.Write && active && key && len(rows) < ProjectSecretLimit, CanWrite: caps.Write, CanDisable: caps.Delete, ProjectActive: active, EncryptionConfigured: key}
		if action == "list" {
			for _, row := range rows {
				list.List = append(list.List, secretMetadata(row.Secret, row.Configured, caps, active, key))
			}
			return current(ctx)
		}
		if !active {
			return ErrProjectSecretConflict
		}
		var row model.Secret
		configured := false
		if action == "create" {
			if len(rows) >= ProjectSecretLimit {
				return ErrProjectSecretConflict
			}
			for _, existing := range rows {
				if strings.EqualFold(existing.Name, in.Name) {
					return ErrProjectSecretConflict
				}
			}
			// This equality query also catches accent/collation aliases on MySQL.
			var count int64
			if err := tx.Model(&model.Secret{}).Where("scope = ? AND scope_id = ? AND name = ?", "project", projectID, in.Name).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return ErrProjectSecretConflict
			}
			if encrypt == nil {
				return ErrProjectSecretUnavailable
			}
			ciphertext, err := encrypt()
			if err != nil {
				return err
			}
			row = model.Secret{SecretID: id.GetUild(), Scope: "project", ScopeID: projectID, Name: in.Name, SecretType: in.SecretType, SecretValue: ciphertext, CreatedBy: actor, Version: 1, IsEnabled: 1}
			if in.Description != nil {
				row.Description = *in.Description
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			configured = true
		} else {
			found := false
			for _, candidate := range rows {
				if candidate.SecretID == secretID {
					row = candidate.Secret
					configured = candidate.Configured
					found = true
					break
				}
			}
			if !found {
				return gorm.ErrRecordNotFound
			}
			if row.Version == math.MaxUint64 || strconv.FormatUint(row.Version, 10) != in.Version || row.IsEnabled != 1 {
				return ErrProjectSecretConflict
			}
			changes := map[string]any{"version": row.Version + 1}
			switch action {
			case "update":
				if in.Description == nil {
					return ErrProjectSecretInvalid
				}
				changes["description"] = *in.Description
				row.Description = *in.Description
			case "rotate":
				if encrypt == nil {
					return ErrProjectSecretUnavailable
				}
				ciphertext, err := encrypt()
				if err != nil {
					return err
				}
				changes["secret_value"] = ciphertext
				configured = true
			case "disable":
				changes["is_enabled"] = 0
				row.IsEnabled = 0
			default:
				return ErrProjectSecretInvalid
			}
			updated := tx.Model(&model.Secret{}).Where("id = ? AND secret_id = ? AND scope = ? AND scope_id = ? AND version = ? AND is_enabled = ?", row.ID, row.SecretID, "project", projectID, row.Version, 1).Updates(changes)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrProjectSecretConflict
			}
			row.Version++
		}
		// Reload metadata only; never return ciphertext even on the create response.
		var saved projectSecretMetadata
		if err := tx.Model(&model.Secret{}).Select(projectSecretMetadataColumns).Where("id = ?", row.ID).Take(&saved).Error; err != nil {
			return err
		}
		audit := model.AuditLog{LogID: id.GetUUID(), ActorUserID: actor, Action: "secret:" + action, ResourceType: "secret", ResourceID: row.SecretID, ScopeType: "project", ScopeID: projectID, Result: model.AuditResultSuccess, Payload: []byte(`{}`)}
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		result := secretMetadata(saved.Secret, configured, caps, active, key)
		detail = &result
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &list, detail, nil
}

// ReadProjectSecretSnapshot 在主库锁定完整有界项目集合供执行解密或版本复核
// consume 必须只在内存中使用，事务失败后调用方不得暴露已生成结果
func (sr *SecretRepo) ReadProjectSecretSnapshot(ctx context.Context, projectID string, values bool, consume func([]model.Secret) error) error {
	if projectID == "" || len(projectID) > 64 || strings.TrimSpace(projectID) != projectID || consume == nil {
		return ErrProjectSecretUnavailable
	}
	return sr.Database().WithContext(ctx).Clauses(database.Write()).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		_, active, err := lockSecretProject(tx, projectID)
		if err != nil {
			return err
		}
		if !active {
			return ErrProjectSecretUnavailable
		}
		metadata, err := loadProjectSecretMetadata(tx, projectID)
		if err != nil {
			return err
		}
		rows := make([]model.Secret, 0, len(metadata))
		if values {
			// Reject oversized ciphertext before materializing it. Disabled rows
			// retain metadata but their obsolete values are never loaded/decrypted.
			// Lengths came from the same FOR UPDATE current read as metadata.
			// A separate aggregate would be stale under MySQL REPEATABLE READ.
			var totalBytes int64
			for _, row := range metadata {
				if row.CipherBytes > 128*1024 {
					return ErrProjectSecretUnavailable
				}
				totalBytes += row.CipherBytes
				if totalBytes > 2*1024*1024 {
					return ErrProjectSecretUnavailable
				}
			}
			columns := make([]string, 0, len(secretSelectFields)+2)
			for _, column := range secretSelectFields {
				if column == "secret_value" {
					columns = append(columns, "CASE WHEN is_enabled = 1 THEN secret_value ELSE '' END AS secret_value")
				} else {
					columns = append(columns, column)
				}
			}
			columns = append(columns, "version", "is_enabled")
			if err := tx.Model(&model.Secret{}).Select(columns).Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope = ? AND scope_id = ?", "project", projectID).Order("secret_id ASC").Limit(ProjectSecretLimit + 1).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != len(metadata) {
				return ErrProjectSecretUnavailable
			}
			for index, row := range rows {
				if row.SecretID != metadata[index].SecretID || row.Scope != "project" || row.ScopeID != projectID || row.Version != metadata[index].Version {
					return ErrProjectSecretUnavailable
				}
			}
		} else {
			for _, row := range metadata {
				rows = append(rows, row.Secret)
			}
		}
		return consume(rows)
	})
}
