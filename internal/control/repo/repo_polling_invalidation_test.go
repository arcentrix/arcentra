// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type pollingInvalidationFixture struct {
	ctx       context.Context
	db        *gorm.DB
	projects  *ProjectRepo
	pipelines *PipelineRepo
}

func newPollingInvalidationFixture(t *testing.T, withStreams bool) pollingInvalidationFixture {
	t.Helper()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "invalidation.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.WithContext(ctx).AutoMigrate(&model.Project{}, &model.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	if withStreams {
		if err := db.WithContext(ctx).AutoMigrate(&model.SCMPollingStream{}); err != nil {
			t.Fatal(err)
		}
	}
	f := pollingInvalidationFixture{ctx: ctx, db: db, projects: &ProjectRepo{IDatabase: statusTestDB{db}}, pipelines: &PipelineRepo{IDatabase: statusTestDB{db}}}
	for _, id := range []string{"project-a", "project-b"} {
		project := &model.Project{ProjectID: id, RepoURL: "https://gitlab.example/team/repo.git", RepoType: model.RepoTypeGitLab,
			DefaultBranch: "main", AuthType: model.AuthTypeToken, Credential: "synthetic-credential", TriggerMode: model.TriggerModeMR,
			IsEnabled: 1, Status: model.ProjectStatusActive, Settings: datatypes.JSON(` {"legacy_cursor":"unchanged"} `)}
		if err := f.projects.Create(ctx, project); err != nil {
			t.Fatal(err)
		}
		if withStreams {
			selection := polling.Selection{Version: polling.Version, Pipelines: []polling.PipelineBinding{{PipelineID: "pipeline-a", Repository: "https://gitlab.example/team/repo", DefaultBranch: "main", DefinitionPath: "pipeline.json"}}}
			selectionJSON, err := selection.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			selectionDigest, _ := selection.Digest()
			stream := &model.SCMPollingStream{StreamID: "stream-" + id, ProjectID: id, ScopeDigest: "scope-" + id,
				Revision: 4, ActivationGeneration: 9, State: "enabled", EnabledBy: "synthetic-actor", BaselineID: "baseline-" + id,
				SelectionJSON: selectionJSON, SelectionDigest: selectionDigest, CursorJSON: `{"boundary":"precise"}`,
				PendingBatchJSON: `{"observations":["unacknowledged"]}`}
			if err := db.WithContext(ctx).Create(stream).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.pipelines.Create(ctx, &model.Pipeline{PipelineID: "pipeline-a", ProjectID: "project-a", RepoURL: "https://gitlab.example/team/repo.git", DefaultBranch: "main", PipelineFilePath: "pipeline.json", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f pollingInvalidationFixture) stream(t *testing.T, projectID string) model.SCMPollingStream {
	t.Helper()
	var stream model.SCMPollingStream
	if err := f.db.WithContext(f.ctx).Select("stream_id", "project_id", "scope_digest", "revision", "activation_generation", "state", "pause_reason", "enabled_by", "baseline_id", "selection_json", "selection_digest", "cursor_json", "pending_batch_json").Where("project_id = ?", projectID).Take(&stream).Error; err != nil {
		t.Fatal(err)
	}
	return stream
}

func requirePollingInvalidated(t *testing.T, before, after model.SCMPollingStream, increments uint64) {
	t.Helper()
	if after.Revision != before.Revision+increments || after.ActivationGeneration != before.ActivationGeneration+increments || after.State != "paused" || after.PauseReason != "configuration_changed" {
		t.Fatalf("invalidation state = %s/%s revision=%d generation=%d, want paused/configuration_changed with +%d", after.State, after.PauseReason, after.Revision, after.ActivationGeneration, increments)
	}
	before.State, before.PauseReason, before.Revision, before.ActivationGeneration = after.State, after.PauseReason, after.Revision, after.ActivationGeneration
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalidation changed retained scope, selection, actor, baseline, cursor or pending-batch evidence")
	}
}

func TestPollingProjectConfigurationMutationsInvalidate(t *testing.T) {
	cases := map[string]any{
		"repo_url": "https://gitlab.example/team/changed.git", "repo_type": model.RepoTypeGit,
		"default_branch": "other", "auth_type": model.AuthTypeNone, "credential": "synthetic-replacement",
		"trigger_mode": model.TriggerModeManual, "is_enabled": 0, "status": model.ProjectStatusArchived,
		"RepoURL": "https://gitlab.example/team/field-name.git", "DefaultBranch": "field-name",
	}
	for column, value := range cases {
		t.Run(column, func(t *testing.T) {
			f := newPollingInvalidationFixture(t, true)
			before := f.stream(t, "project-a")
			other := f.stream(t, "project-b")
			if err := f.projects.Update(f.ctx, "project-a", map[string]any{column: value}); err != nil {
				t.Fatal(err)
			}
			requirePollingInvalidated(t, before, f.stream(t, "project-a"), 1)
			if !reflect.DeepEqual(other, f.stream(t, "project-b")) {
				t.Fatal("unrelated project stream changed")
			}
			project, err := f.projects.Get(f.ctx, "project-a")
			if err != nil || string(project.Settings) != ` {"legacy_cursor":"unchanged"} ` {
				t.Fatalf("legacy settings changed: %v", err)
			}
		})
	}
}

func TestPollingPipelineConfigurationMutationsInvalidate(t *testing.T) {
	cases := map[string]any{
		"repo_url": "https://gitlab.example/team/changed.git", "default_branch": "other", "pipeline_file_path": "other.json",
		"project_id": "project-b", "is_enabled": 0, "is_deleted": 1,
		"RepoURL": "https://gitlab.example/team/field-name.git", "PipelineFilePath": "field-name.json", "ProjectID": "project-b",
	}
	for column, value := range cases {
		t.Run(column, func(t *testing.T) {
			f := newPollingInvalidationFixture(t, true)
			before, other := f.stream(t, "project-a"), f.stream(t, "project-b")
			if err := f.pipelines.Update(f.ctx, "pipeline-a", map[string]any{column: value}); err != nil {
				t.Fatal(err)
			}
			requirePollingInvalidated(t, before, f.stream(t, "project-a"), 1)
			if column == "project_id" || column == "ProjectID" {
				requirePollingInvalidated(t, other, f.stream(t, "project-b"), 1)
			} else if !reflect.DeepEqual(other, f.stream(t, "project-b")) {
				t.Fatal("unrelated project stream changed")
			}
		})
	}
}

func TestPollingConfigurationPauseSurvivesRestoration(t *testing.T) {
	for _, kind := range []string{"project", "pipeline"} {
		t.Run(kind, func(t *testing.T) {
			f := newPollingInvalidationFixture(t, true)
			before := f.stream(t, "project-a")
			mutate, id := f.projects.Update, "project-a"
			if kind == "pipeline" {
				mutate, id = f.pipelines.Update, "pipeline-a"
			}
			for _, branch := range []string{"changed", "main"} {
				if err := mutate(f.ctx, id, map[string]any{"default_branch": branch}); err != nil {
					t.Fatal(err)
				}
			}
			requirePollingInvalidated(t, before, f.stream(t, "project-a"), 2)
		})
	}
}

func TestPollingDedicatedProjectLifecycleMutationsInvalidate(t *testing.T) {
	f := newPollingInvalidationFixture(t, true)
	before := f.stream(t, "project-a")
	for i, mutate := range []func(context.Context, string) error{f.projects.Disable, f.projects.Enable, f.projects.Delete} {
		if err := mutate(f.ctx, "project-a"); err != nil {
			t.Fatal(err)
		}
		requirePollingInvalidated(t, before, f.stream(t, "project-a"), uint64(i+1))
	}
	project, err := f.projects.Get(f.ctx, "project-a")
	if err != nil || project.Status != model.ProjectStatusDisabled {
		t.Fatalf("dedicated delete did not change status: %v", err)
	}
}

func TestPollingOrdinaryAndUnchangedWritesPreserveAuthority(t *testing.T) {
	f := newPollingInvalidationFixture(t, true)
	before := f.stream(t, "project-a")
	for _, updates := range []map[string]any{
		{"display_name": "New display", "description": "New description", "settings": datatypes.JSON(` {"another":"setting"} `)},
		{"repo_url": "https://gitlab.example/team/repo.git", "repo_type": model.RepoTypeGitLab, "default_branch": "main", "auth_type": model.AuthTypeToken, "credential": "synthetic-credential", "trigger_mode": model.TriggerModeMR, "is_enabled": 1, "status": model.ProjectStatusActive},
	} {
		if err := f.projects.Update(f.ctx, "project-a", updates); err != nil {
			t.Fatal(err)
		}
	}
	for _, updates := range []map[string]any{
		{"name": "New name", "description": "New description", "metadata": `{"saved":true}`, "status": model.PipelineStatusSuccess, "last_commit_sha": "new-approved-branch-head"},
		{"repo_url": "https://gitlab.example/team/repo.git", "default_branch": "main", "pipeline_file_path": "pipeline.json", "project_id": "project-a", "is_enabled": 1, "is_deleted": 0},
	} {
		if err := f.pipelines.Update(f.ctx, "pipeline-a", updates); err != nil {
			t.Fatal(err)
		}
	}
	count := 7
	if err := f.projects.UpdateStatistics(f.ctx, "project-a", &model.ProjectStatisticsReq{TotalBuilds: &count}); err != nil {
		t.Fatal(err)
	}
	if err := f.projects.Enable(f.ctx, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := f.pipelines.IncrementRuns(f.ctx, "pipeline-a"); err != nil {
		t.Fatal(err)
	}
	if err := f.pipelines.RecordRunResult(f.ctx, "pipeline-a", model.PipelineStatusSuccess); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.stream(t, "project-a")) {
		t.Fatal("ordinary or unchanged configuration write modified polling authority")
	}
}

func TestPollingCreationDoesNotSelectOrActivate(t *testing.T) {
	f := newPollingInvalidationFixture(t, true)
	before := f.stream(t, "project-a")
	if err := f.pipelines.Create(f.ctx, &model.Pipeline{PipelineID: "new-pipeline", ProjectID: "project-a", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.stream(t, "project-a")) {
		t.Fatal("new pipeline changed selection or activation")
	}
	if err := f.projects.Create(f.ctx, &model.Project{ProjectID: "new-project", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.projects.Update(f.ctx, "new-project", map[string]any{"default_branch": "changed"}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.SCMPollingStream{}).Where("project_id = ?", "new-project").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("project creation/mutation added polling state: count=%d err=%v", count, err)
	}
}

func TestPollingInvalidationFailureRollsBackConfiguration(t *testing.T) {
	for _, kind := range []string{"project", "pipeline"} {
		t.Run(kind, func(t *testing.T) {
			f := newPollingInvalidationFixture(t, true)
			before := f.stream(t, "project-a")
			injected := errors.New("injected stream update failure")
			if err := f.db.Callback().Update().Before("gorm:update").Register("polling:test:fail", func(tx *gorm.DB) {
				if tx.Statement.Table == (model.SCMPollingStream{}).TableName() {
					tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "project" {
				err = f.projects.Update(f.ctx, "project-a", map[string]any{"default_branch": "changed"})
			} else {
				err = f.pipelines.Update(f.ctx, "pipeline-a", map[string]any{"default_branch": "changed"})
			}
			if !errors.Is(err, injected) {
				t.Fatalf("invalidation failure = %v", err)
			}
			project, projectErr := f.projects.Get(f.ctx, "project-a")
			pipeline, pipelineErr := f.pipelines.Get(f.ctx, "pipeline-a")
			if projectErr != nil || pipelineErr != nil || project.DefaultBranch != "main" || pipeline.DefaultBranch != "main" {
				t.Fatalf("configuration escaped rollback: project=%v pipeline=%v", projectErr, pipelineErr)
			}
			if !reflect.DeepEqual(before, f.stream(t, "project-a")) {
				t.Fatal("failed transaction changed stream state")
			}
		})
	}
}

func TestPollingLegacySchemaMutationsDoNotMigrate(t *testing.T) {
	f := newPollingInvalidationFixture(t, false)
	for _, mutate := range []func() error{
		func() error {
			return f.projects.Update(f.ctx, "project-a", map[string]any{"default_branch": "changed"})
		},
		func() error {
			return f.pipelines.Update(f.ctx, "pipeline-a", map[string]any{"pipeline_file_path": "changed.json"})
		},
		func() error { return f.projects.Disable(f.ctx, "project-a") },
		func() error { return f.projects.Enable(f.ctx, "project-a") },
		func() error { return f.projects.Delete(f.ctx, "project-a") },
	} {
		if err := mutate(); err != nil {
			t.Fatal(err)
		}
	}
	if f.db.WithContext(f.ctx).Migrator().HasTable(&model.SCMPollingStream{}) {
		t.Fatal("CRUD implicitly migrated polling schema")
	}
}

func TestPollingInvalidationDeclaresCommonLockOrder(t *testing.T) {
	// This inspects GORM clauses only. SQLite does not implement the MySQL
	// row-locking/race guarantees, which need separate integration coverage.
	for _, kind := range []string{"project", "pipeline-move"} {
		t.Run(kind, func(t *testing.T) {
			f := newPollingInvalidationFixture(t, true)
			var locks []string
			if err := f.db.Callback().Query().Before("gorm:query").Register("polling:test:locks", func(tx *gorm.DB) {
				lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
				if ok && lock.Strength == "UPDATE" {
					locks = append(locks, tx.Statement.Table)
				}
			}); err != nil {
				t.Fatal(err)
			}
			want := []string{"project", "project", "scm_polling_stream"}
			var err error
			if kind == "project" {
				err = f.projects.Disable(f.ctx, "project-a")
			} else {
				want = []string{"pipeline", "pipeline", "project", "project", "scm_polling_stream", "scm_polling_stream"}
				err = f.pipelines.Update(f.ctx, "pipeline-a", map[string]any{"project_id": "project-b"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(locks, want) {
				t.Fatalf("declared locks = %v, want %v", locks, want)
			}
		})
	}
}

func TestPollingInvalidationFailureRollsBackBothMovedProjects(t *testing.T) {
	f := newPollingInvalidationFixture(t, true)
	first, second := f.stream(t, "project-a"), f.stream(t, "project-b")
	injected := errors.New("injected second stream update failure")
	updates := 0
	if err := f.db.Callback().Update().Before("gorm:update").Register("polling:test:second-fail", func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SCMPollingStream{}).TableName() {
			updates++
			if updates == 2 {
				tx.AddError(injected)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.pipelines.Update(f.ctx, "pipeline-a", map[string]any{"project_id": "project-b"}); !errors.Is(err, injected) {
		t.Fatalf("move invalidation failure = %v", err)
	}
	pipeline, err := f.pipelines.Get(f.ctx, "pipeline-a")
	if err != nil || pipeline.ProjectID != "project-a" {
		t.Fatalf("pipeline move escaped rollback: %v", err)
	}
	if !reflect.DeepEqual(first, f.stream(t, "project-a")) || !reflect.DeepEqual(second, f.stream(t, "project-b")) {
		t.Fatal("partially completed invalidation escaped rollback")
	}
}

func TestPollingConfigurationInvalidatesEveryPriorState(t *testing.T) {
	for _, state := range []string{"disabled", "baselined", "enabled", "paused"} {
		t.Run(state, func(t *testing.T) {
			f := newPollingInvalidationFixture(t, true)
			if err := f.db.WithContext(f.ctx).Model(&model.SCMPollingStream{}).Where("project_id = ?", "project-a").Updates(map[string]any{"state": state, "pause_reason": "authorization_denied"}).Error; err != nil {
				t.Fatal(err)
			}
			before := f.stream(t, "project-a")
			if err := f.projects.Update(f.ctx, "project-a", map[string]any{"default_branch": "changed"}); err != nil {
				t.Fatal(err)
			}
			requirePollingInvalidated(t, before, f.stream(t, "project-a"), 1)
		})
	}
}

type pollingNonLegacyDialect struct{ gorm.Dialector }

func (pollingNonLegacyDialect) Name() string { return "mysql" }

func TestPollingMissingSchemaFailsClosedOutsideSQLiteCompatibility(t *testing.T) {
	// Only the dialect-name compatibility branch is exercised here; this is
	// deliberately not a claim about MySQL storage or locking behavior.
	for _, kind := range []string{"project", "pipeline"} {
		t.Run(kind, func(t *testing.T) {
			f := newPollingInvalidationFixture(t, false)
			f.db.Dialector = pollingNonLegacyDialect{Dialector: f.db.Dialector}
			mutate, id := f.projects.Update, "project-a"
			if kind == "pipeline" {
				mutate, id = f.pipelines.Update, "pipeline-a"
			}
			if err := mutate(f.ctx, id, map[string]any{"default_branch": "changed"}); err == nil {
				t.Fatal("missing polling schema silently bypassed invalidation")
			}
			project, projectErr := f.projects.Get(f.ctx, "project-a")
			pipeline, pipelineErr := f.pipelines.Get(f.ctx, "pipeline-a")
			if projectErr != nil || pipelineErr != nil || project.DefaultBranch != "main" || pipeline.DefaultBranch != "main" {
				t.Fatalf("missing schema allowed partial mutation: project=%v pipeline=%v", projectErr, pipelineErr)
			}
		})
	}
}

func TestPollingUnselectedPipelineMutationCannotPauseActivation(t *testing.T) {
	f := newPollingInvalidationFixture(t, true)
	before := f.stream(t, "project-a")
	if err := f.pipelines.Create(f.ctx, &model.Pipeline{PipelineID: "unselected", ProjectID: "project-a", RepoURL: "https://gitlab.example/team/repo", DefaultBranch: "main", PipelineFilePath: "pipeline.json", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	for _, updates := range []map[string]any{{"default_branch": "other"}, {"is_enabled": 0}, {"project_id": "project-b"}} {
		if err := f.pipelines.Update(f.ctx, "unselected", updates); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, f.stream(t, "project-a")) {
		t.Fatal("unselected editor paused active stream")
	}
}
func TestPollingMalformedSelectionRollsBackPipelineMutation(t *testing.T) {
	f := newPollingInvalidationFixture(t, true)
	if err := f.db.WithContext(f.ctx).Model(&model.SCMPollingStream{}).Where("project_id = ?", "project-a").Update("selection_json", "{}").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.pipelines.Update(f.ctx, "pipeline-a", map[string]any{"default_branch": "other"}); err == nil {
		t.Fatal("malformed selection accepted mutation")
	}
	p, err := f.pipelines.Get(f.ctx, "pipeline-a")
	if err != nil || p.DefaultBranch != "main" {
		t.Fatal("configuration escaped fail-closed rollback")
	}
}
