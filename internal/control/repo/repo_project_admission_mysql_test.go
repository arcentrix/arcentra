// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func projectAdmissionMySQLConfig(dsn string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid project admission acceptance DSN")
	}
	const prefix = "arcentra_project_admission_test"
	if (cfg.DBName != prefix && !strings.HasPrefix(cfg.DBName, prefix+"_")) || len(cfg.DBName) > 64 || strings.Trim(cfg.DBName, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return nil, errors.New("dedicated project admission test schema required")
	}
	switch cfg.Net {
	case "unix":
		if !filepath.IsAbs(cfg.Addr) {
			return nil, errors.New("absolute local socket required")
		}
	case "tcp":
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
			return nil, errors.New("explicit loopback host required")
		}
	default:
		return nil, errors.New("local MySQL connection required")
	}
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 15*time.Second, 15*time.Second
	cfg.ParseTime, cfg.Loc, cfg.MultiStatements, cfg.ClientFoundRows = true, time.UTC, false, false
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["transaction_isolation"] = "'REPEATABLE-READ'"
	return cfg, nil
}

func TestProjectAdmissionMySQLDSNGuards(t *testing.T) {
	for _, value := range []string{"test@tcp(127.0.0.1:3306)/production", "test@tcp(remote.example:3306)/arcentra_project_admission_test", "test@unix(relative.sock)/arcentra_project_admission_test", "test@tcp(127.0.0.1:3306)/arcentra_project_admission_testunsafe"} {
		if _, err := projectAdmissionMySQLConfig(value); err == nil {
			t.Fatal("unsafe DSN accepted")
		}
	}
	for _, value := range []string{"test@tcp(127.0.0.1:3306)/arcentra_project_admission_test", "test@tcp([::1]:3306)/arcentra_project_admission_test_ci", "test@unix(/tmp/mysql.sock)/arcentra_project_admission_test"} {
		if _, err := projectAdmissionMySQLConfig(value); err != nil {
			t.Fatal(err)
		}
	}
}

// Use the same schema-aware JSON defaults/validation as the retry acceptance
// lane. SQLite alone cannot catch empty-string JSON rejected by actual MySQL.
func projectAdmissionMySQLEntities(project, pipeline, runID, source string) ([]any, *model.PipelineRun) {
	rows := []any{
		&model.Project{ProjectID: project, OrgID: "legacy", IsEnabled: 1, Status: model.ProjectStatusActive, BuildConfig: datatypes.JSON("{}"), EnvVars: datatypes.JSON("{}"), Settings: datatypes.JSON("{}")},
		&model.Pipeline{PipelineID: pipeline, ProjectID: project, IsEnabled: 1},
	}
	run := &model.PipelineRun{RunID: runID, PipelineID: pipeline, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: "actor", RequestID: "manual-" + runID, Status: model.PipelineStatusPending}
	if source == "automatic" {
		run.TriggerType, run.TriggeredBy, run.RequestID = int(pipelinev1.TriggerType_TRIGGER_TYPE_CRON), "cron", "cron:"+pipeline+":fixture"
	}
	for _, row := range append(rows, run) {
		retryApprovalMySQLJSONDefaults(row)
	}
	return rows, run
}

func TestProjectAdmissionMySQLFixtureJSONDefaults(t *testing.T) {
	repository, _ := automaticAdmissionRepository(t)
	for _, source := range []string{"manual", "automatic"} {
		rows, run := projectAdmissionMySQLEntities("fixture-project", "fixture-pipeline", "fixture-run", source)
		for _, row := range append(rows, run) {
			if err := retryApprovalMySQLValidateJSON(repository.Database(), row); err != nil {
				t.Fatal(err)
			}
		}
		if rows[1].(*model.Pipeline).Metadata != "{}" || run.Env != "{}" {
			t.Fatal("fixture JSON objects lost semantic defaults")
		}
	}
}

// The lane must explicitly provide a fresh local disposable schema. This test
// never creates databases, starts MySQL, discovers credentials or relaxes access.
func TestProjectAdmissionMySQLArchiveSerialization(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_PROJECT_ADMISSION_MYSQL")
	if required != "" && required != "1" {
		t.Fatal("ARCENTRA_REQUIRE_PROJECT_ADMISSION_MYSQL must be 1 or unset")
	}
	dsn := os.Getenv("ARCENTRA_PROJECT_ADMISSION_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required project admission MySQL DSN missing")
		}
		t.Skip("explicit local project admission MySQL fixture absent; no MySQL acceptance claim")
	}
	cfg, err := projectAdmissionMySQLConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	open := func() *gorm.DB {
		db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("cannot open explicit project admission test database")
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(3)
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	db, other := open(), open()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tables, err := db.WithContext(ctx).Migrator().GetTables()
	if err != nil || len(tables) != 0 {
		t.Fatal("project admission fixture must be a fresh empty schema")
	}
	// Exercise the production migration, including immutable settings revision
	// and project-identity uniqueness, instead of a permissive partial schema.
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		models := schemaModels()
		for i := len(models) - 1; i >= 0; i-- {
			if err := db.WithContext(clean).Migrator().DropTable(models[i]); err != nil {
				t.Error("disposable project admission table cleanup failed")
			}
		}
	})
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatal("repeated production migration", err)
	}
	for _, firstArchive := range []bool{true, false} {
		t.Run(fmt.Sprintf("polling/archive-first-%v", firstArchive), func(t *testing.T) {
			projectAdmissionPollingArchive(t, newPollingMySQLFixture(t, db, other), firstArchive, cfg.DBName)
		})
	}
	for _, source := range []string{"manual", "automatic"} {
		for _, firstArchive := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/archive-first-%v", source, firstArchive), func(t *testing.T) {
				suffix := fmt.Sprintf("%s-%v", source, firstArchive)
				project, pipeline, runID := "project-"+suffix, "pipeline-"+suffix, "run-"+suffix
				rows, run := projectAdmissionMySQLEntities(project, pipeline, runID, source)
				// Actual MySQL rewrites JSON spacing/key order on storage. Both
				// admission paths must retain semantic nonempty environment identity.
				run.Env = `{"SECOND":"two","FIRST":"one"}`
				for _, row := range append(rows, run) {
					if err := retryApprovalMySQLValidateJSON(db, row); err != nil {
						t.Fatal(err)
					}
				}
				for _, row := range rows {
					if err := db.WithContext(ctx).Create(row).Error; err != nil {
						t.Fatal(err)
					}
				}
				repository := &PipelineRepo{IDatabase: statusTestDB{db}}
				if err := repository.CreateRun(ctx, run); err != nil {
					t.Fatal(err)
				}
				firstLocked, secondAttempted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var firstOnce, secondOnce sync.Once
				hook1, hook2 := "project-admission:first:"+suffix, "project-admission:second:"+suffix
				// The first transaction holds the actual InnoDB project row lock.
				// The second reaches its competing current read before release.
				if err := db.Callback().Query().After("gorm:query").Register(hook1, func(tx *gorm.DB) {
					if tx.Statement.Table == "project" && tx.Error == nil {
						firstOnce.Do(func() {
							close(firstLocked)
							select {
							case <-release:
							case <-ctx.Done():
								tx.AddError(ctx.Err())
							}
						})
					}
				}); err != nil {
					t.Fatal(err)
				}
				if err := other.Callback().Query().Before("gorm:query").Register(hook2, func(tx *gorm.DB) {
					if tx.Statement.Table == "project" {
						secondOnce.Do(func() { close(secondAttempted) })
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer db.Callback().Query().Remove(hook1)
				defer other.Callback().Query().Remove(hook2)
				admit := func(database *gorm.DB) (bool, error) {
					repo := &PipelineRepo{IDatabase: statusTestDB{database}}
					if source == "manual" {
						return repo.StartManualRunIfPending(ctx, run)
					}
					return repo.StartAutomaticRunIfPending(ctx, run)
				}
				archive := func(database *gorm.DB) error {
					return NewProjectRepo(statusTestDB{database}).Update(ctx, project, map[string]any{"status": model.ProjectStatusArchived})
				}
				type result struct {
					admitted bool
					err      error
				}
				first, second := make(chan result, 1), make(chan result, 1)
				go func() {
					if firstArchive {
						first <- result{err: archive(db)}
					} else {
						a, e := admit(db)
						first <- result{a, e}
					}
				}()
				select {
				case <-firstLocked:
				case <-ctx.Done():
					t.Fatal("first transaction never acquired project lock")
				}
				go func() {
					if firstArchive {
						a, e := admit(other)
						second <- result{a, e}
					} else {
						second <- result{err: archive(other)}
					}
				}()
				select {
				case <-secondAttempted:
				case <-ctx.Done():
					close(release)
					t.Fatal("second transaction never attempted project lock")
				}
				close(release)
				a, b := <-first, <-second
				if a.err != nil || b.err != nil {
					t.Fatalf("ordered race failed: first=%v second=%v", a.err, b.err)
				}
				admitted := a.admitted
				if firstArchive {
					admitted = b.admitted
				}
				if admitted == firstArchive {
					t.Fatal("archive/admission order was not serialized by project lock")
				}
				stored, err := repository.GetRun(ctx, runID)
				if err != nil {
					t.Fatal(err)
				}
				expected := model.PipelineStatusRunning
				if firstArchive {
					expected = model.PipelineStatusPending
				}
				if stored.Status != expected || stored.EndTime != nil {
					t.Fatal("archive rewrote admission outcome")
				}
				if firstArchive {
					if stored.ProjectID != "" {
						t.Fatal("denied admission manufactured project secret authority")
					}
				} else {
					if stored.ProjectID != project {
						t.Fatal("admission did not pin the locked original project")
					}
					replacementRows, _ := projectAdmissionMySQLEntities("replacement-"+suffix, "unused", "unused", "manual")
					if err := db.WithContext(ctx).Create(replacementRows[0]).Error; err != nil {
						t.Fatal(err)
					}
					if err := repository.Update(ctx, pipeline, map[string]any{"project_id": "replacement-" + suffix}); err != nil {
						t.Fatal(err)
					}
					pinned, err := repository.GetRunSecretProject(ctx, runID, pipeline)
					if err != nil || pinned != project {
						t.Fatal("pipeline reassignment changed admitted secret authority", err)
					}
				}
				if replay, err := admit(other); err != nil || replay {
					t.Fatal("archived scope admitted replay", err)
				}
			})
		}
	}
	for _, source := range []string{"manual", "automatic"} {
		for _, moveFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/project-move-first-%v", source, moveFirst), func(t *testing.T) {
				suffix := fmt.Sprintf("move-%s-%v", source, moveFirst)
				project, destination, pipeline, runID := "source-"+suffix, "target-"+suffix, "pipeline-"+suffix, "run-"+suffix
				rows, run := projectAdmissionMySQLEntities(project, pipeline, runID, source)
				targetRows, _ := projectAdmissionMySQLEntities(destination, "unused", "unused", source)
				for _, row := range append(rows, targetRows[0]) {
					if err := db.WithContext(ctx).Create(row).Error; err != nil {
						t.Fatal(err)
					}
				}
				r := &PipelineRepo{IDatabase: statusTestDB{db}}
				if err := r.CreateRun(ctx, run); err != nil {
					t.Fatal(err)
				}
				locked, attempted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var firstOnce, secondOnce sync.Once
				hook1, hook2 := "project-pin:first:"+suffix, "project-pin:second:"+suffix
				if err := db.Callback().Query().After("gorm:query").Register(hook1, func(tx *gorm.DB) {
					if tx.Statement.Table == "pipeline" && tx.Error == nil {
						firstOnce.Do(func() {
							close(locked)
							select {
							case <-release:
							case <-ctx.Done():
								tx.AddError(ctx.Err())
							}
						})
					}
				}); err != nil {
					t.Fatal(err)
				}
				if err := other.Callback().Query().Before("gorm:query").Register(hook2, func(tx *gorm.DB) {
					if tx.Statement.Table == "pipeline" {
						secondOnce.Do(func() { close(attempted) })
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer db.Callback().Query().Remove(hook1)
				defer other.Callback().Query().Remove(hook2)
				type result struct {
					admitted bool
					err      error
				}
				admit := func(database *gorm.DB) result {
					repository := &PipelineRepo{IDatabase: statusTestDB{database}}
					var admitted bool
					var err error
					if source == "manual" {
						admitted, err = repository.StartManualRunIfPending(ctx, run)
					} else {
						admitted, err = repository.StartAutomaticRunIfPending(ctx, run)
					}
					return result{admitted, err}
				}
				move := func(database *gorm.DB) result {
					return result{err: (&PipelineRepo{IDatabase: statusTestDB{database}}).Update(ctx, pipeline, map[string]any{"project_id": destination})}
				}
				first, second := make(chan result, 1), make(chan result, 1)
				go func() {
					if moveFirst {
						first <- move(db)
					} else {
						first <- admit(db)
					}
				}()
				select {
				case <-locked:
				case <-ctx.Done():
					close(release)
					t.Fatal("first transaction never locked pipeline")
				}
				go func() {
					if moveFirst {
						second <- admit(other)
					} else {
						second <- move(other)
					}
				}()
				select {
				case <-attempted:
				case <-ctx.Done():
					close(release)
					t.Fatal("second transaction never attempted pipeline lock")
				}
				close(release)
				a, b := <-first, <-second
				if a.err != nil || b.err != nil || !(a.admitted || b.admitted) {
					t.Fatalf("move/admission race failed: first=%v second=%v", a.err, b.err)
				}
				want := project
				if moveFirst {
					want = destination
				}
				pinned, err := r.GetRunSecretProject(ctx, runID, pipeline)
				if err != nil || pinned != want {
					t.Fatalf("secret project pin crossed pipeline lock: got=%s want=%s err=%v", pinned, want, err)
				}
			})
		}
	}

}

// The same scoped-authority fixture and protocol assertions run locally. SQLite
// verifies setup, identity, receipts and lock order, not InnoDB serialization.
func TestProjectAdmissionPollingArchiveFixture(t *testing.T) {
	for _, firstArchive := range []bool{true, false} {
		t.Run(fmt.Sprintf("archive-first-%v", firstArchive), func(t *testing.T) {
			f := projectAdmissionPollingSQLiteFixture(t)
			projectAdmissionPollingArchive(t, f, firstArchive, "")
		})
	}
}

func projectAdmissionPollingSQLiteFixture(t *testing.T) *pollingMySQLFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "project-polling-admission.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, MigrateSchema(context.Background(), db))
	return newPollingMySQLFixture(t, db, db)
}

func projectAdmissionPollingPending(t *testing.T, f *pollingMySQLFixture) (*model.SCMPollingStream, *model.PipelineRun) {
	t.Helper()
	// This prepares a real actor, scoped grant and permission bindings, then
	// baselines, enables, captures observations and freezes a selected batch.
	stream, run := f.prepare(t)
	run.Env = `{"SECOND":"two","FIRST":"one"}`
	project, err := NewProjectRepo(statusTestDB{f.db}).Get(f.ctx, f.projectID)
	require.NoError(t, err)
	pipeline, err := f.runs.Get(f.ctx, f.pipelineID)
	require.NoError(t, err)
	for _, row := range []any{project, pipeline, run} {
		require.NoError(t, retryApprovalMySQLValidateJSON(f.db, row))
	}
	captured, err := pollingRunContext(run)
	require.NoError(t, err)
	require.Equal(t, polling.Actor, run.TriggeredBy)
	require.Equal(t, f.actorID, captured.ActorID)
	require.Equal(t, f.scope, captured.Scope)
	require.Equal(t, f.pipelineID, captured.Target.PipelineID)
	require.Equal(t, stream.Revision, captured.BatchRevision)
	require.Equal(t, polling.StateEnabled, stream.State)
	batch, err := polling.ParseBatch(stream.PendingBatchJSON)
	require.NoError(t, err)
	require.Equal(t, []polling.Target{captured.Target}, batch.Targets)
	require.Empty(t, batch.Results)
	f.create(t, run)
	stored := f.stored(t, run)
	require.True(t, samePollingRunIdentity(stored, run))
	require.Empty(t, stored.ProjectID, "creation cannot grant project secret authority")
	require.Nil(t, stored.StartTime)
	require.Nil(t, stored.EndTime)
	_, err = f.runs.GetRunSecretProject(f.ctx, run.RunID, run.PipelineID)
	require.Error(t, err)
	return stream, run
}

func projectAdmissionPollingArchive(t *testing.T, f *pollingMySQLFixture, firstArchive bool, schema string) {
	t.Helper()
	stream, run := projectAdmissionPollingPending(t, f)
	var admission polling.AdmissionOutcome
	admit := func(ctx context.Context, db *gorm.DB) error {
		var err error
		admission, err = (&PipelineRepo{IDatabase: statusTestDB{db}}).StartPollingRunIfPending(ctx, run)
		return err
	}
	archive := func(ctx context.Context, db *gorm.DB) error {
		return NewProjectRepo(statusTestDB{db}).Update(ctx, f.projectID, map[string]any{"status": model.ProjectStatusArchived})
	}
	first, second := admit, archive
	admissionIndex, archiveIndex := 0, 1
	if firstArchive {
		first, second = archive, admit
		admissionIndex, archiveIndex = 1, 0
	}
	var errs []error
	if schema != "" {
		// Actual independent-connection MySQL contention on the project row,
		// the first shared current-read lock in archive and polling admission.
		errs = projectAdmissionPollingMySQLOrderedRace(t, f, schema, first, second)
	} else {
		// SQLite cannot establish concurrent InnoDB row-lock behavior. Exercise
		// the same two serial outcomes and assert the production locking clauses.
		runInOrder := func(operation func(context.Context, *gorm.DB) error, isArchive bool) error {
			var locks []string
			name := "project-admission:polling-lock-order"
			require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				for _, table := range []string{"pipeline", "project", "scm_polling_stream"} {
					if pollingMySQLLockedQuery(tx, table) && (len(locks) == 0 || locks[len(locks)-1] != table) {
						if table == "project" {
							require.True(t, projectAdmissionPollingProjectLock(tx, f.projectID), "MySQL gate must recognize the exact shared locking read")
							require.False(t, projectAdmissionPollingProjectLock(tx, "another-project"))
						}
						locks = append(locks, table)
					}
				}
			}))
			defer f.db.Callback().Query().Remove(name)
			err := operation(f.ctx, f.db)
			want := []string{"pipeline", "project", "scm_polling_stream"}
			if isArchive {
				want = []string{"project", "scm_polling_stream"}
			}
			require.Equal(t, want, locks, "archive must never acquire pipeline after project")
			return err
		}
		errs = []error{runInOrder(first, firstArchive), runInOrder(second, !firstArchive)}
	}
	require.NoError(t, errs[archiveIndex], "archive must complete without a lock inversion")
	if firstArchive {
		require.ErrorIs(t, errs[admissionIndex], polling.ErrConflict, "archive invalidates the captured activation generation")
		require.False(t, admission.ClaimedNow)
		require.Equal(t, polling.OutcomeUnresolved, admission.Outcome)
	} else {
		require.NoError(t, errs[admissionIndex])
		require.True(t, admission.ClaimedNow)
		require.Equal(t, polling.OutcomeAdmitted, admission.Outcome)
		require.Equal(t, run.RunID, admission.RunID)
	}
	stored, latest := f.stored(t, run), f.latest(t)
	require.True(t, samePollingRunIdentity(stored, run), "archive must retain the frozen event identity")
	require.Empty(t, run.ProjectID, "admission must not mutate the shared caller input")
	require.Nil(t, stored.EndTime, "archive cannot cancel a pending or admitted run")
	require.Equal(t, polling.StatePaused, latest.State)
	require.Equal(t, "configuration_changed", latest.PauseReason)
	require.Equal(t, stream.ActivationGeneration+1, latest.ActivationGeneration)
	wantRevision := stream.Revision + 1
	if !firstArchive {
		wantRevision++ // admission receipt, then archive invalidation
	}
	require.Equal(t, wantRevision, latest.Revision)
	require.Equal(t, stream.CursorJSON, latest.CursorJSON, "archive cannot acknowledge a batch")
	require.Equal(t, stream.SelectionJSON, latest.SelectionJSON)
	project, err := NewProjectRepo(statusTestDB{f.other}).Get(f.ctx, f.projectID)
	require.NoError(t, err)
	require.Equal(t, model.ProjectStatusArchived, project.Status)
	batch, err := polling.ParseBatch(latest.PendingBatchJSON)
	require.NoError(t, err)
	originalBatch, err := polling.ParseBatch(stream.PendingBatchJSON)
	require.NoError(t, err)
	withoutReceipts := *batch
	withoutReceipts.Results = originalBatch.Results
	require.Equal(t, *originalBatch, withoutReceipts, "archive must preserve frozen targets and cursor evidence")
	if firstArchive {
		require.Equal(t, model.PipelineStatusPending, stored.Status)
		require.Nil(t, stored.StartTime)
		require.Empty(t, stored.ProjectID, "denied admission cannot manufacture secret authority")
		require.Equal(t, stream.PendingBatchJSON, latest.PendingBatchJSON)
		require.Empty(t, batch.Results)
		_, err := f.runs.GetRunSecretProject(f.ctx, run.RunID, run.PipelineID)
		require.Error(t, err)
	} else {
		require.Equal(t, model.PipelineStatusRunning, stored.Status)
		require.NotNil(t, stored.StartTime)
		require.Equal(t, f.projectID, stored.ProjectID)
		require.Equal(t, []polling.TargetResult{{PipelineID: run.PipelineID, RequestID: run.RequestID, RunID: run.RunID, Outcome: polling.OutcomeAdmitted}}, batch.Results)
		pinned, err := f.runs.GetRunSecretProject(f.ctx, run.RunID, run.PipelineID)
		require.NoError(t, err)
		require.Equal(t, f.projectID, pinned)
	}
	// Check replay before moving the pipeline, so denial specifically proves the
	// archived generation cannot reclaim even an already-admitted polling run.
	replay, err := f.otherRuns.StartPollingRunIfPending(f.ctx, run)
	require.ErrorIs(t, err, polling.ErrConflict)
	require.False(t, replay.ClaimedNow)
	require.Equal(t, polling.OutcomeUnresolved, replay.Outcome)
	afterReplay := f.stored(t, run)
	require.Equal(t, stored.ProjectID, afterReplay.ProjectID)
	require.Equal(t, stored.Status, afterReplay.Status)
	require.True(t, sameOptionalTime(stored.StartTime, afterReplay.StartTime))
	require.Nil(t, afterReplay.EndTime)
	streamAfterReplay := f.latest(t)
	require.Equal(t, latest.PendingBatchJSON, streamAfterReplay.PendingBatchJSON)
	require.Equal(t, latest.Revision, streamAfterReplay.Revision)
	if !firstArchive {
		replacementID := f.projectID + "-replacement"
		rows, _ := projectAdmissionMySQLEntities(replacementID, "unused", "unused", "manual")
		require.NoError(t, f.db.WithContext(f.ctx).Create(rows[0]).Error)
		require.NoError(t, f.runs.Update(f.ctx, f.pipelineID, map[string]any{"project_id": replacementID}))
		pinned, err := f.otherRuns.GetRunSecretProject(f.ctx, run.RunID, run.PipelineID)
		require.NoError(t, err)
		require.Equal(t, f.projectID, pinned, "pipeline reassignment cannot change admitted secret authority")
	}
}

func TestProjectAdmissionPollingFixtureRequiresLiveAuthority(t *testing.T) {
	for _, missing := range []string{"actor", "grant", "permission-binding"} {
		t.Run(missing, func(t *testing.T) {
			f := projectAdmissionPollingSQLiteFixture(t)
			_, run := projectAdmissionPollingPending(t, f)
			var err error
			switch missing {
			case "actor":
				err = f.db.Model(&model.User{}).Where("user_id = ?", f.actorID).Update("is_enabled", 0).Error
			case "grant":
				err = f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", f.grantID).Update("is_enabled", 0).Error
			case "permission-binding":
				err = f.db.Where("role_id = ? AND permission_id = ?", f.roleID, "pipeline:trigger").Delete(&model.RolePermissionBinding{}).Error
			}
			require.NoError(t, err)
			outcome, err := f.runs.StartPollingRunIfPending(f.ctx, run)
			require.ErrorIs(t, err, polling.ErrForbidden)
			require.False(t, outcome.ClaimedNow)
			require.Equal(t, polling.OutcomeRejected, outcome.Outcome)
			stored := f.stored(t, run)
			require.Empty(t, stored.ProjectID)
			require.Nil(t, stored.StartTime)
			_, err = f.runs.GetRunSecretProject(f.ctx, run.RunID, run.PipelineID)
			require.Error(t, err)
		})
	}
}
