// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func copilotHistoryMySQLConfig(dsn string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid history test connection")
	}
	const prefix = "arcentra_copilot_history_test"
	if cfg.DBName != prefix && !strings.HasPrefix(cfg.DBName, prefix+"_") || len(cfg.DBName) > 64 || strings.Trim(cfg.DBName, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return nil, errors.New("dedicated history test schema required")
	}
	switch cfg.Net {
	case "unix":
		if !filepath.IsAbs(cfg.Addr) {
			return nil, errors.New("absolute local socket required")
		}
	case "tcp":
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return nil, errors.New("explicit loopback required")
		}
	default:
		return nil, errors.New("local MySQL required")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.WriteTimeout = 15 * time.Second
	return cfg, nil
}
func TestCopilotHistoryMySQLDSNGuards(t *testing.T) {
	for _, dsn := range []string{"test@tcp(127.0.0.1:3306)/production", "test@tcp(remote.invalid:3306)/arcentra_copilot_history_test", "test@unix(relative.sock)/arcentra_copilot_history_test", "test@tcp(127.0.0.1:3306)/arcentra_copilot_history_testunsafe"} {
		_, err := copilotHistoryMySQLConfig(dsn)
		require.Error(t, err)
	}
	for _, dsn := range []string{"test@tcp(127.0.0.1:3306)/arcentra_copilot_history_test", "test@unix(/tmp/mysql.sock)/arcentra_copilot_history_test_ci"} {
		_, err := copilotHistoryMySQLConfig(dsn)
		require.NoError(t, err)
	}
}
func TestCopilotHistoryMySQLRealRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_HISTORY_MYSQL")
	require.True(t, required == "" || required == "1", "required flag must be absent or 1")
	dsn := os.Getenv("ARCENTRA_COPILOT_HISTORY_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required Copilot history MySQL fixture missing")
		}
		t.Skip("disposable real MySQL fixture absent; no MySQL acceptance claim")
	}
	cfg, err := copilotHistoryMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("explicit Copilot history MySQL connection unavailable")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(12)
	t.Cleanup(func() { _ = pool.Close() })
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "migration gate requires fresh dedicated empty schema")
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			scoped := db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
			ctx := context.Background()
			// Cleanup touches only this freshly verified, dedicated disposable schema.
			defer func() {
				for _, m := range schemaModels() {
					require.NoError(t, db.Migrator().DropTable(m))
				}
			}()
			require.NoError(t, scoped.AutoMigrate(&inboxLegacyRun{}, &inboxLegacyApproval{}))
			require.NoError(t, db.Create(&inboxLegacyRun{RunID: "legacy", PipelineID: "legacy-pipe", ProjectID: "legacy-project", Env: `{"TOKEN":"never-backfill"}`}).Error)
			require.NoError(t, MigrateSchema(ctx, scoped))
			require.NoError(t, MigrateSchema(ctx, scoped))
			var old model.PipelineRun
			require.NoError(t, db.Where("run_id = ?", "legacy").First(&old).Error)
			require.Contains(t, old.Env, "never-backfill")
			for _, m := range []any{&model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}} {
				var n int64
				require.NoError(t, db.Model(m).Count(&n).Error)
				require.Zero(t, n)
			}
			historyConcurrencyTest(t, db)
			historyLostAcknowledgementTest(t, db)
			r := &Repositories{database: statusTestDB{db}}
			c := historyConversation(t, r, historySeed(t, db, "mysql-actor", "mysql-project"), "mysql-conversation")
			for _, scope := range [][3]string{{"MYSQL-ACTOR", "mysql-project", c.ConversationID}, {"mysql-actor", "MYSQL-PROJECT", c.ConversationID}, {"mysql-actor", "mysql-project", "MYSQL-CONVERSATION"}} {
				_, err = r.CopilotHistoryGet(ctx, scope[0], scope[1], scope[2], historyTestAllow)
				require.Error(t, err)
			}
			v := historyCompleted(t, r, c, "mysql-complete")
			// Simulate a caller losing the completion acknowledgement: discard it and
			// obtain durable status from an independent primary repository after restart.
			restart := &Repositories{database: statusTestDB{db.Session(&gorm.Session{NewDB: true})}}
			got, err := restart.CopilotHistoryGetRequest(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, v.RequestID, historyTestAllow)
			require.NoError(t, err)
			require.Equal(t, "completed", got.State)
			require.Equal(t, v.PayloadCiphertext, got.PayloadCiphertext)
			_, fresh, err := restart.CopilotHistoryReserve(ctx, historyReservation(c, "mysql-complete"), historyTestAllow)
			require.NoError(t, err)
			require.False(t, fresh)
			// All plans remain inert, and competing CAS review/cancel has one winner.
			p, _, err := r.CopilotHistoryCreatePlan(ctx, CopilotHistoryPlanInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, PlanID: "mysql-plan", RequestID: "mysql-plan-request", Kind: "new_run_review", PlanDigest: "mysql-plan-digest", ConversationVersion: c.Version, TurnVersion: v.Version, SchemaVersion: 1, CipherVersion: 1, PayloadCiphertext: []byte("opaque-plan")}, historyTestAllow)
			require.NoError(t, err)
			start := make(chan struct{})
			errs := make(chan error, 2)
			var wg sync.WaitGroup
			for _, state := range []string{"reviewed", "cancelled"} {
				state := state
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, e := r.CopilotHistoryTransitionPlan(ctx, CopilotHistoryPlanTransitionInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, PlanID: p.PlanID, PlanDigest: p.PlanDigest, Version: p.Version, State: state}, historyTestAllow)
					errs <- e
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			success := 0
			for e := range errs {
				if e == nil {
					success++
				} else {
					require.ErrorIs(t, e, ErrCopilotHistoryConflict)
				}
			}
			require.Equal(t, 1, success)
			active, _, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "mysql-cancel-race"), historyTestAllow)
			require.NoError(t, err)
			active, err = r.CopilotHistoryDispatch(ctx, CopilotHistoryDispatchInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: active.TurnID, Version: active.Version, PayloadCiphertext: []byte("opaque"), CipherVersion: 1}, historyTestAllow)
			require.NoError(t, err)
			start = make(chan struct{})
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, _ = r.CopilotHistoryCancel(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, active.RequestID, historyTestAllow)
			}()
			go func() {
				defer wg.Done()
				<-start
				_, _ = r.CopilotHistoryFinalize(ctx, CopilotHistoryFinalizeInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: active.TurnID, Version: active.Version, State: "completed", PayloadCiphertext: []byte("opaque-raced-answer"), CipherVersion: 1}, historyTestAllow)
			}()
			close(start)
			wg.Wait()
			raced, err := r.CopilotHistoryGetRequest(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, active.RequestID, historyTestAllow)
			require.NoError(t, err)
			require.Contains(t, []string{"completed", "cancelled"}, raced.State)
			if raced.State == "cancelled" {
				require.NotEqual(t, []byte("opaque-raced-answer"), raced.PayloadCiphertext)
			}
			require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id = ?", v.ID).UpdateColumn("expires_at", time.Now().Add(-time.Minute)).Error)
			_, err = r.CopilotHistoryGetTurn(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, v.TurnID, historyTestAllow)
			require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
			n, err := r.CopilotHistoryPurge(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			var tombstone model.CopilotTurn
			require.NoError(t, db.Where("id = ?", v.ID).First(&tombstone).Error)
			require.Empty(t, tombstone.PayloadCiphertext)
			require.Equal(t, v.RequestID, tombstone.RequestID)
			// Unique constraints survive content removal under each actual collation.
			duplicate := tombstone
			duplicate.ID = 0
			duplicate.TurnID = "new-unique-id"
			duplicate.Sequence = 199
			require.Error(t, db.Create(&duplicate).Error)
			historyConversationCapacityTest(t, db)
			t.Logf("history retained owner-cap concurrent proof passed: %s", collation)
			// Mixed fixture exercises the exact scoped descending candidate query.
			now := time.Now().UTC()
			rows := make([]model.CopilotConversation, 0, 4000)
			for i := 0; i < 4000; i++ {
				owner, project, state := "plan-owner", "plan-project", "active"
				expiry := now.Add(time.Hour)
				if i%4 != 0 {
					owner = fmt.Sprintf("irrelevant-owner-%d", i%4)
				} else {
					switch (i / 4) % 10 {
					case 1:
						project = "other-project"
					case 2:
						state = "deleted"
					case 3:
						expiry = now.Add(-time.Hour)
					}
				}
				id := fmt.Sprintf("plan-fixture-%05d", i)
				rows = append(rows, model.CopilotConversation{ConversationID: id, OwnerUserID: owner, ProjectID: project, PipelineID: "plan-pipeline", RunID: "plan-run", CreateRequestID: id, CreateFingerprint: id, State: state, Version: 1, NextSequence: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiry, TombstoneUntil: now.Add(24 * time.Hour)})
			}
			require.NoError(t, db.CreateInBatches(rows, 100).Error)
			// Use the production candidate-page size and full row projection. Do not
			// force a preferred index or confuse an optimizer estimate with work.
			const listSQL = "SELECT * FROM copilot_conversation WHERE owner_user_id = ? AND project_id = ? AND id < ? AND state = 'active' AND expires_at > ? ORDER BY id DESC LIMIT 201"
			cursor := rows[3800].ID
			args := []any{"plan-owner", "plan-project", cursor, now}
			var expected []uint64
			for i := len(rows) - 1; i >= 0 && len(expected) < 201; i-- {
				row := rows[i]
				if row.ID < cursor && row.OwnerUserID == "plan-owner" && row.ProjectID == "plan-project" && row.State == "active" && row.ExpiresAt.After(now) {
					expected = append(expected, uint64(row.ID))
				}
			}
			require.Len(t, expected, 201)
			var planJSON, execution string
			require.NoError(t, db.Raw("EXPLAIN FORMAT=JSON "+listSQL, args...).Row().Scan(&planJSON))
			require.LessOrEqual(t, len(planJSON), 16384)
			t.Logf("synthetic history list plan: %s", planJSON)
			require.NoError(t, db.Raw("EXPLAIN ANALYZE FORMAT=TREE "+listSQL, args...).Row().Scan(&execution))
			require.LessOrEqual(t, len(execution), 8192)
			t.Logf("synthetic history list execution: %s", execution)
			require.NoError(t, validateHistoryMySQLListPlan(planJSON, execution))
			var selected []model.CopilotConversation
			require.NoError(t, db.Raw(listSQL, args...).Scan(&selected).Error)
			require.Len(t, selected, 201)
			for i, row := range selected {
				require.Equal(t, expected[i], uint64(row.ID))
				require.Less(t, row.ID, cursor)
				require.Equal(t, "plan-owner", row.OwnerUserID)
				require.Equal(t, "plan-project", row.ProjectID)
				require.Equal(t, "active", row.State)
				require.True(t, row.ExpiresAt.After(now))
			}
			t.Logf("history scoped list actual-work and exact 201-row content proof passed: %s", collation)
			// Current actor state denies reads even though conversation data still exists.
			require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", c.OwnerUserID).UpdateColumn("is_enabled", 0).Error)
			_, err = restart.CopilotHistoryGet(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, historyTestAllow)
			require.ErrorIs(t, err, ErrCopilotHistoryForbidden)
		})
	}
}
