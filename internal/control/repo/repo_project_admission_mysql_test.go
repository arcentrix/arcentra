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
	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/datatypes"
	"gorm.io/driver/mysql"
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
