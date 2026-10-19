package repo

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func copilotExecutionMySQLConfig(dsn string) (*drivermysql.Config, error) {
	c, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid execution fixture connection")
	}
	const prefix = "arcentra_copilot_execution_test"
	if c.DBName != prefix && !strings.HasPrefix(c.DBName, prefix+"_") || len(c.DBName) > 64 || strings.Trim(c.DBName, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return nil, fmt.Errorf("dedicated execution fixture schema required")
	}
	switch c.Net {
	case "unix":
		if !filepath.IsAbs(c.Addr) {
			return nil, fmt.Errorf("absolute local socket required")
		}
	case "tcp":
		h, _, e := net.SplitHostPort(c.Addr)
		if e != nil || (h != "localhost" && h != "127.0.0.1" && h != "::1") {
			return nil, fmt.Errorf("explicit loopback required")
		}
	default:
		return nil, fmt.Errorf("local MySQL required")
	}
	c.ParseTime = true
	c.Loc = time.UTC
	c.MultiStatements = false
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 15 * time.Second
	c.WriteTimeout = 15 * time.Second
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	c.Params["transaction_isolation"] = "'REPEATABLE-READ'"
	return c, nil
}
func TestCopilotExecutionMySQLDSNGuards(t *testing.T) {
	for _, dsn := range []string{"test@tcp(127.0.0.1:3306)/production", "test@tcp(remote.invalid:3306)/arcentra_copilot_execution_test", "test@unix(relative)/arcentra_copilot_execution_test", "test@tcp(127.0.0.1:3306)/arcentra_copilot_execution_testunsafe"} {
		_, err := copilotExecutionMySQLConfig(dsn)
		require.Error(t, err)
	}
	_, err := copilotExecutionMySQLConfig("test@tcp(127.0.0.1:3306)/arcentra_copilot_execution_test_ci")
	require.NoError(t, err)
}
func executionMySQLModels() []any {
	return []any{&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}, &model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}, &model.CopilotExecutionIntent{}}
}
func executionMySQLSeed(t *testing.T, db *gorm.DB) (*Repositories, CopilotExecutionPrepareInput) {
	t.Helper()
	source, _, in := executionFixture(t)
	for _, entity := range executionMySQLModels() {
		rows := reflect.New(reflect.SliceOf(reflect.TypeOf(entity).Elem())).Interface()
		require.NoError(t, source.db.Select(retryApprovalTestColumns(source.db, entity)).Find(rows).Error)
		for i := 0; i < reflect.ValueOf(rows).Elem().Len(); i++ {
			row := reflect.ValueOf(rows).Elem().Index(i).Addr().Interface()
			retryApprovalMySQLJSONDefaults(row)
			require.NoError(t, retryApprovalMySQLValidateJSON(db, row))
		}
		if reflect.ValueOf(rows).Elem().Len() > 0 {
			require.NoError(t, db.Create(rows).Error)
		}
	}
	r := &Repositories{database: statusTestDB{db}}
	authorize, err := retryApprovalAuthorizer(source.db)
	require.NoError(t, err)
	require.NoError(t, SetRetryApprovalAuthorizer(r, authorize))
	t.Cleanup(func() { _ = SetRetryApprovalAuthorizer(r, nil) })
	return r, in
}

// An observed server-side lock wait is required; sleeping alone is not evidence.
func executionMySQLWait(t *testing.T, db *gorm.DB, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("operation completed before observed MySQL lock wait: %v", err)
		default:
		}
		var n int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.data_locks l ON l.ENGINE_LOCK_ID = w.BLOCKING_ENGINE_LOCK_ID AND l.ENGINE = w.ENGINE WHERE l.OBJECT_SCHEMA = DATABASE()`).Scan(&n).Error)
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no real InnoDB wait observed")
}
func executionMySQLNoVote(t *testing.T, db *gorm.DB) {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&n).Error)
	require.Zero(t, n)
}
func TestCopilotExecutionMySQLRealRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_EXECUTION_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_EXECUTION_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required Copilot execution MySQL fixture missing")
		}
		t.Skip("real disposable MySQL absent; no MySQL acceptance claim")
	}
	cfg, err := copilotExecutionMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(12)
	defer pool.Close()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "fresh dedicated empty schema required")
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			scoped := db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
			require.NoError(t, scoped.AutoMigrate(executionMySQLModels()...))
			defer func() {
				models := executionMySQLModels()
				for i := len(models) - 1; i >= 0; i-- {
					require.NoError(t, db.Migrator().DropTable(models[i]))
				}
			}()
			for _, scenario := range []string{"cancel-wins", "confirm-wins", "duplicate-ack", "revocation-wait", "expiry-wait", "ordinary-vote", "plan-cancel-wait", "detached-history", "current-read-denial", "sparse-list", "scope-lock-deadlock", "history-reserve-observe-deadlock", "history-review-observe-deadlock", "username-login-identifiers"} {
				t.Run(scenario, func(t *testing.T) {
					models := executionMySQLModels()
					defer func() {
						for i := len(models) - 1; i >= 0; i-- {
							require.NoError(t, db.Where("1=1").Delete(models[i]).Error)
						}
					}()
					r, in := executionMySQLSeed(t, db)
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if scenario == "plan-cancel-wait" {
						holder := db.Begin()
						require.NoError(t, holder.Error)
						defer holder.Rollback()
						require.NoError(t, holder.Model(&model.CopilotActionPlan{}).Where("plan_id = ?", in.PlanID).Update("state", "cancelled").Error)
						done := make(chan error, 1)
						go func() { _, _, e := r.CopilotExecutionPrepare(ctx, in, historyTestAllow); done <- e }()
						executionMySQLWait(t, db, done)
						require.NoError(t, holder.Commit().Error)
						require.Error(t, awaitRetryMySQL(t, done))
						executionMySQLNoVote(t, db)
						return
					}
					if scenario == "history-reserve-observe-deadlock" || scenario == "history-review-observe-deadlock" {
						historyObserveMySQLDeadlock(t, db, r, in, scenario == "history-review-observe-deadlock")
						return
					}
					v := executionPrepare(t, r, in)
					mutation := executionMutationInput(v)
					switch scenario {
					case "cancel-wins", "confirm-wins", "duplicate-ack":
						entered, release := make(chan struct{}), make(chan struct{})
						blocking := func(c context.Context, rr *Repositories, o string, p *model.Project, pipe string) error {
							close(entered)
							select {
							case <-release:
							case <-c.Done():
								return c.Err()
							}
							return historyTestAllow(c, rr, o, p, pipe)
						}
						first := make(chan error, 1)
						go func() {
							var e error
							if scenario == "cancel-wins" {
								_, e = r.CopilotExecutionCancel(ctx, mutation, blocking)
							} else {
								_, e = r.CopilotExecutionConfirm(ctx, mutation, blocking)
							}
							first <- e
						}()
						select {
						case <-entered:
						case <-ctx.Done():
							t.Fatal("first operation did not reach authorization boundary")
						}
						second := make(chan error, 1)
						go func() {
							var e error
							if scenario == "confirm-wins" {
								_, e = r.CopilotExecutionCancel(ctx, mutation, historyTestAllow)
							} else {
								_, e = r.CopilotExecutionConfirm(ctx, mutation, historyTestAllow)
							}
							second <- e
						}()
						executionMySQLWait(t, db, second)
						close(release)
						require.NoError(t, awaitRetryMySQL(t, first))
						if scenario == "duplicate-ack" {
							require.NoError(t, awaitRetryMySQL(t, second))
						} else {
							require.ErrorIs(t, awaitRetryMySQL(t, second), ErrCopilotHistoryConflict)
						}
						// Simulated dropped first response: independent repository inspects primary.
						restarted := &Repositories{database: statusTestDB{db.Session(&gorm.Session{NewDB: true})}}
						got, e := restarted.CopilotExecutionGet(ctx, v.OwnerUserID, v.ProjectID, v.IntentID, historyTestAllow)
						require.NoError(t, e)
						var n int64
						require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&n).Error)
						if scenario == "cancel-wins" {
							require.Equal(t, "cancelled", got.State)
							require.Zero(t, n)
						} else {
							require.Equal(t, "submitted", got.State)
							require.EqualValues(t, 1, n)
							require.Equal(t, v.DecisionID, got.DecisionID)
						}
					case "revocation-wait", "expiry-wait":
						if scenario == "expiry-wait" {
							v.ExpiresAt = time.Now().UTC().Add(600 * time.Millisecond).Truncate(time.Millisecond)
							v.IntentDigest = executionDigest(v)
							require.NoError(t, db.Save(v).Error)
							mutation = executionMutationInput(v)
						}
						holder := db.Begin()
						require.NoError(t, holder.Error)
						defer holder.Rollback()
						if scenario == "revocation-wait" {
							require.NoError(t, holder.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error)
						} else {
							require.NoError(t, holder.Model(&model.CopilotExecutionIntent{}).Where("intent_id = ?", v.IntentID).UpdateColumn("id", gorm.Expr("id")).Error)
						}
						done := make(chan error, 1)
						go func() { _, e := r.CopilotExecutionConfirm(ctx, mutation, historyTestAllow); done <- e }()
						executionMySQLWait(t, db, done)
						if scenario == "expiry-wait" {
							time.Sleep(time.Until(v.ExpiresAt) + 20*time.Millisecond)
						}
						require.NoError(t, holder.Commit().Error)
						require.Error(t, awaitRetryMySQL(t, done))
						executionMySQLNoVote(t, db)
					case "ordinary-vote":
						ar := &ApprovalRepo{IDatabase: statusTestDB{db}}
						_, e := ar.Decide(ctx, v.ApprovalID, v.OwnerUserID, model.ApprovalDecisionApprove, "")
						require.NoError(t, e)
						_, e = r.CopilotExecutionConfirm(ctx, mutation, historyTestAllow)
						require.ErrorIs(t, e, ErrCopilotHistoryConflict)
						got, e := r.CopilotExecutionGet(ctx, v.OwnerUserID, v.ProjectID, v.IntentID, historyTestAllow)
						require.NoError(t, e)
						require.Equal(t, "prepared", got.State)
					case "detached-history":
						require.NoError(t, db.Where("conversation_id = ?", in.ConversationID).Delete(&model.CopilotConversation{}).Error)
						require.NoError(t, db.Where("plan_id = ?", in.PlanID).Delete(&model.CopilotActionPlan{}).Error)
						_, _, e := r.CopilotExecutionPrepare(ctx, in, historyTestAllow)
						require.Error(t, e)
						page, e := r.CopilotExecutionList(ctx, v.OwnerUserID, v.ProjectID, 0, 20, historyTestAllow)
						require.NoError(t, e)
						require.Len(t, page.List, 1)
						_, e = r.CopilotExecutionConfirm(ctx, mutation, historyTestAllow)
						require.NoError(t, e)
					case "sparse-list":
						rows := make([]model.CopilotExecutionIntent, 2000)
						for i := range rows {
							rows[i] = *v
							rows[i].ID = 0
							rows[i].OwnerUserID = fmt.Sprintf("sparse-owner-%04d", i)
							rows[i].IntentID = fmt.Sprintf("sparse-intent-%04d", i)
							rows[i].DecisionID = fmt.Sprintf("sparse-decision-%04d", i)
							rows[i].IntentDigest = executionDigest(&rows[i])
						}
						require.NoError(t, db.CreateInBatches(rows, 100).Error)

						target := *v
						target.ID = 0
						target.IntentID = "sparse-target"
						target.DecisionID = "sparse-target-decision"
						target.RequestID = "sparse-target-request"
						target.IntentDigest = executionDigest(&target)
						require.NoError(t, db.Create(&target).Error)
						query := "SELECT * FROM copilot_execution_intent WHERE owner_user_id = ? AND project_id = ? AND id > ? ORDER BY id ASC LIMIT 20"
						var structured, actual string
						require.NoError(t, db.Raw("EXPLAIN FORMAT=JSON "+query, v.OwnerUserID, v.ProjectID, v.ID).Row().Scan(&structured))
						require.NoError(t, db.Raw("EXPLAIN ANALYZE "+query, v.OwnerUserID, v.ProjectID, v.ID).Row().Scan(&actual))
						// Only this disposable fixture's fixed identifiers are queried.
						// Preserve bounded optimizer evidence for independent CI review.
						require.LessOrEqual(t, len(structured), 16384)
						require.LessOrEqual(t, len(actual), 8192)
						t.Logf("execution-list-plan collation=%s after=%d structured=%q actual=%q", collation, v.ID, structured, actual)
						require.NoError(t, validateExecutionMySQLListPlan(structured, actual, v.ID), actual)
						page, e := r.CopilotExecutionList(ctx, v.OwnerUserID, v.ProjectID, v.ID, 20, historyTestAllow)
						require.NoError(t, e)
						require.Len(t, page.List, 1)
						require.Equal(t, target.IntentID, page.List[0].IntentID)
						t.Log("execution scoped list actual-work and exact one-row content proof passed: " + collation)

					case "scope-lock-deadlock":
						// Reproduce the documented run -> pipeline / pipeline -> run
						// cycle. A database rollback is reported; never automatically
						// resubmit a vote after an uncertain transaction result.
						holder := db.Begin()
						require.NoError(t, holder.Error)
						defer holder.Rollback()
						require.NoError(t, holder.Model(&model.Pipeline{}).Where("pipeline_id = ?", v.PipelineID).UpdateColumn("id", gorm.Expr("id")).Error)
						done := make(chan error, 1)
						go func() { _, e := r.CopilotExecutionConfirm(ctx, mutation, historyTestAllow); done <- e }()
						executionMySQLWait(t, db, done)
						holderErr := holder.Model(&model.PipelineRun{}).Where("run_id = ?", v.RunID).UpdateColumn("id", gorm.Expr("id")).Error
						if holderErr != nil {
							require.NoError(t, holder.Rollback().Error)
						} else {
							require.NoError(t, holder.Commit().Error)
						}
						confirmErr := awaitRetryMySQL(t, done)
						require.True(t, (holderErr != nil) != (confirmErr != nil), "exactly one transaction must be the deadlock victim")
						got, e := r.CopilotExecutionGet(ctx, v.OwnerUserID, v.ProjectID, v.IntentID, historyTestAllow)
						require.NoError(t, e)
						var n int64
						require.NoError(t, db.Model(&model.ApprovalDecision{}).Count(&n).Error)
						if confirmErr != nil {
							require.ErrorIs(t, confirmErr, ErrCopilotHistoryUnavailable)
							require.Equal(t, "prepared", got.State)
							require.Zero(t, n)
						} else {
							require.Equal(t, "submitted", got.State)
							require.EqualValues(t, 1, n)
						}
					case "username-login-identifiers":
						users := []model.User{
							{UserID: "login-first", Username: "login-first", Email: "", Password: "first-stored-hash", IsEnabled: 1},
							{UserID: "login-second", Username: "login-second", Email: "", Password: "second-stored-hash", IsEnabled: 1},
							{UserID: "login-email", Username: "login-email", Email: "login@example.invalid", Password: "email-stored-hash", IsEnabled: 1},
							{UserID: "login-disabled", Username: "login-disabled", Email: "disabled@example.invalid", IsEnabled: 0},
						}
						require.NoError(t, db.Create(&users).Error)
						userRepo := NewUserRepo(statusTestDB{db}, nil)
						got, e := userRepo.Login(&model.Login{Username: "login-second"})
						require.NoError(t, e)
						require.Equal(t, "login-second", got.UserID)
						require.Equal(t, "second-stored-hash", got.Password)
						got, e = userRepo.Login(&model.Login{Email: "login@example.invalid"})
						require.NoError(t, e)
						require.Equal(t, "login-email", got.UserID)
						got, e = userRepo.Login(&model.Login{Username: "login-email", Email: "login@example.invalid"})
						require.NoError(t, e)
						require.Equal(t, "login-email", got.UserID)
						for _, input := range []*model.Login{nil, {}, {Username: "login-first", Email: "login@example.invalid"}, {Username: "missing"}, {Email: "missing@example.invalid"}, {Username: "login-disabled"}, {Email: "disabled@example.invalid"}} {
							got, e = userRepo.Login(input)
							require.ErrorIs(t, e, gorm.ErrRecordNotFound)
							require.Nil(t, got)
						}
						// Preserve the existing database collation contract; the
						// repository does not silently fold or trim identifiers.
						for _, input := range []*model.Login{{Username: "LOGIN-SECOND"}, {Email: "LOGIN@EXAMPLE.INVALID"}} {
							got, e = userRepo.Login(input)
							if collation == "utf8mb4_0900_ai_ci" {
								require.NoError(t, e)
								require.NotNil(t, got)
							} else {
								require.ErrorIs(t, e, gorm.ErrRecordNotFound)
								require.Nil(t, got)
							}
						}
					case "current-read-denial":
						require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", v.OwnerUserID).Update("is_enabled", 0).Error)
						_, e := r.CopilotExecutionGet(ctx, v.OwnerUserID, v.ProjectID, v.IntentID, historyTestAllow)
						require.ErrorIs(t, e, ErrCopilotHistoryForbidden)
					}
				})
			}
		})
	}
}
