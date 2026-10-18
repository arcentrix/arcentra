// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func secretMySQLAuthorize(ctx context.Context, r *Repositories, p *model.Project) (ProjectSecretCapabilities, error) {
	access, err := organizationMySQLAuthorize(ctx, r, "admin")
	if err != nil {
		return ProjectSecretCapabilities{}, err
	}
	caps, err := access("project", p.ProjectID, p.OrgID)
	return ProjectSecretCapabilities{Read: caps.Read, Write: caps.Update, Delete: caps.Update}, err
}

func TestProjectSecretsMySQLConcurrentCASRollbackAndRevocation(t *testing.T) {
	ctx, db, other, _ := organizationMySQLFixture(t)
	if db.Migrator().HasTable(&model.Secret{}) {
		var count int64
		require.NoError(t, db.Model(&model.Secret{}).Count(&count).Error)
		require.Zero(t, count, "secret MySQL acceptance refuses pre-existing rows")
	}
	require.NoError(t, db.AutoMigrate(&model.Secret{}))
	t.Cleanup(func() {
		require.NoError(t, db.Where("scope = ? AND scope_id = ?", "project", "secrets-mysql").Delete(&model.Secret{}).Error)
	})
	p := model.Project{ProjectID: "secrets-mysql", OrgID: "legacy-scope", Name: "secrets", Status: 1, IsEnabled: 1, BuildConfig: datatypes.JSON("{}"), EnvVars: datatypes.JSON("{}"), Settings: datatypes.JSON("{}")}
	require.NoError(t, db.Create(&p).Error)
	one, two := &Repositories{database: statusTestDB{db}}, &Repositories{database: statusTestDB{other}}
	encrypt := func() (string, error) { return "synthetic-ciphertext-not-an-operator-credential", nil }
	_, created, err := one.ProjectSecrets(ctx, p.ProjectID, "admin", "create", "", ProjectSecretMutation{Name: "TOKEN", SecretType: "token"}, true, encrypt, secretMySQLAuthorize)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, r := range []*Repositories{one, two} {
		r := r
		safe.Go(func() {
			<-start
			_, _, err := r.ProjectSecrets(ctx, p.ProjectID, "admin", "rotate", created.SecretID, ProjectSecretMutation{Version: created.Version}, true, encrypt, secretMySQLAuthorize)
			results <- err
		})
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := awaitRetryMySQL(t, results)
		if err == nil {
			success++
		} else if errors.Is(err, ErrProjectSecretConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
	list, _, err := one.ProjectSecrets(ctx, p.ProjectID, "admin", "list", "", ProjectSecretMutation{}, true, nil, secretMySQLAuthorize)
	require.NoError(t, err)
	require.Len(t, list.List, 1)
	current := list.List[0]
	_, _, err = one.ProjectSecrets(ctx, strings.ToUpper(p.ProjectID), "admin", "list", "", ProjectSecretMutation{}, true, nil, secretMySQLAuthorize)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NotEqual(t, current.SecretID, strings.ToLower(current.SecretID), "fixture must exercise a real case alias")
	_, _, err = one.ProjectSecrets(ctx, p.ProjectID, "admin", "rotate", strings.ToLower(current.SecretID), ProjectSecretMutation{Version: current.Version}, true, encrypt, secretMySQLAuthorize)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, _, err = one.ProjectSecrets(ctx, p.ProjectID, "admin", "create", "", ProjectSecretMutation{Name: "token", SecretType: "token"}, true, encrypt, secretMySQLAuthorize)
	require.ErrorIs(t, err, ErrProjectSecretConflict)
	hook := "secret_acceptance_audit_failure"
	failure := errors.New("synthetic audit failure")
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(failure)
		}
	}))
	_, _, err = one.ProjectSecrets(ctx, p.ProjectID, "admin", "disable", current.SecretID, ProjectSecretMutation{Version: current.Version}, true, nil, secretMySQLAuthorize)
	require.ErrorIs(t, err, failure)
	require.NoError(t, db.Callback().Create().Remove(hook))
	list, _, err = one.ProjectSecrets(ctx, p.ProjectID, "admin", "list", "", ProjectSecretMutation{}, true, nil, secretMySQLAuthorize)
	require.NoError(t, err)
	require.Equal(t, current.Version, list.List[0].Version)
	require.Equal(t, 1, list.List[0].IsEnabled)
	// Each request rebuilds authority on the primary; an already expired grant cannot rotate.
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "organization-platform").Update("expires_at", time.Now().Add(-time.Second)).Error)
	_, _, err = two.ProjectSecrets(ctx, p.ProjectID, "admin", "rotate", current.SecretID, ProjectSecretMutation{Version: current.Version}, true, encrypt, secretMySQLAuthorize)
	require.ErrorIs(t, err, ErrProjectSecretForbidden)
	var audits int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("resource_id = ?", current.SecretID).Count(&audits).Error)
	require.EqualValues(t, 2, audits)
}

func TestProjectSecretsMySQLCipherBudgetUsesCurrentLockedMetadata(t *testing.T) {
	ctx, db, other, _ := organizationMySQLFixture(t)
	if db.Migrator().HasTable(&model.Secret{}) {
		var count int64
		require.NoError(t, db.Model(&model.Secret{}).Count(&count).Error)
		require.Zero(t, count, "secret MySQL acceptance refuses pre-existing rows")
	}
	require.NoError(t, db.AutoMigrate(&model.Secret{}))
	t.Cleanup(func() {
		require.NoError(t, db.Where("scope = ? AND scope_id = ?", "project", "secrets-mysql").Delete(&model.Secret{}).Error)
	})
	p := model.Project{ProjectID: "secrets-mysql", OrgID: "legacy-scope", Name: "secrets", Status: 1, IsEnabled: 1, BuildConfig: datatypes.JSON("{}"), EnvVars: datatypes.JSON("{}"), Settings: datatypes.JSON("{}")}
	require.NoError(t, db.Create(&p).Error)
	secret := model.Secret{SecretID: "size-race", Name: "TOKEN", Scope: "project", ScopeID: p.ProjectID, SecretValue: "small-synthetic-ciphertext"}
	require.NoError(t, db.Create(&secret).Error)
	writer := db.WithContext(ctx).Begin()
	require.NoError(t, writer.Error)
	defer writer.Rollback()
	var locked model.Project
	require.NoError(t, writer.Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id").Where("project_id = ?", p.ProjectID).Take(&locked).Error)
	require.NoError(t, writer.Model(&model.Secret{}).Where("secret_id = ?", secret.SecretID).Update("secret_value", strings.Repeat("x", 256*1024)).Error)
	snapshotRead := make(chan struct{}, 1)
	hook := "secret_snapshot_consistent_read"
	require.NoError(t, other.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "project" && len(tx.Statement.Selects) == 2 && tx.Statement.Selects[0] == "project_id" {
			select {
			case snapshotRead <- struct{}{}:
			default:
			}
		}
	}))
	defer func() { require.NoError(t, other.Callback().Query().Remove(hook)) }()
	results := make(chan error, 1)
	consumed := false
	reader := NewSecretRepo(statusTestDB{other}).(*SecretRepo)
	safe.Go(func() {
		results <- reader.ReadProjectSecretSnapshot(ctx, p.ProjectID, true, func([]model.Secret) error { consumed = true; return nil })
	})
	select {
	case <-snapshotRead:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not establish its pre-writer consistent snapshot")
	}
	require.NoError(t, writer.Commit().Error)
	require.ErrorIs(t, awaitRetryMySQL(t, results), ErrProjectSecretUnavailable)
	require.False(t, consumed, "new oversized ciphertext must be refused before value materialization")
}
