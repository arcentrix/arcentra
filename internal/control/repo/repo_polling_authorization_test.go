// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
	"path/filepath"
	"testing"
)

func TestPollingPrimaryViewFailsClosedWithoutDatabase(t *testing.T) {
	for _, r := range []*Repositories{nil, {}, {database: statusTestDB{nil}}} {
		if view, e := r.PollingPrimaryRepositories(); e == nil || view != nil {
			t.Fatal("missing database created view")
		}
		if e := SetPollingAuthorizer(r, nil); e == nil {
			t.Fatal("missing database accepted authorizer")
		}
	}
}
func TestPollingPrimaryViewBypassesReplicaAndDoesNotReuseConditions(t *testing.T) {
	f := newPollingFixture(t)
	if e := f.db.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty-replica.db"))}})); e != nil {
		t.Fatal(e)
	}
	view, e := f.repos.PollingPrimaryRepositories()
	if e != nil {
		t.Fatal(e)
	}
	if view.Storage != nil || view.Setting != nil || view.AuditLog != nil || view.database == nil {
		t.Fatal("primary view exposed unrelated dependencies")
	}
	if _, e := view.Project.Get(f.ctx, "project"); e != nil {
		t.Fatal("project used replica", e)
	}
	if _, e := view.Pipeline.Get(f.ctx, "pipeline"); e != nil {
		t.Fatal("pipeline used replica", e)
	}
	if _, e := view.User.(IExecutionActorReader).GetExecutionActor(f.ctx, "actor"); e != nil {
		t.Fatal("actor used replica", e)
	}
	if _, e := view.TeamMember.ListUserTeams(f.ctx, "actor"); e != nil {
		t.Fatal(e)
	}
	if _, e := view.Team.BatchGet(f.ctx, []string{"missing"}); e != nil {
		t.Fatal(e)
	}
	if _, e := view.Permission.ListPermissions(f.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := view.Permission.ListAllRoleBindings(f.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := view.Role.BatchGet(f.ctx, []string{"missing"}); e != nil {
		t.Fatal(e)
	}
	if _, e := view.RoleGrant.ListAllEnabled(f.ctx); e != nil {
		t.Fatal(e)
	}
	if rows, e := view.SCMPolling.ListEnabled(f.ctx); e != nil || len(rows) != 0 {
		t.Fatal(e)
	}
	db := view.database.Database().Where("project_id = ?", "not-project")
	var missing model.Project
	if e := db.WithContext(f.ctx).Select("project_id").First(&missing).Error; e == nil {
		t.Fatal("conditional fixture unexpectedly matched")
	}
	if got, e := view.Project.Get(f.ctx, "project"); e != nil || got.ProjectID != "project" {
		t.Fatal("view reused prior query conditions", e)
	}
	if e := SetPollingAuthorizer(view, nil); e != nil {
		t.Fatal("view lost underlying database identity", e)
	}
}
