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
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func manualInboxMySQLConfig(dsn string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid inbox test DSN")
	}
	const prefix = "arcentra_inbox_test"
	if (cfg.DBName != prefix && !strings.HasPrefix(cfg.DBName, prefix+"_")) || len(cfg.DBName) > 64 || strings.Trim(cfg.DBName, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return nil, errors.New("dedicated inbox test schema required")
	}
	switch cfg.Net {
	case "unix":
		if !filepath.IsAbs(cfg.Addr) {
			return nil, errors.New("absolute local socket required")
		}
	case "tcp":
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return nil, errors.New("explicit loopback host required")
		}
	default:
		return nil, errors.New("local MySQL connection required")
	}
	cfg.ParseTime, cfg.Loc, cfg.MultiStatements = true, time.UTC, false
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 10*time.Second, 10*time.Second
	return cfg, nil
}

func TestManualInboxMySQLDSNGuards(t *testing.T) {
	for _, dsn := range []string{"test@tcp(127.0.0.1:3306)/production", "test@tcp(remote.invalid:3306)/arcentra_inbox_test", "test@unix(relative.sock)/arcentra_inbox_test", "test@tcp(127.0.0.1:3306)/arcentra_inbox_testunsafe"} {
		_, err := manualInboxMySQLConfig(dsn)
		require.Error(t, err)
	}
	for _, dsn := range []string{"test@tcp(127.0.0.1:3306)/arcentra_inbox_test", "test@unix(/tmp/mysql.sock)/arcentra_inbox_test_ci"} {
		_, err := manualInboxMySQLConfig(dsn)
		require.NoError(t, err)
	}
}

// Explicitly opted-in disposable local MySQL only. Neither credentials nor
// schemas are discovered/created, and missing opt-in makes no acceptance claim.
func TestManualInboxMySQLProvenanceBothCollations(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_INBOX_MYSQL")
	require.True(t, required == "" || required == "1", "ARCENTRA_REQUIRE_INBOX_MYSQL must be 1 or unset")
	dsn := os.Getenv("ARCENTRA_INBOX_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required inbox MySQL DSN missing")
		}
		t.Skip("explicit local disposable inbox MySQL fixture absent; no MySQL acceptance claim")
	}
	cfg, err := manualInboxMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("cannot open explicit local inbox MySQL fixture")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(5)
	t.Cleanup(func() { _ = pool.Close() })
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "fixture must be a fresh empty schema")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	models := []any{&model.Pipeline{}, &model.Project{}, &model.PipelineRun{}, &model.User{}, &model.InboxWork{}, &model.JobRun{}}
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			require.NoError(t, db.WithContext(ctx).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation).AutoMigrate(models...))
			defer func() {
				for i := len(models) - 1; i >= 0; i-- {
					require.NoError(t, db.Migrator().DropTable(models[i]))
				}
			}()
			for _, row := range []any{&model.Pipeline{PipelineID: "pipeline", ProjectID: "project", Metadata: "{}", IsEnabled: 1}, &model.Project{ProjectID: "project", IsEnabled: 1, Status: model.ProjectStatusActive, BuildConfig: datatypes.JSON("{}"), EnvVars: datatypes.JSON("{}"), Settings: datatypes.JSON("{}")}, &model.User{UserID: "actor", IsEnabled: 1}} {
				require.NoError(t, db.Create(row).Error)
			}
			r := &PipelineRepo{IDatabase: statusTestDB{db}}
			template := model.PipelineRun{RunID: "run", PipelineID: "pipeline", RequestID: "request", TriggeredBy: "actor", TriggerType: 1, Status: model.PipelineStatusPending, Env: "{}"}
			alias := template
			alias.TriggeredBy = "Actor"
			require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, "Actor", &alias, permitManualInbox))
			alias = template
			alias.PipelineID = "Pipeline"
			require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", &alias, permitManualInbox))
			const callers = 4
			var wg sync.WaitGroup
			out := make(chan error, callers)
			for i := 0; i < callers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					run := template
					out <- r.CreateVerifiedManualTriggerRun(ctx, "actor", &run, permitManualInbox)
				}()
			}
			wg.Wait()
			close(out)
			success := 0
			for err := range out {
				if err == nil {
					success++
				}
			}
			require.Equal(t, 1, success)
			stored, err := r.GetRunByRequestID(ctx, "pipeline", "request")
			require.NoError(t, err)
			require.Equal(t, "actor", stored.InboxActorID)
			_, err = r.GetRunByRequestID(ctx, "pipeline", "Request")
			require.ErrorIs(t, err, ErrManualTriggerConflict)
			for _, change := range []string{"run", "request"} {
				alias := template
				if change == "run" {
					alias.RunID, alias.RequestID = "Run", "another"
				} else {
					alias.RunID, alias.RequestID = "another", "Request"
				}
				require.ErrorIs(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", &alias, permitManualInbox), ErrManualTriggerConflict)
			}
			for _, row := range []any{&model.PipelineRun{}, &model.InboxWork{}} {
				var count int64
				require.NoError(t, db.Model(row).Count(&count).Error)
				require.EqualValues(t, 1, count)
			}
			other := template
			other.RunID = "second"
			other.RequestID = "second"
			require.NoError(t, db.Create(&model.User{UserID: "Actor", IsEnabled: 1}).Error)
			require.Error(t, r.CreateVerifiedManualTriggerRun(ctx, "actor", &other, permitManualInbox))
		})
	}
}
