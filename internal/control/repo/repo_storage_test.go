// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/bytedance/sonic"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func storageRepositoryFixture(t *testing.T) (*gorm.DB, IStorageRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "storage.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.WithContext(context.Background()).AutoMigrate(&model.StorageConfig{}))
	return db, NewStorageRepo(artifactTestDatabase{db}, nil)
}

func storageTestConfig(id string, isDefault int) *model.StorageConfig {
	return &model.StorageConfig{StorageID: id, Name: id, StorageType: "minio", Description: "original", IsEnabled: 1, IsDefault: isDefault,
		Config: datatypes.JSON(`{"endpoint":"127.0.0.1:9000","bucket":"synthetic","accessKey":"synthetic-access","secretKey":"synthetic-secret","region":"","basePath":"","useTLS":false}`)}
}

func assertStorageDefault(t *testing.T, db *gorm.DB, want string) {
	t.Helper()
	var rows []model.StorageConfig
	require.NoError(t, db.WithContext(context.Background()).Select("storage_id", "is_default", "is_enabled").Where("is_default = ?", 1).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, want, rows[0].StorageID)
	require.Equal(t, 1, rows[0].IsEnabled)
}

func TestStorageDefaultValidationAndAtomicUpdates(t *testing.T) {
	db, r := storageRepositoryFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, storageTestConfig("old", 1)))
	require.NoError(t, r.Create(ctx, storageTestConfig("next", 0)))
	disabled := storageTestConfig("disabled", 0)
	disabled.IsEnabled = 0
	require.NoError(t, r.Create(ctx, disabled))
	for _, id := range []string{"missing", "disabled"} {
		require.ErrorIs(t, r.SetDefault(ctx, id), ErrStorageConfigNotFound)
		assertStorageDefault(t, db, "old")
	}
	require.NoError(t, r.SetDefault(ctx, "next"))
	require.NoError(t, r.SetDefault(ctx, "next"))
	assertStorageDefault(t, db, "next")
	require.NoError(t, r.Create(ctx, storageTestConfig("created-default", 1)))
	assertStorageDefault(t, db, "created-default")
	require.ErrorIs(t, r.Create(ctx, storageTestConfig("old", 1)), ErrStorageConfigConflict)
	assertStorageDefault(t, db, "created-default")
	_, err := r.UpdateConfig(ctx, "next", func(current *model.StorageConfig) error { current.IsDefault = 1; return nil })
	require.NoError(t, err)
	assertStorageDefault(t, db, "next")
	_, err = r.UpdateConfig(ctx, "next", func(current *model.StorageConfig) error { current.IsEnabled = 0; return nil })
	require.ErrorIs(t, err, ErrStorageConfigFlags)
	assertStorageDefault(t, db, "next")
}

func TestStorageUpdatePersistsZeroValuesAndKeepsTargetIdentity(t *testing.T) {
	db, r := storageRepositoryFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, storageTestConfig("old", 1)))
	updated, err := r.UpdateConfig(ctx, "old", func(current *model.StorageConfig) error {
		current.Description, current.IsDefault, current.IsEnabled = "", 0, 0
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, updated.Description)
	require.Zero(t, updated.IsDefault)
	require.Zero(t, updated.IsEnabled)
	var stored model.StorageConfig
	require.NoError(t, db.WithContext(ctx).Select("storage_id", "description", "is_default", "is_enabled").Where("storage_id = ?", "old").Take(&stored).Error)
	require.Empty(t, stored.Description)
	require.Zero(t, stored.IsDefault)
	require.Zero(t, stored.IsEnabled)
	require.NoError(t, r.Create(ctx, storageTestConfig("active", 1)))
	_, err = r.UpdateConfig(ctx, "active", func(current *model.StorageConfig) error {
		current.Config = datatypes.JSON(`{"endpoint":"different","bucket":"synthetic"}`)
		return nil
	})
	require.ErrorIs(t, err, ErrStorageTargetImmutable)
	assertStorageDefault(t, db, "active")
}

func TestStorageDefaultFailureRollsBackPreviousDefault(t *testing.T) {
	db, r := storageRepositoryFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, storageTestConfig("old", 1)))
	require.NoError(t, r.Create(ctx, storageTestConfig("next", 0)))
	injected := errors.New("synthetic storage update failure")
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("storage:fail_target", func(tx *gorm.DB) {
		values, ok := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == "storage_config" && ok && values["is_default"] == 1 {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove("storage:fail_target") })
	require.ErrorIs(t, r.SetDefault(ctx, "next"), injected)
	assertStorageDefault(t, db, "old")
	_, err := r.UpdateConfig(ctx, "next", func(current *model.StorageConfig) error { current.IsDefault = 1; return nil })
	require.ErrorIs(t, err, injected)
	assertStorageDefault(t, db, "old")
}

type storageTestCache struct {
	cache.ICache
	mu     sync.Mutex
	values map[string]string
}

func (c *storageTestCache) Get(_ context.Context, key string) *redis.StringCmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value, ok := c.values[key]; ok {
		return redis.NewStringResult(value, nil)
	}
	return redis.NewStringResult("", redis.Nil)
}
func (c *storageTestCache) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = value.(string)
	return redis.NewStatusResult("OK", nil)
}
func (c *storageTestCache) Del(_ context.Context, keys ...string) *redis.IntCmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		delete(c.values, key)
	}
	return redis.NewIntResult(int64(len(keys)), nil)
}

func TestStorageMutationsInvalidatePreviousAndCurrentDefaultCaches(t *testing.T) {
	db, _ := storageRepositoryFixture(t)
	cache := &storageTestCache{values: map[string]string{}}
	r := NewStorageRepo(artifactTestDatabase{db}, cache)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, storageTestConfig("old", 1)))
	require.NoError(t, r.Create(ctx, storageTestConfig("next", 0)))
	_, err := r.Get(ctx, "old")
	require.NoError(t, err)
	_, err = r.Get(ctx, "next")
	require.NoError(t, err)
	_, err = r.GetDefault(ctx)
	require.NoError(t, err)
	require.NoError(t, r.SetDefault(ctx, "next"))
	old, err := r.Get(ctx, "old")
	require.NoError(t, err)
	require.Zero(t, old.IsDefault)
	current, err := r.GetDefault(ctx)
	require.NoError(t, err)
	require.Equal(t, "next", current.StorageID)
	_, err = r.UpdateConfig(ctx, "next", func(current *model.StorageConfig) error { current.Description = "changed"; return nil })
	require.NoError(t, err)
	current, err = r.GetDefault(ctx)
	require.NoError(t, err)
	require.Equal(t, "changed", current.Description)
	_, err = r.UpdateConfig(ctx, "next", func(current *model.StorageConfig) error { current.IsDefault = 0; return nil })
	require.NoError(t, err)
	_, err = r.GetDefault(ctx)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestStorageReadsIgnoreStaleCredentialAndDefaultCaches(t *testing.T) {
	db, r := storageRepositoryFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, storageTestConfig("current", 1)))
	stale := storageTestConfig("stale", 1)
	encoded, err := sonic.MarshalString(stale)
	require.NoError(t, err)
	cache := &storageTestCache{values: map[string]string{storageDefaultConfigCacheKey: encoded, storageConfigCacheKeyPrefix + "current": encoded}}
	cachedRepo := NewStorageRepo(artifactTestDatabase{db}, cache)
	current, err := cachedRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.Equal(t, "current", current.StorageID)
	current, err = cachedRepo.Get(ctx, "current")
	require.NoError(t, err)
	require.Equal(t, "current", current.StorageID)
}

type storageCommitFaultPool struct {
	*sql.DB
	commits *int
	err     error
}

func (p storageCommitFaultPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &storageCommitFaultTx{Tx: tx, commits: p.commits, err: p.err}, nil
}

type storageCommitFaultTx struct {
	*sql.Tx
	commits *int
	err     error
}

func (t storageCommitFaultTx) Commit() error {
	*t.commits++
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	return t.err
}

func TestStorageUnknownCommitResultIsNotRetriedAndReadsStayFresh(t *testing.T) {
	db, r := storageRepositoryFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, storageTestConfig("old", 1)))
	require.NoError(t, r.Create(ctx, storageTestConfig("next", 0)))
	pool, err := db.DB()
	require.NoError(t, err)
	commits := 0
	lostResponse := errors.New("synthetic commit response lost")
	faultDB := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	faultDB.Statement.ConnPool = storageCommitFaultPool{DB: pool, commits: &commits, err: lostResponse}
	faulty := NewStorageRepo(artifactTestDatabase{faultDB}, nil)
	require.ErrorIs(t, faulty.SetDefault(ctx, "next"), lostResponse)
	require.Equal(t, 1, commits, "unknown commit result must not replay a mutation")
	current, err := r.GetDefault(ctx)
	require.NoError(t, err)
	require.Equal(t, "next", current.StorageID, "live primary read must reflect a commit whose response was lost")
	assertStorageDefault(t, db, "next")
}

func TestStorageIDMatchesArtifactIdentityBoundaries(t *testing.T) {
	_, r := storageRepositoryFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, storageTestConfig(strings.Repeat("x", 64), 0)))
	for _, id := range []string{"", ".", "..", "with/slash", "with\\backslash", "with:colon", "with\nnewline", strings.Repeat("x", 65), strings.Repeat("界", 22)} {
		require.ErrorIs(t, r.Create(ctx, storageTestConfig(id, 0)), ErrStorageConfigID)
		require.ErrorIs(t, r.SetDefault(ctx, id), ErrStorageConfigID)
	}
}

func TestStorageTransactionRetryIsBoundedToDeadlockVictims(t *testing.T) {
	for _, test := range []struct {
		name     string
		number   uint16
		attempts int
	}{
		{"deadlock", 1213, storageMutationAttempts}, {"lock_timeout", 1205, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, r := storageRepositoryFixture(t)
			ctx := context.Background()
			require.NoError(t, r.Create(ctx, storageTestConfig("old", 1)))
			calls := 0
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("storage:retry_fault", func(tx *gorm.DB) {
				if tx.Statement.Table == "storage_config" {
					if _, locked := tx.Statement.Clauses["FOR"]; locked {
						calls++
						tx.AddError(&drivermysql.MySQLError{Number: test.number, Message: "synthetic transaction fault"})
					}
				}
			}))
			err := r.SetDefault(ctx, "old")
			require.Error(t, err)
			require.Equal(t, test.attempts, calls)
			require.NoError(t, db.Callback().Query().Remove("storage:retry_fault"))
			assertStorageDefault(t, db, "old")
		})
	}
}
