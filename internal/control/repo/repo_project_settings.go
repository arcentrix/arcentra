// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/id"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrProjectSettingsInvalid   = errors.New("invalid project settings request")
	ErrProjectSettingsConflict  = errors.New("project settings changed or lifecycle transition is unavailable")
	ErrProjectSettingsForbidden = errors.New("current project access denied")
)

// ProjectSettingsView intentionally excludes credentials, arbitrary JSON settings,
// environment variables and infrastructure identifiers. Version binds editable
// metadata and lifecycle only; authorization is rebuilt independently each time.
type ProjectSettingsView struct {
	ProjectID     string `json:"projectId"`
	OrgID         string `json:"orgId"`
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	Description   string `json:"description"`
	DefaultBranch string `json:"defaultBranch"`
	Tags          string `json:"tags"`
	Language      string `json:"language"`
	Framework     string `json:"framework"`
	Visibility    int    `json:"visibility"`
	Status        int    `json:"status"`
	IsEnabled     int    `json:"isEnabled"`
	Version       string `json:"version"`
	CanUpdate     bool   `json:"canUpdate"`
	CanArchive    bool   `json:"canArchive"`
	CanRestore    bool   `json:"canRestore"`
}

type ProjectSettingsPatch struct {
	Version       string  `json:"version"`
	DisplayName   *string `json:"displayName,omitempty"`
	Description   *string `json:"description,omitempty"`
	DefaultBranch *string `json:"defaultBranch,omitempty"`
	Tags          *string `json:"tags,omitempty"`
	Language      *string `json:"language,omitempty"`
	Framework     *string `json:"framework,omitempty"`
	Visibility    *int    `json:"visibility,omitempty"`
}

// ProjectSettingsAuthorize must reconstruct the authenticated actor and check
// project:read/update against these primary transaction-bound repositories.
// It returns whether a reader may mutate; request body attribution is never used.
type ProjectSettingsAuthorize func(context.Context, *Repositories, *model.Project) (bool, error)

func projectSettingsView(p *model.Project, writable bool) ProjectSettingsView {
	v := ProjectSettingsView{ProjectID: p.ProjectID, OrgID: p.OrgID, Name: p.Name, DisplayName: p.DisplayName, Description: p.Description, DefaultBranch: p.DefaultBranch, Tags: p.Tags, Language: p.Language, Framework: p.Framework, Visibility: p.Visibility, Status: p.Status, IsEnabled: p.IsEnabled}
	raw, _ := json.Marshal(struct {
		View     ProjectSettingsView
		Revision uint64
	}{v, p.SettingsVersion})
	sum := sha256.Sum256(raw)
	v.Version = hex.EncodeToString(sum[:])
	v.CanUpdate = writable && p.IsEnabled == 1 && p.Status == model.ProjectStatusActive
	v.CanArchive = v.CanUpdate
	v.CanRestore = writable && p.IsEnabled == 1 && p.Status == model.ProjectStatusArchived
	return v
}

func validProjectSettingsText(s string, max int, multiline bool) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) && !(multiline && (c == '\n' || c == '\t')) {
			return false
		}
	}
	return true
}
func validProjectSettingsBranch(s string) bool {
	if !validProjectSettingsText(s, 255, false) || s == "" || strings.TrimSpace(s) != s || s == "@" || s == "HEAD" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") || strings.ContainsAny(s, " \\~^:?*[") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
func (p ProjectSettingsPatch) updates(action string) (map[string]any, error) {
	if len(p.Version) != 64 {
		return nil, ErrProjectSettingsInvalid
	}
	if _, err := hex.DecodeString(p.Version); err != nil {
		return nil, ErrProjectSettingsInvalid
	}
	changes := map[string]any{}
	for _, f := range []struct {
		name      string
		value     *string
		max       int
		multiline bool
	}{{"display_name", p.DisplayName, 128, false}, {"description", p.Description, 4096, true}, {"tags", p.Tags, 1024, false}, {"language", p.Language, 128, false}, {"framework", p.Framework, 128, false}} {
		if f.value != nil {
			if !validProjectSettingsText(*f.value, f.max, f.multiline) {
				return nil, ErrProjectSettingsInvalid
			}
			changes[f.name] = *f.value
		}
	}
	if p.DisplayName != nil && strings.TrimSpace(*p.DisplayName) == "" {
		return nil, ErrProjectSettingsInvalid
	}
	if p.DefaultBranch != nil {
		if !validProjectSettingsBranch(*p.DefaultBranch) {
			return nil, ErrProjectSettingsInvalid
		}
		changes["default_branch"] = *p.DefaultBranch
	}
	if p.Visibility != nil {
		if *p.Visibility < 0 || *p.Visibility > 2 {
			return nil, ErrProjectSettingsInvalid
		}
		changes["visibility"] = *p.Visibility
	}
	switch action {
	case "update":
		if len(changes) == 0 {
			return nil, ErrProjectSettingsInvalid
		}
	case "archive", "restore":
		if len(changes) != 0 {
			return nil, ErrProjectSettingsInvalid
		}
	default:
		return nil, ErrProjectSettingsInvalid
	}
	return changes, nil
}

// ProjectSettings performs a bounded read or explicit CAS edit/archive/restore.
// Lock order: existing organization -> project -> actor/team -> permission catalog/grants -> polling stream.
// Never acquire pipeline locks while holding a project row. Lifecycle changes do
// not cancel or roll back already executing work or re-enable polling activation.
func (r *Repositories) ProjectSettings(ctx context.Context, projectID, actor, action string, patch ProjectSettingsPatch, authorize ProjectSettingsAuthorize) (*ProjectSettingsView, error) {
	if r == nil || r.database == nil || authorize == nil {
		return nil, errors.New("project settings unavailable")
	}
	if projectID == "" || len(projectID) > 64 || strings.TrimSpace(projectID) != projectID || actor == "" {
		return nil, ErrProjectSettingsInvalid
	}
	var changes map[string]any
	var err error
	if action != "read" {
		changes, err = patch.updates(action)
		if err != nil {
			return nil, err
		}
	}
	var result ProjectSettingsView
	err = r.database.Database().WithContext(ctx).Clauses(database.Write()).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		// Inspect only immutable scope to acquire organization before project. The
		// locked project is compared again; a stale scope cannot authorize writes.
		var scopes []struct {
			OrgID     string
			ProjectID string
		}
		if err := tx.Model(&model.Project{}).Select("org_id", "project_id").Where("project_id = ?", projectID).Limit(2).Find(&scopes).Error; err != nil {
			return err
		}
		if len(scopes) != 1 || scopes[0].ProjectID != projectID {
			return gorm.ErrRecordNotFound
		}
		scope := scopes[0]
		var organizations []model.Organization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ?", scope.OrgID).Limit(2).Find(&organizations).Error; err != nil {
			return err
		}
		if len(organizations) > 1 || len(organizations) == 1 && organizations[0].OrgID != scope.OrgID {
			return ErrProjectSettingsConflict
		}
		orgActive := len(organizations) == 0 || organizations[0].Status == model.OrgStatusActive && organizations[0].IsEnabled == 1
		var projects []model.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ?", projectID).Limit(2).Find(&projects).Error; err != nil {
			return err
		}
		if len(projects) != 1 || projects[0].ProjectID != projectID || projects[0].OrgID != scope.OrgID {
			return gorm.ErrRecordNotFound
		}
		p := projects[0]
		view, current := NewScopedAuthorizationView(tx)
		writable, err := authorize(ctx, view, &p)
		if err != nil {
			return err
		}
		result = projectSettingsView(&p, writable && orgActive)
		if action == "read" {
			return current(ctx)
		}
		if !writable {
			return ErrProjectSettingsForbidden
		}
		if result.Version != patch.Version {
			return ErrProjectSettingsConflict
		}
		switch action {
		case "update":
			if !result.CanUpdate {
				return ErrProjectSettingsConflict
			}
		case "archive":
			if !result.CanArchive {
				return ErrProjectSettingsConflict
			}
			changes["status"] = model.ProjectStatusArchived
		case "restore":
			if !result.CanRestore {
				return ErrProjectSettingsConflict
			}
			changes["status"] = model.ProjectStatusActive
		}
		if p.SettingsVersion == math.MaxUint64 {
			return ErrProjectSettingsConflict
		}
		changes["settings_version"] = p.SettingsVersion + 1
		if err := updateProjectWithPollingInvalidation(ctx, tx, projectID, changes); err != nil {
			return err
		}
		if err := tx.Where("id = ?", p.ID).Take(&p).Error; err != nil {
			return err
		}
		// No user content or before/after secrets are copied into audit payloads.
		audit := model.AuditLog{LogID: id.GetUUID(), ActorUserID: actor, Action: "project:" + action, ResourceType: "project", ResourceID: projectID, ScopeType: "project", ScopeID: projectID, Result: model.AuditResultSuccess, Payload: []byte(`{}`)}
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		result = projectSettingsView(&p, writable && orgActive)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
