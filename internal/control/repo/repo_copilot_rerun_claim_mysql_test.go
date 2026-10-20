// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestCopilotRerunClaimMySQLRequiredGate(t *testing.T) {
	required := os.Getenv("ARCENTRA_REQUIRE_COPILOT_RERUN_MYSQL")
	require.True(t, required == "" || required == "1")
	dsn := os.Getenv("ARCENTRA_COPILOT_RERUN_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required disposable rerun MySQL missing")
		}
		t.Skip("real MySQL absent; no claim concurrency acceptance claim")
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
			for _, scenario := range []string{"claim-first", "cancel-first", "same-incarnation", "different-claim", "restart-nonce", "expiry-wait", "revocation-wait", "rollback", "lost-commit-ack", "schedule-expiry-wait", "schedule-authority-expiry", "schedule-lease-takeover", "storage-pin", "storage-disabled-wait", "storage-rollback", "input-pin", "input-rollback", "input-schedule-expiry", "input-schedule-cancel", "generation-bootstrap", "generation-startup-expiry", "generation-startup-grant-expiry", "generation-vote-cancel"} {
				t.Run(scenario, func(t *testing.T) {
					models := append(rerunConfirmMySQLModels(), &model.Agent{}, &model.RoleGrant{}, &model.StorageConfig{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{})
					scoped := db.Session(&gorm.Session{NewDB: true}).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation)
					require.NoError(t, scoped.AutoMigrate(models...))
					defer func() {
						for i := len(models) - 1; i >= 0; i-- {
							require.NoError(t, db.Migrator().DropTable(models[i]))
						}
					}()
					r, saved, lease, in := rerunSealMySQLSeed(t, db)
					rerunClaimMySQLScenario(t, db, r, saved, lease, in, scenario)
				})
			}
		})
	}
}
func rerunClaimMySQLScenario(t *testing.T, db *gorm.DB, r *Repositories, saved *model.CopilotRerunIntent, lease CopilotRerunLeaseToken, sealIn CopilotRerunSealInput, scenario string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if scenario == "expiry-wait" || scenario == "schedule-expiry-wait" || scenario == "input-schedule-expiry" {
		saved.ExpiresAt = time.Now().UTC().Truncate(time.Millisecond).Add(time.Second)
		saved.IntentDigest = rerunIntentDigest(saved)
		sealIn.ExpectedIntentDigest = saved.IntentDigest
		require.NoError(t, db.Model(&model.CopilotRerunIntent{}).Where("intent_id = ?", saved.IntentID).Updates(map[string]any{"expires_at": saved.ExpiresAt, "intent_digest": saved.IntentDigest}).Error)
		require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", saved.ScheduleID).Update("expires_at", saved.ExpiresAt).Error)
	}
	sealed, err := r.CopilotRerunSeal(ctx, lease, sealIn, permitRerun, testRerunSealBuilder)
	require.NoError(t, err)
	sealDigest, err := sealed.Binding.Digest()
	require.NoError(t, err)
	in := CopilotRerunClaimInput{AgentID: sealIn.AgentID, PipelineID: saved.PipelineID, RunID: saved.NewRunID, JobRunID: saved.NewJobRunID, GateID: saved.GateID, ScheduleID: saved.ScheduleID, SealID: sealIn.SealID, SealDigest: sealDigest, PayloadDigest: sealed.Binding.PayloadDigest, ClaimID: uuid.NewString(), AdmissionNonce: []byte(strings.Repeat("synthetic-nonce!", 2))}
	if strings.HasPrefix(scenario, "storage-") {
		rerunClaimStorageSeed(t, db, &in)
	}
	cancelIn := CopilotRerunTargetCancelInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedIntentDigest: saved.IntentDigest, ExpectedIntentVersion: saved.Version, ExpectedGateVersion: 2, ExpectedScheduleVersion: 3}
	if strings.HasPrefix(scenario, "generation-") {
		rerunGenerationMySQLScenario(t, db, r, saved, in, scenario)
		return
	}
	if strings.HasPrefix(scenario, "input-") {
		_, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
		require.NoError(t, err)
		var job model.JobRun
		require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
		identity := generationIdentity(&job)
		expected := &taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: in.ScheduleID, SealID: in.SealID, ExpectedSourceCommit: job.ExpectedSourceCommit}
		digest, err := expected.Digest(identity)
		require.NoError(t, err)
		store := r.JobRun.(*JobRunRepo)
		unknown, _, err := store.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, nil)
		require.NoError(t, err)
		require.Equal(t, "unknown", unknown.InputManifestState)
		require.Empty(t, unknown.RetryApprovalID)
		proposed, err := taskqueue.NewJobInputManifest(expected.ExpectedSourceCommit, nil, nil)
		require.NoError(t, err)
		if scenario == "input-schedule-expiry" || scenario == "input-schedule-cancel" {
			first, holder := rerunMySQLConnection(t, db)
			second, waiter := rerunMySQLConnection(t, db)
			secondStore := NewRepositories(statusTestDB{second}, nil).JobRun.(*JobRunRepo)
			held, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() {
				firstDone <- first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var schedule model.CopilotRerunSchedule
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", in.ScheduleID).First(&schedule).Error; err != nil {
						return err
					}
					close(held)
					<-release
					if scenario == "input-schedule-cancel" {
						return tx.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", in.ScheduleID).Update("cancel_requested", true).Error
					}
					return nil
				})
			}()
			select {
			case <-held:
			case err := <-firstDone:
				t.Fatalf("input schedule holder: %v", err)
			case <-ctx.Done():
				t.Fatal("holder timeout")
			}
			go func() {
				_, _, err := secondStore.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
				secondDone <- err
			}()
			rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
			if scenario == "input-schedule-expiry" {
				time.Sleep(time.Until(saved.ExpiresAt) + 5*time.Millisecond)
			}
			close(release)
			require.NoError(t, <-firstDone)
			require.Error(t, <-secondDone)
		} else if scenario == "input-rollback" {
			const callback = "rerun-input-approval-rollback"
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "ApprovalRequest" {
					tx.AddError(ErrCopilotRerunConflict)
				}
			}))
			got, _, err := store.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
			require.NoError(t, db.Callback().Create().Remove(callback))
			require.Error(t, err)
			require.Nil(t, got)
		} else {
			got, _, err := store.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
			require.NoError(t, err)
			require.NotEmpty(t, got.RetryApprovalID)
			replay, _, err := store.GetOrRecordJobInputsWithSourceExpectation(ctx, identity, digest, proposed)
			require.NoError(t, err)
			require.Equal(t, got.RetryApprovalID, replay.RetryApprovalID)
			var approval model.ApprovalRequest
			require.NoError(t, db.Where("approval_id = ?", got.RetryApprovalID).First(&approval).Error)
			require.Equal(t, in.RunID, approval.PipelineRunID)
			require.Equal(t, in.JobRunID, *approval.RetryJobRunID)
			require.Equal(t, model.ApprovalStatusPending, approval.Status)
			return
		}
		var after model.JobRun
		require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&after).Error)
		require.Equal(t, "unknown", after.InputManifestState)
		require.Empty(t, after.RetryApprovalID)
		var count int64
		require.NoError(t, db.Model(&model.ApprovalRequest{}).Count(&count).Error)
		require.Zero(t, count)
		return
	}
	if scenario == "storage-pin" {
		got, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
		require.NoError(t, err)
		require.Equal(t, in.ArtifactStorageID, got.Receipt.ArtifactStorageID)
		require.Equal(t, in.ArtifactStorageFingerprint, got.Receipt.ArtifactStorageFingerprint)
		require.Equal(t, "s3", got.Receipt.ArtifactStorageType)
		receipt, err := r.CopilotRerunInspectClaim(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
		require.NoError(t, err)
		require.Equal(t, got.Receipt, receipt)
		changed := in
		changed.ArtifactStorageID = strings.ToUpper(in.ArtifactStorageID)
		denied, err := r.CopilotRerunClaim(ctx, changed, permitRerun, testRerunClaimValidator)
		require.Error(t, err)
		require.Nil(t, denied)
		return
	}
	if scenario == "lost-commit-ack" {
		pool, err := db.DB()
		require.NoError(t, err)
		lose := &atomic.Bool{}
		lose.Store(true)
		wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
		wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
		uncertain := NewRepositories(statusTestDB{wrapped}, nil)
		got, err := uncertain.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
		require.Nil(t, got)
		require.ErrorIs(t, err, ErrCopilotRerunUnavailable)
		receipt, err := r.CopilotRerunInspectClaim(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
		require.NoError(t, err)
		require.Equal(t, in.ClaimID, receipt.ClaimID)
		got, err = r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
		require.NoError(t, err)
		require.Equal(t, receipt, got.Receipt)
		return
	}
	if scenario == "rollback" || scenario == "storage-rollback" {
		const name = "rerun-claim-rollback"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunSchedule" {
				tx.AddError(errors.New("synthetic schedule write failure"))
			}
		}))
		got, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
		require.NoError(t, db.Callback().Update().Remove(name))
		require.Error(t, err)
		require.Nil(t, got)
		var gate model.CopilotRerunAdmissionGate
		var job model.JobRun
		require.NoError(t, db.Where("gate_id = ?", saved.GateID).First(&gate).Error)
		require.Empty(t, gate.ClaimID)
		require.Empty(t, gate.AdmissionNonceDigest)
		require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
		require.Empty(t, job.ClaimID)
		require.Nil(t, job.StartTime)
		require.Empty(t, job.ArtifactStorageID)
		require.Empty(t, job.ArtifactStorageFingerprint)
		require.Empty(t, job.ArtifactStorageType)
		return
	}
	first, holder := rerunMySQLConnection(t, db)
	second, waiter := rerunMySQLConnection(t, db)
	firstRepo, secondRepo := NewRepositories(statusTestDB{first}, nil), NewRepositories(statusTestDB{second}, nil)

	if scenario == "storage-disabled-wait" {
		held, release := make(chan struct{}), make(chan struct{})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		go func() {
			firstDone <- first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var storage model.StorageConfig
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("storage_id = ?", in.ArtifactStorageID).First(&storage).Error; err != nil {
					return err
				}
				close(held)
				<-release
				return tx.Model(&model.StorageConfig{}).Where("storage_id = ?", in.ArtifactStorageID).Update("is_enabled", 0).Error
			})
		}()
		select {
		case <-held:
		case err := <-firstDone:
			t.Fatalf("storage holder ended: %v", err)
		case <-ctx.Done():
			t.Fatal("storage holder timeout")
		}
		go func() {
			_, err := secondRepo.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
			secondDone <- err
		}()
		rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
		close(release)
		require.NoError(t, <-firstDone)
		require.Error(t, <-secondDone)
		var job model.JobRun
		require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
		require.Empty(t, job.ClaimID)
		require.Empty(t, job.ArtifactStorageID)
		return
	}
	if strings.HasPrefix(scenario, "schedule-") {
		expiry := time.Now().UTC().Truncate(time.Millisecond).Add(time.Second)
		validate := CopilotRerunClaimValidator(testRerunClaimValidator)
		if scenario == "schedule-authority-expiry" {
			require.NoError(t, db.Create(&model.RoleGrant{GrantID: "candidate-expiring", SubjectType: "user", SubjectID: "approver", RoleID: "reviewer", ScopeType: "project", ScopeID: saved.ProjectID, IsEnabled: 1, ExpiresAt: &expiry}).Error)
			validate = func(ctx context.Context, view *Repositories, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, schedule *model.CopilotRerunSchedule) (*CopilotRerunClaimMaterial, error) {
				grants, err := view.RoleGrant.ListAllEnabled(ctx)
				if err != nil {
					return nil, err
				}
				if len(grants) != 1 {
					return nil, ErrCopilotRerunForbidden
				}
				return testRerunClaimValidator(ctx, view, scope, snapshot, evidence, ids, intent, agent, schedule)
			}
		}
		held, release := make(chan struct{}), make(chan struct{})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		go func() {
			firstDone <- first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var schedule model.CopilotRerunSchedule
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("schedule_id = ?", in.ScheduleID).First(&schedule).Error; err != nil {
					return err
				}
				close(held)
				<-release
				if scenario == "schedule-lease-takeover" {
					return tx.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", in.ScheduleID).Updates(map[string]any{"lease_owner": "replacement-controller", "lease_epoch": schedule.LeaseEpoch + 1, "lease_until": time.Now().UTC().Truncate(time.Millisecond).Add(time.Minute), "version": schedule.Version + 1}).Error
				}
				return nil
			})
		}()
		select {
		case <-held:
		case err := <-firstDone:
			t.Fatalf("schedule holder ended: %v", err)
		case <-ctx.Done():
			t.Fatal("schedule holder timeout")
		}
		go func() { _, err := secondRepo.CopilotRerunClaim(ctx, in, permitRerun, validate); secondDone <- err }()
		rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
		if scenario == "schedule-expiry-wait" {
			time.Sleep(time.Until(saved.ExpiresAt) + 5*time.Millisecond)
		}
		if scenario == "schedule-authority-expiry" {
			time.Sleep(time.Until(expiry) + 5*time.Millisecond)
		}
		close(release)
		require.NoError(t, <-firstDone)
		err := <-secondDone
		var job model.JobRun
		var gate model.CopilotRerunAdmissionGate
		require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
		require.NoError(t, db.Where("gate_id = ?", in.GateID).First(&gate).Error)
		if scenario == "schedule-lease-takeover" {
			require.NoError(t, err)
			require.Equal(t, in.ClaimID, job.ClaimID)
			require.True(t, rerunClaimNonceMatches(gate.AdmissionNonceDigest, in))
		} else {
			require.Error(t, err)
			require.Empty(t, job.ClaimID)
			require.Empty(t, gate.AdmissionNonceDigest)
		}
		return
	}
	if scenario == "expiry-wait" || scenario == "revocation-wait" {
		held, release := make(chan struct{}), make(chan struct{})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		go func() {
			firstDone <- first.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var gate model.CopilotRerunAdmissionGate
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("gate_id = ?", in.GateID).First(&gate).Error; err != nil {
					return err
				}
				close(held)
				<-release
				if scenario == "revocation-wait" {
					return tx.Model(&model.User{}).Where("user_id = ?", saved.OwnerUserID).Update("is_enabled", 0).Error
				}
				return nil
			})
		}()
		select {
		case <-held:
		case err := <-firstDone:
			t.Fatalf("holder ended: %v", err)
		case <-ctx.Done():
			t.Fatal("holder timeout")
		}
		go func() {
			_, err := secondRepo.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
			secondDone <- err
		}()
		rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
		if scenario == "expiry-wait" {
			time.Sleep(time.Until(saved.ExpiresAt) + 5*time.Millisecond)
		}
		close(release)
		require.NoError(t, <-firstDone)
		require.Error(t, <-secondDone)
		var job model.JobRun
		require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
		require.Empty(t, job.ClaimID)
		return
	}

	held, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var once atomic.Bool
	const name = "rerun-claim-holder"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "CopilotRerunAdmissionGate" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if once.CompareAndSwap(false, true) {
			close(held)
			<-release
		}
	}))
	defer func() { require.NoError(t, db.Callback().Query().Remove(name)) }()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		var err error
		if scenario == "cancel-first" {
			_, err = firstRepo.CopilotRerunCancelUnclaimed(ctx, cancelIn, permitRerun)
		} else {
			_, err = firstRepo.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
		}
		firstDone <- err
	}()
	select {
	case <-held:
	case err := <-firstDone:
		t.Fatalf("holder ended before lock: %v", err)
	case <-ctx.Done():
		t.Fatal("holder timeout")
	}
	go func() {
		var err error
		if scenario == "claim-first" {
			_, err = secondRepo.CopilotRerunCancelUnclaimed(ctx, cancelIn, permitRerun)
		} else {
			request := in
			if scenario == "different-claim" {
				request.ClaimID = uuid.NewString()
			}
			if scenario == "restart-nonce" {
				request.AdmissionNonce = append([]byte(nil), in.AdmissionNonce...)
				request.AdmissionNonce[0] ^= 1
			}
			_, err = secondRepo.CopilotRerunClaim(ctx, request, permitRerun, testRerunClaimValidator)
		}
		secondDone <- err
	}()
	rerunMySQLWaitEdge(t, db, holder, waiter, secondDone)
	close(release)
	require.NoError(t, <-firstDone)
	err = <-secondDone
	if scenario == "same-incarnation" {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, ErrCopilotRerunConflict)
		if scenario == "different-claim" || scenario == "restart-nonce" {
			require.ErrorIs(t, err, ErrCopilotRerunClaimRejected)
			require.ErrorIs(t, err, errCopilotRerunIncarnationMismatch)
		}
	}
}
