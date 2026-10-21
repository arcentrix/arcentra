// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// This opt-in fixture requires a disposable empty MySQL schema and exact server
// wait-edge evidence. SQLite success is not a concurrency acceptance substitute.
func TestCopilotRerunCancelActorMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable rerun MySQL missing")
		}
		t.Skip("real MySQL absent; actor cancellation contention unverified")
	}
	cfg, err := rerunConfirmMySQLConfig(dsn)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(12)
	defer pool.Close()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables)
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			for _, scenario := range []string{"claim-first", "cancel-first", "actor-revocation-wait", "grant-expiry-wait", "pointer-fence-wait"} {
				t.Run(scenario, func(t *testing.T) {
					models := append(rerunConfirmMySQLModels(), &model.Agent{}, &model.RoleGrant{})
					require.NoError(t, db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation).AutoMigrate(models...))
					defer func() {
						for i := len(models) - 1; i >= 0; i-- {
							require.NoError(t, db.Migrator().DropTable(models[i]))
						}
					}()
					r, saved, lease, sealIn := rerunSealMySQLSeed(t, db)
					actor := &model.User{UserID: "cancel-actor", IsEnabled: 1}
					retryApprovalMySQLJSONDefaults(actor)
					require.NoError(t, retryApprovalMySQLValidateJSON(db, actor))
					require.NoError(t, db.Create(actor).Error)
					grant := &model.RoleGrant{GrantID: "cancel-grant", SubjectType: "user", SubjectID: actor.UserID, RoleID: "cancel-role", ScopeType: "project", ScopeID: saved.ProjectID, IsEnabled: 1}
					require.NoError(t, db.Create(grant).Error)
					authorize := func(ctx context.Context, view *Repositories, scope *CopilotRerunScope, id, action string) error {
						if id != actor.UserID || action != "cancel" || scope.Project.ProjectID != saved.ProjectID {
							return ErrCopilotRerunForbidden
						}
						grants, err := view.RoleGrant.ListAllEnabled(ctx)
						if err != nil {
							return err
						}
						for _, g := range grants {
							if g.GrantID == grant.GrantID && g.SubjectID == id && g.ScopeID == scope.Project.ProjectID && g.IsEnabled == 1 && (g.ExpiresAt == nil || g.ExpiresAt.After(time.Now())) {
								return nil
							}
						}
						return ErrCopilotRerunForbidden
					}
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					sealed, err := r.CopilotRerunSeal(ctx, lease, sealIn, permitRerun, testRerunSealBuilder)
					require.NoError(t, err)
					claim := rerunCancelActorClaimInput(t, saved, sealIn, sealed)
					hint, err := r.LocateCopilotRerunCancellation(ctx, saved.PipelineID, saved.NewRunID)
					require.NoError(t, err)
					first, holder := rerunMySQLConnection(t, db)
					second, waiter := rerunMySQLConnection(t, db)
					firstRepo, secondRepo := NewRepositories(statusTestDB{first}, nil), NewRepositories(statusTestDB{second}, nil)
					workers := newRerunMySQLWorkers(ctx)
					ctx = workers.ctx
					held, release := make(chan struct{}), workers.release
					const hook = "cancel-actor-gate-holder"
					hookRegistered := false
					defer func() {
						workers.finish()
						if hookRegistered {
							_ = db.Callback().Query().Remove(hook)
						}
					}()
					var firstDone <-chan error
					if scenario == "claim-first" || scenario == "cancel-first" {
						var once atomic.Bool
						require.NoError(t, db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
							if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "CopilotRerunAdmissionGate" {
								return
							}
							if _, ok := tx.Statement.Clauses["FOR"]; !ok {
								return
							}
							if once.CompareAndSwap(false, true) {
								close(held)
								select {
								case <-release:
								case <-ctx.Done():
									tx.AddError(ctx.Err())
								}
							}
						}))
						hookRegistered = true
						firstDone = workers.start(func() error {
							var err error
							if scenario == "claim-first" {
								_, err = firstRepo.CopilotRerunClaim(ctx, claim, permitRerun, testRerunClaimValidator)
							} else {
								_, err = firstRepo.CopilotRerunCancelUnclaimedAs(ctx, actor.UserID, hint.Unclaimed, authorize)
							}
							return err
						})
					} else {
						if scenario == "grant-expiry-wait" {
							expiry := time.Now().UTC().Add(time.Second)
							grant.ExpiresAt = &expiry
							require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", grant.GrantID).Update("expires_at", expiry).Error)
						}
						firstDone = workers.start(func() error {
							return first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
								var err error
								if scenario == "grant-expiry-wait" {
									err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", saved.ScheduleID).First(&model.CopilotRerunSchedule{}).Error
								} else {
									err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("gate_id = ?", saved.GateID).First(&model.CopilotRerunAdmissionGate{}).Error
								}
								if err != nil {
									return err
								}
								close(held)
								<-release
								if scenario == "actor-revocation-wait" {
									return tx.Model(&model.User{}).Where("user_id = ?", actor.UserID).Update("is_enabled", 0).Error
								}
								if scenario == "pointer-fence-wait" {
									return tx.Model(&model.CopilotRerunAdmissionGate{}).Where("gate_id = ?", saved.GateID).Update("pipeline_id", "foreign-pipeline").Error
								}
								return nil
							})
						})
					}
					select {
					case <-held:
					case err := <-firstDone:
						t.Fatalf("holder ended before lock: %v", err)
					case <-ctx.Done():
						t.Fatal("holder deadline")
					}
					secondDone := workers.start(func() error {
						var err error
						if scenario == "cancel-first" {
							_, err = secondRepo.CopilotRerunClaim(ctx, claim, permitRerun, testRerunClaimValidator)
						} else {
							_, err = secondRepo.CopilotRerunCancelUnclaimedAs(ctx, actor.UserID, hint.Unclaimed, authorize)
						}
						return err
					})
					rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
					if scenario == "grant-expiry-wait" {
						time.Sleep(time.Until(*grant.ExpiresAt) + 5*time.Millisecond)
					}
					workers.releaseHolder()
					firstErr := awaitRetryMySQL(t, firstDone)
					secondErr := awaitRetryMySQL(t, secondDone)
					require.NoError(t, firstErr)
					require.Error(t, secondErr)
					var job model.JobRun
					require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
					switch scenario {
					case "claim-first":
						require.Equal(t, claim.ClaimID, job.ClaimID)
						require.Equal(t, model.JobRunStatusRunning, job.Status)
					case "cancel-first":
						require.Empty(t, job.ClaimID)
						require.Equal(t, model.JobRunStatusCancelled, job.Status)
					default:
						require.Empty(t, job.ClaimID)
						require.Equal(t, model.JobRunStatusPending, job.Status)
					}
					var intent model.CopilotRerunIntent
					require.NoError(t, db.Where("intent_id = ?", saved.IntentID).First(&intent).Error)
					require.Equal(t, saved.OwnerUserID, intent.OwnerUserID)
				})
			}
		})
	}
}

func rerunCancelActorClaimInput(t *testing.T, saved *model.CopilotRerunIntent, in CopilotRerunSealInput, sealed *CopilotRerunSealReceipt) CopilotRerunClaimInput {
	t.Helper()
	digest, err := sealed.Binding.Digest()
	require.NoError(t, err)
	claim := CopilotRerunClaimInput{AgentID: in.AgentID, PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: saved.GateID, ScheduleID: saved.ScheduleID, SealID: in.SealID, SealDigest: digest, PayloadDigest: sealed.Binding.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: []byte(strings.Repeat("synthetic-nonce!", 2))}
	_, err = rerunClaimNonceDigest(claim)
	require.NoError(t, err, "the actor-cancellation competitor must pass request validation before racing")
	return claim
}

func TestCopilotRerunCancelActorMySQLClaimFixture(t *testing.T) {
	r, saved, lease, in := rerunSealFixture(t)
	ctx := context.Background()
	sealed, err := r.CopilotRerunSeal(ctx, lease, in, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	claim := rerunCancelActorClaimInput(t, saved, in, sealed)
	require.Len(t, claim.AdmissionNonce, 32)
	result, err := r.CopilotRerunClaim(ctx, claim, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, claim.ClaimID, result.Receipt.ClaimID)
}
