// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type effectiveActionsDB struct {
	database.IDatabase
	db *gorm.DB
}

func (d effectiveActionsDB) Database() *gorm.DB { return d.db }

// Keep a legacy entry available even when invalidation would fail. Projection
// must derive the current answer from repositories rather than touch this cache.
type effectiveActionsCache struct {
	cache.ICache
	value         string
	reads, writes int
}

func (c *effectiveActionsCache) Get(context.Context, string) *redis.StringCmd {
	c.reads++
	return redis.NewStringResult(c.value, nil)
}

func (c *effectiveActionsCache) Set(context.Context, string, any, time.Duration) *redis.StatusCmd {
	c.writes++
	return redis.NewStatusResult("OK", nil)
}

func (c *effectiveActionsCache) Del(context.Context, ...string) *redis.IntCmd {
	return redis.NewIntResult(0, nil)
}

type effectiveActionsFixture struct {
	db      *gorm.DB
	repos   *repo.Repositories
	subject Subject
}

func newEffectiveActionsFixture(t *testing.T, permissions ...model.Permission) *effectiveActionsFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "effective-actions.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	require.NoError(t, db.AutoMigrate(&model.Permission{}, &model.RolePermissionBinding{}, &model.Role{},
		&model.RoleGrant{}, &model.AuditLog{}, &model.Project{}, &model.Pipeline{}))
	f := &effectiveActionsFixture{
		db: db, repos: repo.NewRepositories(effectiveActionsDB{db: db}, nil), subject: Subject{UserID: "user-1"},
	}
	require.NoError(t, db.Create(&model.Role{RoleID: "role-1", Name: "test role", IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.RoleGrant{
		GrantID: "grant-1", SubjectType: "user", SubjectID: f.subject.UserID,
		RoleID: "role-1", ScopeType: "platform", IsEnabled: 1,
	}).Error)
	for i := range permissions {
		require.NoError(t, db.Create(&permissions[i]).Error)
		require.NoError(t, db.Create(&model.RolePermissionBinding{
			RoleID: "role-1", PermissionID: permissions[i].PermissionID,
		}).Error)
	}
	return f
}

func (f *effectiveActionsFixture) authorizer(t *testing.T, ch cache.ICache) IAuthorizer {
	t.Helper()
	a, err := NewAuthorizer(f.repos, ch)
	require.NoError(t, err)
	return a
}

func TestEffectiveActionsProjectsCanonicalActionsAndAudits(t *testing.T) {
	f := newEffectiveActionsFixture(t,
		model.Permission{PermissionID: "opaque37", ResourceType: "pipeline", Action: "rerun"},
		model.Permission{PermissionID: "secret:write", ResourceType: "approval", Action: "read"},
		model.Permission{PermissionID: "opaque-secret-a", ResourceType: "secret", Action: "read"},
		model.Permission{PermissionID: "opaque-secret-b", ResourceType: "secret", Action: "read"},
		model.Permission{PermissionID: "project:read", ResourceType: "project", Action: "read"},
	)
	// An unbound dictionary entry must be checked and audited using its canonical
	// action too, without allowing an unrelated opaque ID to become a capability.
	require.NoError(t, f.db.Create(&model.Permission{
		PermissionID: "opaque-denied", ResourceType: "secret", Action: "delete",
	}).Error)
	actions, err := f.authorizer(t, nil).EffectiveActions(context.Background(), f.subject, "platform", "")
	require.NoError(t, err)
	require.Equal(t, []string{"approval:read", "pipeline:rerun", "project:read", "secret:read"}, actions)
	require.NotContains(t, actions, "secret:write")

	var entries []model.AuditLog
	require.NoError(t, f.db.Find(&entries).Error)
	require.NotEmpty(t, entries)
	var allowedSecret, deniedSecret bool
	for _, entry := range entries {
		require.NotContains(t, []string{"opaque37", "opaque-secret-a", "opaque-secret-b", "opaque-denied", "secret:write"}, entry.Action)
		if entry.Action == "secret:read" && entry.Result == model.AuditResultSuccess {
			allowedSecret = true
			require.Equal(t, "secret", entry.ResourceType)
		}
		if entry.Action == "secret:delete" && entry.Result == model.AuditResultDenied {
			deniedSecret = true
		}
	}
	require.True(t, allowedSecret, "canonical sensitive actions must retain their success audit")
	require.True(t, deniedSecret, "denial audit must refer to the real action")
}

func TestEffectiveActionsPreservesWildcardComponents(t *testing.T) {
	for _, tc := range []struct {
		name, resource, action string
		want                   []string
	}{
		{name: "all actions", resource: "*", action: "*", want: []string{"*:*", "pipeline:rerun", "secret:read"}},
		{name: "resource wildcard", resource: "*", action: "read", want: []string{"*:read", "secret:read"}},
		{name: "action wildcard", resource: "pipeline", action: "*", want: []string{"pipeline:*", "pipeline:rerun"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEffectiveActionsFixture(t, model.Permission{
				PermissionID: "opaque-wildcard", ResourceType: tc.resource, Action: tc.action,
			})
			require.NoError(t, f.db.Create(&[]model.Permission{
				{PermissionID: "opaque-pipeline", ResourceType: "pipeline", Action: "rerun"},
				{PermissionID: "opaque-secret", ResourceType: "secret", Action: "read"},
			}).Error)
			actions, err := f.authorizer(t, nil).EffectiveActions(context.Background(), f.subject, "platform", "")
			require.NoError(t, err)
			require.Equal(t, tc.want, actions)
		})
	}
}

func TestEffectiveActionsRejectsMalformedCanonicalComponents(t *testing.T) {
	for _, tc := range []struct {
		name, resource, action string
	}{
		{name: "empty resource", action: "read"},
		{name: "empty action", resource: "secret"},
		{name: "both empty"},
		{name: "resource whitespace", resource: " \t", action: "read"},
		{name: "action whitespace", resource: "secret", action: "\n"},
		{name: "resource leading space", resource: " secret", action: "read"},
		{name: "action trailing space", resource: "secret", action: "read "},
		{name: "resource internal space", resource: "sec ret", action: "read"},
		{name: "action internal tab", resource: "secret", action: "re\tad"},
		{name: "unicode space", resource: "secret", action: "read\u00a0"},
		{name: "resource colon", resource: "secret:write", action: "read"},
		{name: "action colon", resource: "secret", action: "read:write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Wildcard authorization would allow any checked string. Validation
			// must therefore happen before projecting the malformed dictionary row.
			f := newEffectiveActionsFixture(t,
				model.Permission{PermissionID: "opaque-admin", ResourceType: "*", Action: "*"},
				model.Permission{PermissionID: "opaque-malformed", ResourceType: tc.resource, Action: tc.action},
			)
			actions, err := f.authorizer(t, nil).EffectiveActions(context.Background(), f.subject, "platform", "")
			require.NoError(t, err)
			require.Equal(t, []string{"*:*"}, actions)
			var malformedAudits int64
			require.NoError(t, f.db.Model(&model.AuditLog{}).Where("action = ?", "opaque-malformed").Count(&malformedAudits).Error)
			require.Zero(t, malformedAudits)
		})
	}
}

func TestEffectiveActionsIgnoresMissingPermissionBindings(t *testing.T) {
	f := newEffectiveActionsFixture(t)
	require.NoError(t, f.db.Create(&model.RolePermissionBinding{RoleID: "role-1", PermissionID: "secret:write"}).Error)
	actions, err := f.authorizer(t, nil).EffectiveActions(context.Background(), f.subject, "platform", "")
	require.NoError(t, err)
	require.Empty(t, actions)
}

func TestEffectiveActionsIgnoresWarmCacheAfterLiveRevocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*gorm.DB) error
		want   []string
	}{
		{name: "disabled grant", change: func(db *gorm.DB) error {
			return db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Update("is_enabled", 0).Error
		}},
		{name: "deleted grant", change: func(db *gorm.DB) error {
			return db.Where("grant_id = ?", "grant-1").Delete(&model.RoleGrant{}).Error
		}},
		{name: "expired grant", change: func(db *gorm.DB) error {
			return db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Update("expires_at", time.Now().Add(-time.Hour)).Error
		}},
		{name: "disabled role", change: func(db *gorm.DB) error {
			return db.Model(&model.Role{}).Where("role_id = ?", "role-1").Update("is_enabled", 0).Error
		}},
		{name: "deleted role", change: func(db *gorm.DB) error {
			return db.Where("role_id = ?", "role-1").Delete(&model.Role{}).Error
		}},
		{name: "deleted binding", change: func(db *gorm.DB) error {
			return db.Where("role_id = ?", "role-1").Delete(&model.RolePermissionBinding{}).Error
		}},
		{name: "deleted permission", change: func(db *gorm.DB) error {
			return db.Where("permission_id = ?", "secret:read").Delete(&model.Permission{}).Error
		}},
		{name: "remapped permission", change: func(db *gorm.DB) error {
			return db.Model(&model.Permission{}).Where("permission_id = ?", "secret:read").Update("resource_type", "approval").Error
		}, want: []string{"approval:read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "secret:read", ResourceType: "secret", Action: "read"})
			ch := &effectiveActionsCache{value: `["secret:read"]`}
			a := f.authorizer(t, ch)
			before, err := a.EffectiveActions(context.Background(), f.subject, "platform", "")
			require.NoError(t, err)
			require.Equal(t, []string{"secret:read"}, before)
			require.NoError(t, tc.change(f.db))

			// Neither Reload nor cache invalidation runs between these requests.
			after, err := a.EffectiveActions(context.Background(), f.subject, "platform", "")
			require.NoError(t, err)
			require.Equal(t, tc.want, append([]string(nil), after...))
			require.Zero(t, ch.reads, "legacy action cache must never authorize a request")
			require.Zero(t, ch.writes, "live action projection must not create stale cache entries")
		})
	}
}

func TestEffectiveActionsUsesCurrentTeamMembership(t *testing.T) {
	f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "opaque37", ResourceType: "pipeline", Action: "rerun"})
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Updates(map[string]any{
		"subject_type": "team", "subject_id": "team-1",
	}).Error)
	ch := &effectiveActionsCache{value: `["pipeline:rerun"]`}
	a := f.authorizer(t, ch)
	member := Subject{UserID: f.subject.UserID, TeamIDs: []string{"team-1"}}
	actions, err := a.EffectiveActions(context.Background(), member, "platform", "")
	require.NoError(t, err)
	require.Equal(t, []string{"pipeline:rerun"}, actions)
	actions, err = a.EffectiveActions(context.Background(), f.subject, "platform", "")
	require.NoError(t, err)
	require.Empty(t, actions)
	require.Zero(t, ch.reads)
	require.Zero(t, ch.writes)
}

func TestEffectiveActionsUsesNewLiveGrantsAndResolvedScope(t *testing.T) {
	f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "opaque37", ResourceType: "pipeline", Action: "rerun"})
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Update("is_enabled", 0).Error)
	require.NoError(t, f.db.Create(&model.Project{ProjectID: "project-1", OrgID: "org-1", IsEnabled: 1}).Error)
	require.NoError(t, f.db.Create(&model.Pipeline{PipelineID: "pipeline-1", ProjectID: "project-1", IsEnabled: 1}).Error)
	ch := &effectiveActionsCache{value: `[]`}
	a := f.authorizer(t, ch)
	actions, err := a.EffectiveActions(context.Background(), f.subject, "pipeline", "pipeline-1")
	require.NoError(t, err)
	require.Empty(t, actions)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant-1").Updates(map[string]any{
		"is_enabled": 1, "scope_type": "org", "scope_id": "org-1",
	}).Error)
	actions, err = a.EffectiveActions(context.Background(), f.subject, "pipeline", "pipeline-1")
	require.NoError(t, err)
	require.Equal(t, []string{"pipeline:rerun"}, actions)
	actions, err = a.EffectiveActions(context.Background(), f.subject, "org", "org-other")
	require.NoError(t, err)
	require.Empty(t, actions)
	require.Zero(t, ch.reads)
	require.Zero(t, ch.writes)
}

type effectiveActionsPermissionFailure struct {
	repo.IPermissionRepository
	listError, bindingError error
	failListAt, calls       int
}

func (r *effectiveActionsPermissionFailure) ListPermissions(ctx context.Context) ([]model.Permission, error) {
	r.calls++
	if r.listError != nil && (r.failListAt == 0 || r.calls == r.failListAt) {
		return nil, r.listError
	}
	return r.IPermissionRepository.ListPermissions(ctx)
}

func (r *effectiveActionsPermissionFailure) ListAllRoleBindings(ctx context.Context) ([]model.RolePermissionBinding, error) {
	if r.bindingError != nil {
		return nil, r.bindingError
	}
	return r.IPermissionRepository.ListAllRoleBindings(ctx)
}

type effectiveActionsRoleFailure struct {
	repo.IRoleRepository
	err error
}

func (r effectiveActionsRoleFailure) BatchGet(context.Context, []string) ([]model.Role, error) {
	return nil, r.err
}

type effectiveActionsGrantFailure struct {
	repo.IRoleGrantRepository
	err error
}

func (r effectiveActionsGrantFailure) ListAllEnabled(context.Context) ([]model.RoleGrant, error) {
	return nil, r.err
}

func TestEffectiveActionsPropagatesLiveReadErrorsDespiteWarmCache(t *testing.T) {
	failure := errors.New("live repository unavailable")
	for _, tc := range []struct {
		name   string
		inject func(*repo.Repositories)
	}{
		{name: "permission snapshot", inject: func(r *repo.Repositories) {
			r.Permission = &effectiveActionsPermissionFailure{IPermissionRepository: r.Permission, listError: failure}
		}},
		{name: "permission projection", inject: func(r *repo.Repositories) {
			r.Permission = &effectiveActionsPermissionFailure{IPermissionRepository: r.Permission, listError: failure, failListAt: 2}
		}},
		{name: "bindings", inject: func(r *repo.Repositories) {
			r.Permission = &effectiveActionsPermissionFailure{IPermissionRepository: r.Permission, bindingError: failure}
		}},
		{name: "roles", inject: func(r *repo.Repositories) {
			r.Role = effectiveActionsRoleFailure{IRoleRepository: r.Role, err: failure}
		}},
		{name: "grants", inject: func(r *repo.Repositories) {
			r.RoleGrant = effectiveActionsGrantFailure{IRoleGrantRepository: r.RoleGrant, err: failure}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "secret:read", ResourceType: "secret", Action: "read"})
			ch := &effectiveActionsCache{value: `["*:*","secret:read"]`}
			a := f.authorizer(t, ch)
			tc.inject(f.repos)
			actions, err := a.EffectiveActions(context.Background(), f.subject, "platform", "")
			require.ErrorIs(t, err, failure)
			require.Nil(t, actions, "repository failure must not return cached or partial grants")
			require.Zero(t, ch.reads)
			require.Zero(t, ch.writes)
		})
	}
}

func TestEffectiveActionsFailsClosedForCancelledContextAndMissingScope(t *testing.T) {
	f := newEffectiveActionsFixture(t, model.Permission{PermissionID: "*:*", ResourceType: "*", Action: "*"})
	ch := &effectiveActionsCache{value: `["*:*"]`}
	a := f.authorizer(t, ch)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	actions, err := a.EffectiveActions(ctx, f.subject, "platform", "")
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, actions)
	for _, scope := range []string{"project", "pipeline"} {
		actions, err = a.EffectiveActions(context.Background(), f.subject, scope, "missing")
		require.Error(t, err)
		require.Nil(t, actions)
	}
	require.Zero(t, ch.reads)
	require.Zero(t, ch.writes)
}
