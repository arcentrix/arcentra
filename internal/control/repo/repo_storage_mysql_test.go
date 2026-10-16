// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/configsecret"
	"github.com/arcentrix/arcentra/pkg/safe"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func storageMySQLConfig(dsn string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid storage MySQL acceptance DSN")
	}
	if !strings.HasPrefix(cfg.DBName, "arcentra_storage_test_") || len(cfg.DBName) > 64 || strings.Trim(cfg.DBName, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return nil, errors.New("storage acceptance requires a dedicated arcentra_storage_test_ schema")
	}
	switch cfg.Net {
	case "unix":
		if !filepath.IsAbs(cfg.Addr) {
			return nil, errors.New("storage acceptance requires an absolute local socket path")
		}
	case "tcp":
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
			return nil, errors.New("storage acceptance only permits explicit loopback connections")
		}
	default:
		return nil, errors.New("storage acceptance only permits loopback TCP or local unix sockets")
	}
	cfg.ParseTime, cfg.Loc, cfg.MultiStatements, cfg.ClientFoundRows = true, time.UTC, false, false
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 20*time.Second, 20*time.Second
	return cfg, nil
}

func TestStorageMySQLDSNGuards(t *testing.T) {
	for _, dsn := range []string{"test@tcp(127.0.0.1:3306)/production", "test@tcp(database.example:3306)/arcentra_storage_test_safe", "test@unix(relative.sock)/arcentra_storage_test_safe", "test@tcp(127.0.0.1:3306)/arcentra_storage_test_bad-name"} {
		_, err := storageMySQLConfig(dsn)
		require.Error(t, err)
	}
	for _, dsn := range []string{"test@tcp(127.0.0.1:3306)/arcentra_storage_test_safe", "test@tcp([::1]:3306)/arcentra_storage_test_safe", "test@unix(/tmp/storage-test.sock)/arcentra_storage_test_safe"} {
		_, err := storageMySQLConfig(dsn)
		require.NoError(t, err)
	}
}

func openStorageMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	required := os.Getenv("ARCENTRA_REQUIRE_STORAGE_MYSQL")
	require.True(t, required == "" || required == "1", "ARCENTRA_REQUIRE_STORAGE_MYSQL must be 1 or unset")
	dsn := os.Getenv("ARCENTRA_STORAGE_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required storage MySQL acceptance DSN is missing")
		}
		t.Skip("explicit local disposable ARCENTRA_STORAGE_MYSQL_DSN absent; no real MySQL acceptance claim")
	}
	cfg, err := storageMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("cannot connect to the explicitly supplied disposable storage MySQL schema")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(6)
	pool.SetMaxIdleConns(6)
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tables, err := db.WithContext(ctx).Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "refusing nonempty or previously used storage fixture schema")
	require.NoError(t, db.WithContext(ctx).AutoMigrate(&model.StorageConfig{}))
	var engine string
	require.NoError(t, db.WithContext(ctx).Table("information_schema.TABLES").Select("ENGINE").Where("TABLE_SCHEMA = ? AND TABLE_NAME = ?", cfg.DBName, "storage_config").Scan(&engine).Error)
	require.Equal(t, "InnoDB", engine)
	return db
}

func storageRace(ctx context.Context, operations ...func() error) []error {
	start := make(chan struct{})
	results := make([]error, len(operations))
	var wg sync.WaitGroup
	for i, operation := range operations {
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[i] = ctx.Err()
				return
			case <-start:
			}
			results[i] = operation()
		})
	}
	close(start)
	wg.Wait()
	return results
}

// 首次创建强制双方先读到空表，确实覆盖 InnoDB gap-lock 死锁与重试路径
func synchronizeEmptyStorageReads(t *testing.T, db *gorm.DB, ctx context.Context) {
	t.Helper()
	var reads atomic.Int32
	both := make(chan struct{})
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("storage:empty_read_barrier", func(tx *gorm.DB) {
		if tx.Statement.Table != "storage_config" || tx.Error != nil || tx.RowsAffected != 0 {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		if reads.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("storage:empty_read_barrier") })
}

// TestStorageMySQLAcceptance 使用真实 InnoDB 验证跨连接默认切换和事务内凭据合并
func TestStorageMySQLAcceptance(t *testing.T) {
	db := openStorageMySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first := NewStorageRepo(artifactTestDatabase{db}, nil)
	second := NewStorageRepo(artifactTestDatabase{db}, nil)
	t.Run("concurrent_first_duplicate_creation", func(t *testing.T) {
		synchronizeEmptyStorageReads(t, db, ctx)
		results := storageRace(ctx,
			func() error { return first.Create(ctx, storageTestConfig("empty-duplicate", 1)) },
			func() error { return second.Create(ctx, storageTestConfig("empty-duplicate", 1)) })
		successes, conflicts := 0, 0
		for _, err := range results {
			if err == nil {
				successes++
			} else if errors.Is(err, ErrStorageConfigConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected empty duplicate-create failure: %v", err)
			}
		}
		require.Equal(t, 1, successes)
		require.Equal(t, 1, conflicts)
		assertStorageDefault(t, db, "empty-duplicate")
		require.NoError(t, first.Delete(ctx, "empty-duplicate"))
	})
	t.Run("concurrent_first_default_creation", func(t *testing.T) {
		synchronizeEmptyStorageReads(t, db, ctx)
		results := storageRace(ctx,
			func() error { return first.Create(ctx, storageTestConfig("first", 1)) },
			func() error { return second.Create(ctx, storageTestConfig("second", 1)) })
		for _, err := range results {
			require.NoError(t, err)
		}
		rows, err := first.ListEnabled(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		current, err := first.GetDefault(ctx)
		require.NoError(t, err)
		assertStorageDefault(t, db, current.StorageID)
	})
	t.Run("concurrent_duplicate_creation", func(t *testing.T) {
		results := storageRace(ctx,
			func() error { return first.Create(ctx, storageTestConfig("duplicate", 1)) },
			func() error { return second.Create(ctx, storageTestConfig("duplicate", 1)) })
		successes, conflicts := 0, 0
		for _, err := range results {
			if err == nil {
				successes++
			} else if errors.Is(err, ErrStorageConfigConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected duplicate-create failure: %v", err)
			}
		}
		require.Equal(t, 1, successes)
		require.Equal(t, 1, conflicts)
		var count int64
		require.NoError(t, db.WithContext(ctx).Model(&model.StorageConfig{}).Where("storage_id = ?", "duplicate").Count(&count).Error)
		require.Equal(t, int64(1), count)
		assertStorageDefault(t, db, "duplicate")
		var caseInsensitive int64
		require.NoError(t, db.WithContext(ctx).Model(&model.StorageConfig{}).Where("storage_id = ?", "DUPLICATE").Count(&caseInsensitive).Error)
		if caseInsensitive > 0 {
			require.ErrorIs(t, first.Create(ctx, storageTestConfig("DUPLICATE", 1)), ErrStorageConfigConflict)
		}
	})
	t.Run("concurrent_existing_default_switches", func(t *testing.T) {
		for i := 0; i < 12; i++ {
			results := storageRace(ctx, func() error { return first.SetDefault(ctx, "first") }, func() error { return second.SetDefault(ctx, "second") })
			for _, err := range results {
				require.NoError(t, err)
			}
			current, err := first.GetDefault(ctx)
			require.NoError(t, err)
			assertStorageDefault(t, db, current.StorageID)
		}
	})
	t.Run("invalid_target_preserves_default", func(t *testing.T) {
		require.NoError(t, first.SetDefault(ctx, "first"))
		disabled := storageTestConfig("disabled", 0)
		disabled.IsEnabled = 0
		require.NoError(t, first.Create(ctx, disabled))
		for _, id := range []string{"missing", "disabled"} {
			require.ErrorIs(t, first.SetDefault(ctx, id), ErrStorageConfigNotFound)
			assertStorageDefault(t, db, "first")
		}
		// Repeating a switch also succeeds with the driver's changed-row semantics.
		require.NoError(t, first.SetDefault(ctx, "first"))
	})
	t.Run("failed_target_update_rolls_back", func(t *testing.T) {
		injected := errors.New("synthetic target write failure")
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register("storage:mysql_fail_target", func(tx *gorm.DB) {
			values, ok := tx.Statement.Dest.(map[string]any)
			if tx.Statement.Table == "storage_config" && ok && values["is_default"] == 1 {
				tx.AddError(injected)
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Update().Remove("storage:mysql_fail_target") })
		require.ErrorIs(t, first.SetDefault(ctx, "second"), injected)
		assertStorageDefault(t, db, "first")
	})
	t.Run("concurrent_credential_rotation_and_metadata_edit", func(t *testing.T) {
		raceCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		schemaConfig, err := storageMySQLConfig(os.Getenv("ARCENTRA_STORAGE_MYSQL_DSN"))
		require.NoError(t, err)
		locked, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		releaseLock := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseLock()
		ownerThread, contenderThread := make(chan uint64, 1), make(chan uint64, 1)
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("storage:mysql_contender", func(tx *gorm.DB) {
			if tx.Statement.Table != "storage_config" || tx.Statement.Context.Value(storageMySQLOrderKey{}) != "metadata" {
				return
			}
			if _, locked := tx.Statement.Clauses["FOR"]; !locked {
				return
			}
			thread, err := stepAttemptMySQLConnectionThread(raceCtx, tx)
			if err != nil {
				tx.AddError(err)
				return
			}
			contenderThread <- thread
		}))
		require.NoError(t, db.Callback().Query().After("gorm:query").Register("storage:mysql_owner", func(tx *gorm.DB) {
			if tx.Statement.Table != "storage_config" || tx.Error != nil || tx.Statement.Context.Value(storageMySQLOrderKey{}) != "rotation" {
				return
			}
			if _, locked := tx.Statement.Clauses["FOR"]; !locked {
				return
			}
			thread, err := stepAttemptMySQLConnectionThread(raceCtx, tx)
			if err != nil {
				tx.AddError(err)
				return
			}
			ownerThread <- thread
		}))
		var workers sync.WaitGroup
		t.Cleanup(func() {
			releaseLock()
			cancel()
			workers.Wait()
			_ = db.Callback().Query().Remove("storage:mysql_contender")
			_ = db.Callback().Query().Remove("storage:mysql_owner")
		})
		rotated := make(chan error, 1)
		workers.Add(1)
		safe.Go(func() {
			defer workers.Done()
			_, err := first.UpdateConfig(context.WithValue(raceCtx, storageMySQLOrderKey{}, "rotation"), "first", func(current *model.StorageConfig) error {
				close(locked)
				select {
				case <-release:
				case <-raceCtx.Done():
					return raceCtx.Err()
				}
				merged, _, err := configsecret.Merge(current.Config, []byte(`{"secretKey":"rotated-synthetic-secret"}`), false)
				current.Config = merged
				return err
			})
			rotated <- err
		})
		select {
		case <-locked:
		case <-raceCtx.Done():
			t.Fatal(raceCtx.Err())
		}
		var owner uint64
		select {
		case owner = <-ownerThread:
		case <-raceCtx.Done():
			t.Fatal(raceCtx.Err())
		}
		edited := make(chan error, 1)
		workers.Add(1)
		safe.Go(func() {
			defer workers.Done()
			_, err := second.UpdateConfig(context.WithValue(raceCtx, storageMySQLOrderKey{}, "metadata"), "first", func(current *model.StorageConfig) error {
				merged, _, err := configsecret.Merge(current.Config, []byte(`{"secretKey":"","accessKey":null}`), false)
				current.Config, current.Description = merged, ""
				return err
			})
			edited <- err
		})
		var contender uint64
		select {
		case contender = <-contenderThread:
		case <-raceCtx.Done():
			t.Fatal(raceCtx.Err())
		}
		require.NotEqual(t, owner, contender)
		// Do not release the rotation until MySQL itself proves the second connection
		// is waiting on this transaction's storage row lock.
		for {
			var waits []struct{ RequestingLockID string }
			query := db.WithContext(raceCtx).Table("performance_schema.data_lock_waits AS waits").
				Select("waits.REQUESTING_ENGINE_LOCK_ID AS requesting_lock_id").
				Joins("JOIN performance_schema.data_locks AS requested ON requested.ENGINE = waits.ENGINE AND requested.ENGINE_LOCK_ID = waits.REQUESTING_ENGINE_LOCK_ID AND requested.THREAD_ID = waits.REQUESTING_THREAD_ID").
				Joins("JOIN performance_schema.data_locks AS blocking ON blocking.ENGINE = waits.ENGINE AND blocking.ENGINE_LOCK_ID = waits.BLOCKING_ENGINE_LOCK_ID AND blocking.THREAD_ID = waits.BLOCKING_THREAD_ID").
				Where("waits.ENGINE = ? AND waits.REQUESTING_THREAD_ID = ? AND waits.BLOCKING_THREAD_ID = ?", "INNODB", contender, owner).
				Where("requested.OBJECT_SCHEMA = ? AND requested.OBJECT_NAME = ? AND requested.LOCK_TYPE = ? AND requested.LOCK_STATUS = ?", schemaConfig.DBName, "storage_config", "RECORD", "WAITING").
				Where("blocking.OBJECT_SCHEMA = ? AND blocking.OBJECT_NAME = ? AND blocking.LOCK_TYPE = ? AND blocking.LOCK_STATUS = ?", schemaConfig.DBName, "storage_config", "RECORD", "GRANTED").
				Where("requested.INDEX_NAME = ? AND blocking.INDEX_NAME = ? AND requested.LOCK_DATA = blocking.LOCK_DATA", "PRIMARY", "PRIMARY").
				Where("requested.LOCK_MODE LIKE ? AND blocking.LOCK_MODE LIKE ?", "X%", "X%").Limit(1)
			require.NoError(t, query.Find(&waits).Error)
			if len(waits) == 1 {
				break
			}
			select {
			case <-raceCtx.Done():
				t.Fatal("did not observe real storage row-lock contention")
			case <-time.After(5 * time.Millisecond):
			}
		}
		releaseLock()
		require.NoError(t, <-rotated)
		require.NoError(t, <-edited)
		result, err := first.Get(ctx, "first")
		require.NoError(t, err)
		require.Contains(t, string(result.Config), "rotated-synthetic-secret")
		require.Empty(t, result.Description)
		assertStorageDefault(t, db, "first")
	})
}

type storageMySQLOrderKey struct{}
