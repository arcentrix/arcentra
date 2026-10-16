// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

func firstBuildRepoFixture(t *testing.T) (*gorm.DB, IFirstBuildRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "first-build.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.AutoMigrate(&model.Role{}, &model.RoleGrant{}, &model.Setting{}, &model.StorageConfig{}, &model.Agent{}))
	return db, NewFirstBuildRepo(statusTestDB{db})
}

func TestFirstBuildRepositoryFiltersBeforeLimitAndDeduplicates(t *testing.T) {
	db, repository := firstBuildRepoFixture(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&model.Role{RoleID: "enabled", Name: "Enabled", IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.Role{RoleID: "disabled", Name: "Disabled", IsEnabled: 1}).Error)
	require.NoError(t, db.Model(&model.Role{}).Where("role_id = ?", "disabled").Update("is_enabled", 0).Error)
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	for i := range FirstBuildScopeLimit + 1 {
		for _, kind := range []string{"foreign", "expired", "revoked", "disabled-role", "missing-role", "project", "platform", "foreign-team"} {
			grant := model.RoleGrant{GrantID: fmt.Sprintf("%s-%03d", kind, i), SubjectType: "user", SubjectID: "user", RoleID: "enabled", ScopeType: "org", ScopeID: fmt.Sprintf("a-%s-%03d", kind, i), IsEnabled: 1}
			switch kind {
			case "foreign":
				grant.SubjectID = "other"
			case "expired":
				grant.ExpiresAt = &past
			case "disabled-role":
				grant.RoleID = "disabled"
			case "missing-role":
				grant.RoleID = "missing"
			case "project", "platform":
				grant.ScopeType = kind
			case "foreign-team":
				grant.SubjectType, grant.SubjectID = "team", "foreign-team"
			}
			require.NoError(t, db.Create(&grant).Error)
			if kind == "revoked" {
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", grant.GrantID).Update("is_enabled", 0).Error)
			}
		}
	}
	for _, grant := range []model.RoleGrant{
		{GrantID: "own", SubjectType: "user", SubjectID: "user", RoleID: "enabled", ScopeType: "org", ScopeID: "z-own", IsEnabled: 1, ExpiresAt: &future},
		{GrantID: "team-own", SubjectType: "team", SubjectID: "team", RoleID: "enabled", ScopeType: "org", ScopeID: "z-own", IsEnabled: 1},
		{GrantID: "team-only", SubjectType: "team", SubjectID: "team", RoleID: "enabled", ScopeType: "org", ScopeID: "z-team", IsEnabled: 1},
		{GrantID: "empty", SubjectType: "user", SubjectID: "user", RoleID: "enabled", ScopeType: "org", IsEnabled: 1},
	} {
		require.NoError(t, db.Create(&grant).Error)
	}
	scopes, truncated, err := repository.ScopeCandidates(ctx, "user", []string{"team"})
	require.NoError(t, err)
	require.False(t, truncated)
	require.Equal(t, []string{"z-own", "z-team"}, scopes)
	scopes, truncated, err = repository.ScopeCandidates(ctx, "user", nil)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Equal(t, []string{"z-own"}, scopes)
	_, _, err = repository.ScopeCandidates(ctx, "", []string{"team"})
	require.Error(t, err)
}

func TestFirstBuildRepositorySigningRequiresBothFieldsAndSanitizesFailures(t *testing.T) {
	db, repository := firstBuildRepoFixture(t)
	ctx := context.Background()
	configured, err := repository.SigningConfigured(ctx)
	require.NoError(t, err)
	require.False(t, configured)
	for _, test := range []struct {
		value string
		want  bool
		fail  bool
	}{
		{`{}`, false, false}, {`null`, false, false},
		{`{"secret_key":"key-canary"}`, false, false}, {`{"salt":"salt-canary"}`, false, false},
		{`{"secret_key":"  ","salt":"salt-canary"}`, false, false},
		{`{"secret_key":"key-canary","salt":"salt-canary"}`, true, false},
		{`{"secret_key":null,"salt":"salt-canary"}`, false, false},
		{`{"secret_key":42,"salt":"salt-canary"}`, false, true},
		{`{malformed-secret-canary}`, false, true},
	} {
		require.NoError(t, db.Where("name = ?", consts.SettingNameAgentSecretKey).Delete(&model.Setting{}).Error)
		require.NoError(t, db.Create(&model.Setting{Name: consts.SettingNameAgentSecretKey, Value: datatypes.JSON(test.value)}).Error)
		configured, err = repository.SigningConfigured(ctx)
		if test.fail {
			require.Error(t, err)
			require.NotContains(t, err.Error(), "canary")
		} else {
			require.NoError(t, err)
		}
		require.Equal(t, test.want, configured, test.value)
	}
}

func TestFirstBuildRepositoryAgentRequiresEnabledAvailableAndRecent(t *testing.T) {
	db, repository := firstBuildRepoFixture(t)
	now := time.Now()
	cutoff, old, future := now.Add(-2*time.Minute), now.Add(-3*time.Minute), now.Add(time.Minute)
	for _, test := range []struct {
		name      string
		enabled   int
		status    int
		heartbeat *time.Time
		want      bool
	}{
		{"online", 1, 1, &now, true}, {"idle", 1, 4, &now, true}, {"boundary", 1, 1, &cutoff, true},
		{"disabled", 0, 1, &now, false}, {"busy", 1, 3, &now, false}, {"offline", 1, 2, &now, false},
		{"unknown", 1, 0, &now, false}, {"nil", 1, 1, nil, false}, {"old", 1, 1, &old, false}, {"future", 1, 1, &future, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, db.Where("agent_id <> ?", "").Delete(&model.Agent{}).Error)
			require.NoError(t, db.Create(&model.Agent{AgentID: test.name, IsEnabled: test.enabled, Status: test.status, LastHeartbeat: test.heartbeat}).Error)
			seen, err := repository.AgentEnabledRecentlySeen(context.Background(), cutoff, now)
			require.NoError(t, err)
			require.Equal(t, test.want, seen)
		})
	}
}

func TestFirstBuildRepositoryReadsPrimaryAndOnlyMinimalFields(t *testing.T) {
	db, repository := firstBuildRepoFixture(t)
	// An empty replica makes any accidental replica read fail rather than
	// returning stale configuration. Every probe must select primary storage.
	replica := sqlite.Open(filepath.Join(t.TempDir(), "empty-replica.db"))
	require.NoError(t, db.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{replica}})))
	var selected = map[string][]string{}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("first-build-columns", func(tx *gorm.DB) {
		selected[tx.Statement.Table] = append([]string{}, tx.Statement.Selects...)
	}))
	ctx := context.Background()
	_, _, err := repository.ScopeCandidates(ctx, "user", nil)
	require.NoError(t, err)
	_, err = repository.DefaultStorageConfigured(ctx)
	require.NoError(t, err)
	_, err = repository.SigningConfigured(ctx)
	require.NoError(t, err)
	_, err = repository.AgentEnabledRecentlySeen(ctx, time.Now().Add(-2*time.Minute), time.Now())
	require.NoError(t, err)
	require.Equal(t, []string{"HEX(scope_id)"}, selected["role_grant"])
	require.Equal(t, []string{"is_default"}, selected["storage_config"])
	require.Equal(t, []string{"value"}, selected["setting"])
	require.Equal(t, []string{"is_enabled"}, selected["agent"])
	require.NotContains(t, selected, "organization")
	require.NotContains(t, selected, "project")
}

type firstBuildNoCaseRole struct {
	model.Role
	RoleID string `gorm:"column:role_id;type:text COLLATE NOCASE;uniqueIndex"`
}

func (firstBuildNoCaseRole) TableName() string { return "role" }

type firstBuildNoCaseGrant struct {
	model.RoleGrant
	SubjectType string `gorm:"column:subject_type;type:text COLLATE NOCASE"`
	SubjectID   string `gorm:"column:subject_id;type:text COLLATE NOCASE"`
	RoleID      string `gorm:"column:role_id;type:text COLLATE NOCASE"`
	ScopeType   string `gorm:"column:scope_type;type:text COLLATE NOCASE"`
	ScopeID     string `gorm:"column:scope_id;type:text COLLATE NOCASE"`
}

func (firstBuildNoCaseGrant) TableName() string { return "role_grant" }

func TestFirstBuildRepositoryNoCaseCollationCannotAliasIdentityOrScope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "nocase.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.AutoMigrate(&firstBuildNoCaseRole{}, &firstBuildNoCaseGrant{}))
	require.NoError(t, db.Create(&model.Role{RoleID: "role", Name: "Role", IsEnabled: 1}).Error)
	for i := range FirstBuildScopeLimit + 1 {
		for _, kind := range []string{"user-alias", "team-alias", "role-alias", "subject-type-alias", "scope-type-alias"} {
			grant := model.RoleGrant{GrantID: fmt.Sprintf("%s-%03d", kind, i), SubjectType: "user", SubjectID: "User", RoleID: "role", ScopeType: "org", ScopeID: fmt.Sprintf("a-%s-%03d", kind, i), IsEnabled: 1}
			switch kind {
			case "user-alias":
				grant.SubjectID = "user"
			case "team-alias":
				grant.SubjectType, grant.SubjectID = "team", "team"
			case "role-alias":
				grant.RoleID = "ROLE"
			case "subject-type-alias":
				grant.SubjectType = "USER"
			case "scope-type-alias":
				grant.ScopeType = "ORG"
			}
			require.NoError(t, db.Create(&grant).Error)
		}
	}
	for _, grant := range []model.RoleGrant{
		{GrantID: "upper", SubjectType: "user", SubjectID: "User", RoleID: "role", ScopeType: "org", ScopeID: "Scope", IsEnabled: 1},
		{GrantID: "lower", SubjectType: "team", SubjectID: "Team", RoleID: "role", ScopeType: "org", ScopeID: "scope", IsEnabled: 1},
		{GrantID: "unicode", SubjectType: "user", SubjectID: "User", RoleID: "role", ScopeType: "org", ScopeID: "项目", IsEnabled: 1},
	} {
		require.NoError(t, db.Create(&grant).Error)
	}
	var loose int64
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("subject_id = ?", "User").Count(&loose).Error)
	require.Greater(t, loose, int64(FirstBuildScopeLimit), "fixture must exercise case-insensitive matching")
	repository := NewFirstBuildRepo(statusTestDB{db})
	scopes, truncated, err := repository.ScopeCandidates(context.Background(), "User", []string{"Team"})
	require.NoError(t, err)
	require.False(t, truncated, "case-colliding foreign candidates must be excluded before the cap")
	require.Equal(t, []string{"Scope", "scope", "项目"}, scopes)
}

func TestFirstBuildPrimaryRepositoriesFailClosedWithoutDatabase(t *testing.T) {
	for _, repositories := range []*Repositories{nil, {}, {database: statusTestDB{}}} {
		view, err := repositories.FirstBuildPrimaryRepositories(false)
		require.Error(t, err)
		require.Nil(t, view)
	}
}
