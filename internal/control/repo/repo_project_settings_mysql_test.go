// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func projectSettingsMySQLAuthorize(ctx context.Context, view *Repositories, p *model.Project) (bool, error) {
	access, err := organizationMySQLAuthorize(ctx, view, "admin")
	if err != nil {
		return false, err
	}
	capabilities, err := access("project", p.ProjectID, p.OrgID)
	if err != nil {
		return false, err
	}
	if !capabilities.Read {
		return false, ErrProjectSettingsForbidden
	}
	return capabilities.Update, nil
}

func TestProjectSettingsMySQLCASAndRollback(t *testing.T) {
	ctx, db, other, organizations := organizationMySQLFixture(t)
	if db.Migrator().HasTable(&model.SCMPollingStream{}) {
		var existing int64
		require.NoError(t, db.Model(&model.SCMPollingStream{}).Count(&existing).Error)
		require.Zero(t, existing, "settings MySQL acceptance refuses existing polling rows")
	}
	require.NoError(t, db.AutoMigrate(&model.SCMPollingStream{}))

	org, err := organizations.Create(ctx, "admin", model.OrganizationInput{Name: "settings-mysql", DisplayName: "Settings"}, organizationMySQLAuthorize)
	require.NoError(t, err)
	p := model.Project{ProjectID: "settings-mysql", OrgID: org.OrgID, Name: "settings", DisplayName: "Initial", DefaultBranch: "main", Status: model.ProjectStatusActive, IsEnabled: 1, Visibility: 2, Credential: "synthetic-preserved", BuildConfig: datatypes.JSON("{}"), EnvVars: datatypes.JSON("{}"), Settings: datatypes.JSON("{}")}
	require.NoError(t, retryApprovalMySQLValidateJSON(db, &p))
	require.NoError(t, db.Create(&p).Error)
	// One owned inactive stream proves archive invalidation is atomic without
	// manufacturing provider readiness or deleting unrelated acceptance rows.
	scope := polling.Scope{Version: polling.Version, ProjectID: p.ProjectID, Provider: "gitlab", APIOrigin: "https://gitlab.example.com", Repository: "https://gitlab.example.com/fixtures/settings", ProviderProjectID: "42", Stream: "merge_requests"}
	scopeJSON, err := scope.Canonical()
	require.NoError(t, err)
	scopeDigest, err := scope.Digest()
	require.NoError(t, err)
	stream := model.SCMPollingStream{StreamID: "settings-mysql-stream", ProjectID: p.ProjectID, ScopeJSON: scopeJSON, ScopeDigest: scopeDigest, Revision: 7, ActivationGeneration: 3, State: polling.StateDisabled, PauseReason: "fixture"}
	require.NoError(t, retryApprovalMySQLValidateJSON(db, &stream))
	require.NoError(t, db.Create(&stream).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Where("stream_id = ? AND project_id = ?", stream.StreamID, p.ProjectID).Delete(&model.SCMPollingStream{}).Error)
	})
	one, two := &Repositories{database: statusTestDB{db}}, &Repositories{database: statusTestDB{other}}
	before, err := one.ProjectSettings(ctx, p.ProjectID, "admin", "read", ProjectSettingsPatch{}, projectSettingsMySQLAuthorize)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, repository := range []*Repositories{one, two} {
		repository := repository
		safe.Go(func() {
			<-start
			name := "Changed"
			visibility := 0
			empty := ""
			_, e := repository.ProjectSettings(ctx, p.ProjectID, "admin", "update", ProjectSettingsPatch{Version: before.Version, DisplayName: &name, Description: &empty, Visibility: &visibility}, projectSettingsMySQLAuthorize)
			results <- e
		})
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		e := awaitRetryMySQL(t, results)
		if e == nil {
			successes++
		} else if errors.Is(e, ErrProjectSettingsConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	after, err := one.ProjectSettings(ctx, p.ProjectID, "admin", "read", ProjectSettingsPatch{}, projectSettingsMySQLAuthorize)
	require.NoError(t, err)
	require.NotEqual(t, before.Version, after.Version)
	require.Zero(t, after.Visibility)
	require.Empty(t, after.Description)
	var persisted model.Project
	require.NoError(t, db.Where("id = ?", p.ID).Take(&persisted).Error)
	require.Equal(t, "synthetic-preserved", persisted.Credential)
	// Simulate a persistence failure at the audit boundary. The preceding project
	// update and polling invalidation must roll back together, not report success.
	hook := "settings_acceptance_audit_failure"
	auditFailure := errors.New("synthetic audit failure")
	var streamBeforeRollback model.SCMPollingStream
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			// Read through the same transaction to prove invalidation happened
			// before the forced failure, rather than merely observing no change.
			if err := tx.Session(&gorm.Session{NewDB: true}).Where("stream_id = ? AND project_id = ?", stream.StreamID, p.ProjectID).Take(&streamBeforeRollback).Error; err != nil {
				tx.AddError(err)
				return
			}
			tx.AddError(auditFailure)
		}
	}))
	hookRemoved := false
	defer func() {
		if !hookRemoved {
			require.NoError(t, db.Callback().Create().Remove(hook))
		}
	}()
	_, err = one.ProjectSettings(ctx, p.ProjectID, "admin", "archive", ProjectSettingsPatch{Version: after.Version}, projectSettingsMySQLAuthorize)
	require.ErrorIs(t, err, auditFailure)
	require.NoError(t, db.Callback().Create().Remove(hook))
	hookRemoved = true
	require.Equal(t, stream.Revision+1, streamBeforeRollback.Revision)
	require.Equal(t, stream.ActivationGeneration+1, streamBeforeRollback.ActivationGeneration)
	require.Equal(t, polling.StatePaused, streamBeforeRollback.State)
	unchanged, err := one.ProjectSettings(ctx, p.ProjectID, "admin", "read", ProjectSettingsPatch{}, projectSettingsMySQLAuthorize)
	require.NoError(t, err)
	require.Equal(t, after.Version, unchanged.Version)
	require.Equal(t, model.ProjectStatusActive, unchanged.Status)
	var unchangedStream model.SCMPollingStream
	require.NoError(t, db.Where("stream_id = ?", stream.StreamID).Take(&unchangedStream).Error)
	require.Equal(t, stream.Revision, unchangedStream.Revision)
	require.Equal(t, stream.ActivationGeneration, unchangedStream.ActivationGeneration)
	require.Equal(t, stream.State, unchangedStream.State)
	require.Equal(t, stream.PauseReason, unchangedStream.PauseReason)
	var failedAudits int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("resource_id = ? AND action = ?", p.ProjectID, "project:archive").Count(&failedAudits).Error)
	require.Zero(t, failedAudits)
	archived, err := one.ProjectSettings(ctx, p.ProjectID, "admin", "archive", ProjectSettingsPatch{Version: after.Version}, projectSettingsMySQLAuthorize)
	require.NoError(t, err)
	require.True(t, archived.CanRestore)
	require.NoError(t, db.Where("stream_id = ?", stream.StreamID).Take(&unchangedStream).Error)
	require.Equal(t, stream.Revision+1, unchangedStream.Revision)
	require.Equal(t, stream.ActivationGeneration+1, unchangedStream.ActivationGeneration)
	require.Equal(t, polling.StatePaused, unchangedStream.State)
	require.Equal(t, "configuration_changed", unchangedStream.PauseReason)
	_, err = two.ProjectSettings(ctx, p.ProjectID, "admin", "restore", ProjectSettingsPatch{Version: after.Version}, projectSettingsMySQLAuthorize)
	require.ErrorIs(t, err, ErrProjectSettingsConflict)
	restored, err := two.ProjectSettings(ctx, p.ProjectID, "admin", "restore", ProjectSettingsPatch{Version: archived.Version}, projectSettingsMySQLAuthorize)
	require.NoError(t, err)
	require.Equal(t, model.ProjectStatusActive, restored.Status)
	require.NotEqual(t, after.Version, restored.Version)
	require.NoError(t, db.Where("stream_id = ? AND project_id = ?", stream.StreamID, p.ProjectID).Take(&unchangedStream).Error)
	require.Equal(t, stream.Revision+2, unchangedStream.Revision)
	require.Equal(t, stream.ActivationGeneration+2, unchangedStream.ActivationGeneration)
	require.Equal(t, polling.StatePaused, unchangedStream.State)
	require.Equal(t, "configuration_changed", unchangedStream.PauseReason)
	require.Equal(t, stream.ScopeJSON, unchangedStream.ScopeJSON)
	require.Equal(t, stream.ScopeDigest, unchangedStream.ScopeDigest)
	require.Empty(t, unchangedStream.EnabledBy)
	require.Nil(t, unchangedStream.EnabledAt)
}
