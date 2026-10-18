// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func projectAdmissionPollingProjectLock(tx *gorm.DB, projectID string) bool {
	if !pollingMySQLLockedQuery(tx, (model.Project{}).TableName()) {
		return false
	}
	where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
	if !ok {
		return false
	}
	for _, expression := range where.Exprs {
		if predicate, ok := expression.(clause.Expr); ok && predicate.SQL == "project_id = ?" && len(predicate.Vars) == 1 && predicate.Vars[0] == projectID {
			return true
		}
	}
	return false
}

func projectAdmissionPollingLockWaitProbe(ctx context.Context, db *gorm.DB, schema string, requesting, blocking, rowID uint64, projectID string) *gorm.DB {
	primary := strconv.FormatUint(rowID, 10)
	businessKeys := []string{projectID + ", " + primary, "'" + projectID + "', " + primary, strconv.Quote(projectID) + ", " + primary}
	return stepAttemptMySQLMetadataRead(ctx, db).Table("performance_schema.data_lock_waits AS waits").
		Select("waits.REQUESTING_ENGINE_LOCK_ID AS requesting_lock_id").
		Joins("JOIN performance_schema.data_locks AS requested ON requested.ENGINE = waits.ENGINE AND requested.ENGINE_LOCK_ID = waits.REQUESTING_ENGINE_LOCK_ID AND requested.THREAD_ID = waits.REQUESTING_THREAD_ID").
		Joins("JOIN performance_schema.data_locks AS blocking ON blocking.ENGINE = waits.ENGINE AND blocking.ENGINE_LOCK_ID = waits.BLOCKING_ENGINE_LOCK_ID AND blocking.THREAD_ID = waits.BLOCKING_THREAD_ID").
		Where("waits.ENGINE = ? AND waits.REQUESTING_THREAD_ID = ? AND waits.BLOCKING_THREAD_ID = ?", "INNODB", requesting, blocking).
		Where("requested.OBJECT_SCHEMA = ? AND requested.OBJECT_NAME = ? AND requested.LOCK_TYPE = ? AND requested.LOCK_STATUS = ?", schema, (model.Project{}).TableName(), "RECORD", "WAITING").
		Where("blocking.OBJECT_SCHEMA = ? AND blocking.OBJECT_NAME = ? AND blocking.LOCK_TYPE = ? AND blocking.LOCK_STATUS = ?", schema, (model.Project{}).TableName(), "RECORD", "GRANTED").
		Where("requested.LOCK_MODE LIKE ? AND blocking.LOCK_MODE LIKE ?", "X%", "X%").
		Where("requested.INDEX_NAME = blocking.INDEX_NAME AND requested.LOCK_DATA = blocking.LOCK_DATA").
		Where("(blocking.INDEX_NAME = ? AND blocking.LOCK_DATA = ?) OR (blocking.INDEX_NAME = ? AND blocking.LOCK_DATA IN ?)", "PRIMARY", primary, "idx_project_project_id", businessKeys).Limit(1)
}

func TestProjectAdmissionPollingLockWaitProbeIsScopedAndReadOnly(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN: "fixture@tcp(127.0.0.1:1)/arcentra_project_admission_test_shape", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var project model.Project
	parent := db.Table("project").Clauses(clause.Locking{Strength: "UPDATE"}).Select("project_id").Where("project_id = ?", "target-project").Find(&project)
	require.True(t, projectAdmissionPollingProjectLock(parent, "target-project"))
	require.False(t, projectAdmissionPollingProjectLock(parent, "foreign-project"))
	schema := "arcentra_project_admission_test_bound' OR 1=1 --"
	var edges []struct{ RequestingLockID string }
	query := projectAdmissionPollingLockWaitProbe(ctx, parent, schema, 202, 101, 42, "target-project").Find(&edges)
	require.NoError(t, query.Error)
	statement := query.Statement.SQL.String()
	for _, required := range []string{
		"SELECT waits.REQUESTING_ENGINE_LOCK_ID AS requesting_lock_id FROM performance_schema.data_lock_waits AS waits",
		"requested.ENGINE = waits.ENGINE", "blocking.ENGINE = waits.ENGINE",
		"requested.ENGINE_LOCK_ID = waits.REQUESTING_ENGINE_LOCK_ID", "blocking.ENGINE_LOCK_ID = waits.BLOCKING_ENGINE_LOCK_ID",
		"requested.THREAD_ID = waits.REQUESTING_THREAD_ID", "blocking.THREAD_ID = waits.BLOCKING_THREAD_ID",
		"waits.ENGINE = ? AND waits.REQUESTING_THREAD_ID = ? AND waits.BLOCKING_THREAD_ID = ?",
		"requested.OBJECT_SCHEMA = ? AND requested.OBJECT_NAME = ? AND requested.LOCK_TYPE = ? AND requested.LOCK_STATUS = ?",
		"blocking.OBJECT_SCHEMA = ? AND blocking.OBJECT_NAME = ? AND blocking.LOCK_TYPE = ? AND blocking.LOCK_STATUS = ?",
		"requested.LOCK_MODE LIKE ? AND blocking.LOCK_MODE LIKE ?",
		"requested.INDEX_NAME = blocking.INDEX_NAME AND requested.LOCK_DATA = blocking.LOCK_DATA",
		"AND ((blocking.INDEX_NAME = ? AND blocking.LOCK_DATA = ?) OR (blocking.INDEX_NAME = ? AND blocking.LOCK_DATA IN (?,?,?)))",
		"LIMIT ?",
	} {
		require.Contains(t, statement, required)
	}
	require.True(t, strings.HasPrefix(statement, "SELECT "))
	for _, excluded := range []string{schema, "target-project", "project_id", "FOR UPDATE"} {
		require.NotContains(t, statement, excluded, "metadata probe must not inherit the parent locking statement or interpolate identities")
	}
	require.Equal(t, ctx, query.Statement.Context)
	require.Equal(t, parent.Statement.ConnPool, query.Statement.ConnPool)
	require.Equal(t, []any{"INNODB", uint64(202), uint64(101), schema, "project", "RECORD", "WAITING", schema, "project", "RECORD", "GRANTED", "X%", "X%", "PRIMARY", "42", "idx_project_project_id", "target-project, 42", "'target-project', 42", `"target-project", 42`, 1}, query.Statement.Vars)
}

// Hold the winner's first shared current-read lock until the server proves that
// the other connection is waiting on that exact project record. Reaching a Go
// callback or waiting a fixed duration alone is never sufficient evidence.
func projectAdmissionPollingMySQLOrderedRace(t *testing.T, f *pollingMySQLFixture, schema string, first, second func(context.Context, *gorm.DB) error) []error {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	firstPool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	secondPool, err := f.other.DB()
	if err != nil || firstPool == secondPool {
		t.Fatal("polling/archive race requires independently opened database connections", err)
	}
	var target model.Project
	if err := f.db.WithContext(ctx).Select("id", "project_id").Where("project_id = ?", f.projectID).Take(&target).Error; err != nil || target.ID == 0 || target.ProjectID != f.projectID {
		t.Fatal("cannot identify exact polling/archive project row", err)
	}
	locked, competing := make(chan stepAttemptMySQLLockOwner, 1), make(chan stepAttemptMySQLLockOwner, 1)
	release := make(chan struct{})
	var lockOnce, competingOnce, releaseOnce sync.Once
	var workers sync.WaitGroup
	firstName, secondName := "project-admission:polling:first:"+f.projectID, "project-admission:polling:second:"+f.projectID
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		workers.Wait()
		if err := f.db.Callback().Query().Remove(firstName); err != nil {
			t.Error(err)
		}
		if err := f.other.Callback().Query().Remove(secondName); err != nil {
			t.Error(err)
		}
	}()
	if err := f.db.Callback().Query().After("gorm:query").Register(firstName, func(tx *gorm.DB) {
		if tx.Error != nil || !projectAdmissionPollingProjectLock(tx, f.projectID) {
			return
		}
		lockOnce.Do(func() {
			owner := stepAttemptMySQLLockOwner{}
			selected := ""
			switch rows := tx.Statement.Dest.(type) {
			case *pollingProjectConfiguration:
				selected = rows.ProjectID
			case *[]model.Project:
				if len(*rows) == 1 {
					selected = (*rows)[0].ProjectID
				}
			}
			if tx.RowsAffected != 1 || selected != target.ProjectID {
				owner.err = fmt.Errorf("first current read did not lock the exact polling/archive project")
			} else {
				owner.threadID, owner.err = stepAttemptMySQLConnectionThread(ctx, tx)
			}
			locked <- owner
			if owner.err != nil {
				_ = tx.AddError(owner.err)
				return
			}
			select {
			case <-release:
			case <-ctx.Done():
				_ = tx.AddError(ctx.Err())
			}
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.other.Callback().Query().Before("gorm:query").Register(secondName, func(tx *gorm.DB) {
		if projectAdmissionPollingProjectLock(tx, f.projectID) {
			competingOnce.Do(func() {
				thread, err := stepAttemptMySQLConnectionThread(ctx, tx)
				if err != nil {
					_ = tx.AddError(err)
				}
				competing <- stepAttemptMySQLLockOwner{threadID: thread, err: err}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	workers.Add(1)
	safe.Go(func() {
		defer workers.Done()
		errs[0] = first(ctx, f.db)
	})
	owner := awaitPollingMySQL(t, ctx, locked)
	if owner.err != nil || owner.threadID == 0 {
		t.Fatal("cannot inspect polling/archive lock owner", owner.err)
	}
	workers.Add(1)
	safe.Go(func() {
		defer workers.Done()
		errs[1] = second(ctx, f.other)
	})
	contender := awaitPollingMySQL(t, ctx, competing)
	if contender.err != nil || contender.threadID == 0 || contender.threadID == owner.threadID {
		t.Fatal("polling/archive contender lacks a distinct MySQL connection", contender.err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var edges []struct{ RequestingLockID string }
		if err := projectAdmissionPollingLockWaitProbe(ctx, f.db, schema, contender.threadID, owner.threadID, target.ID, target.ProjectID).Find(&edges).Error; err != nil {
			t.Fatal("cannot inspect real polling/archive project lock wait", err)
		}
		if len(edges) == 1 && edges[0].RequestingLockID != "" {
			t.Logf("observed MySQL waiter thread %d blocked by thread %d on %s.project row %d (%s)", contender.threadID, owner.threadID, schema, target.ID, target.ProjectID)
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("server never reported the contender waiting on the exact held project record")
		case <-ticker.C:
		}
	}
	releaseOnce.Do(func() { close(release) })
	workers.Wait()
	return errs
}
