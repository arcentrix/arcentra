// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type historyObserveMySQLContextKey struct{}

// Characterization of the existing inversion using two actual repository paths.
// No synthetic lock SQL, mutation replay, or changes to the production lock order.
// When lock order is corrected, replace this expected-deadlock characterization
// with an observed-contention/both-success regression; do not retain the old
// two-held-lock barrier after those locks acquire the same order.
func historyObserveMySQLDeadlock(t *testing.T, db *gorm.DB, r *Repositories, in CopilotExecutionPrepareInput, review bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, err := r.CopilotHistoryGet(ctx, in.OwnerUserID, in.ProjectID, in.ConversationID, historyTestAllow)
	require.NoError(t, err)
	var plan *model.CopilotActionPlan
	if review {
		var parent model.CopilotTurn
		require.NoError(t, db.Where("conversation_id = ? AND state = ?", c.ConversationID, "completed").First(&parent).Error)
		var created bool
		plan, created, err = r.CopilotHistoryCreatePlan(ctx, CopilotHistoryPlanInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: parent.TurnID, PlanID: "observe-review-plan", RequestID: "observe-review-request", Kind: "retry_approval_review", PlanDigest: "observe-review-digest", ConversationVersion: c.Version, TurnVersion: parent.Version, SchemaVersion: 1, CipherVersion: 1, PayloadCiphertext: []byte("opaque-repository-fixture")}, historyTestAllow)
		require.NoError(t, err)
		require.True(t, created)
	}
	var beforeJob model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", "job").First(&beforeJob).Error)
	var beforeGeneration model.JobRunGeneration
	require.NoError(t, db.Where("generation_id = ?", beforeJob.CurrentGenerationID).First(&beforeGeneration).Error)
	var beforeApproval model.ApprovalRequest
	require.NoError(t, db.Where("approval_id = ?", beforeJob.RetryApprovalID).First(&beforeApproval).Error)
	_, descriptor, err := loadGenerationRead(ctx, db, beforeJob.CurrentGenerationID, false)
	require.NoError(t, err)
	request := retryapproval.Request{Version: 1, Operation: "observe", Identity: generationIdentity(&beforeJob), GenerationID: beforeGeneration.GenerationID, GenerationBindingDigest: descriptor.BindingDigest, RetryPlanDigest: beforeJob.RetryPlanDigest, InputManifestDigest: beforeJob.InputManifestDigest}
	historyHeld, observeHeld := make(chan struct{}), make(chan struct{})
	var historyOnce, observeOnce sync.Once
	var mu sync.Mutex
	deadlocks := 0
	const callbackName = "acceptance:history_observe_lock_cycle"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		path, _ := tx.Statement.Context.Value(historyObserveMySQLContextKey{}).(string)
		if path != "history" && path != "observe" {
			return
		}
		var driverErr *drivermysql.MySQLError
		if errors.As(tx.Error, &driverErr) && driverErr.Number == 1213 {
			mu.Lock()
			deadlocks++
			mu.Unlock()
		}
		if tx.Error != nil {
			return
		}
		locking, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
		if !ok || locking.Strength != "UPDATE" || tx.RowsAffected != 1 {
			return
		}
		wait := func(ch <-chan struct{}) {
			select {
			case <-ch:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		}
		if path == "history" && tx.Statement.Table == "pipeline" {
			historyOnce.Do(func() { close(historyHeld); wait(observeHeld) })
		}
		if path == "observe" && tx.Statement.Table == "pipeline_run" {
			observeOnce.Do(func() { close(observeHeld); wait(historyHeld) })
		}
	}))
	defer func() { require.NoError(t, db.Callback().Query().Remove(callbackName)) }()
	historyDone, observeDone := make(chan error, 1), make(chan error, 1)
	go func() {
		tagged := context.WithValue(ctx, historyObserveMySQLContextKey{}, "history")
		if review {
			_, e := r.CopilotHistoryTransitionPlan(tagged, CopilotHistoryPlanTransitionInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, PlanID: plan.PlanID, PlanDigest: plan.PlanDigest, Version: plan.Version, State: "reviewed"}, historyTestAllow)
			historyDone <- e
			return
		}
		_, _, e := r.CopilotHistoryReserve(tagged, historyReservation(c, "observe-reservation"), historyTestAllow)
		historyDone <- e
	}()
	go func() {
		jobs := &JobRunRepo{IDatabase: statusTestDB{db}}
		response, e := jobs.ReconcileJobRetryApproval(context.WithValue(ctx, historyObserveMySQLContextKey{}, "observe"), request)
		if e == nil && (response == nil || response.State != "pending" || response.ExecutionAllowed || response.Admission != nil) {
			e = errors.New("observe changed execution authority")
		}
		observeDone <- e
	}()
	// The shared deadline releases every callback barrier, including regressions
	// where one method no longer reaches its original lock. No indefinite waits.
	historyErr, observeErr := <-historyDone, <-observeDone
	require.NoError(t, ctx.Err(), "expected real MySQL deadlock, not deadline release")
	mu.Lock()
	observedDeadlocks := deadlocks
	mu.Unlock()
	require.Equal(t, 1, observedDeadlocks, "exact MySQL 1213 required")
	require.True(t, (historyErr != nil) != (observeErr != nil), "exactly one actual operation is the deadlock victim")
	select {
	case <-historyHeld:
	default:
		t.Fatal("history never held exact pipeline lock")
	}
	select {
	case <-observeHeld:
	default:
		t.Fatal("observe never held exact run lock")
	}
	var afterJob model.JobRun
	var afterGeneration model.JobRunGeneration
	var afterApproval model.ApprovalRequest
	require.NoError(t, db.Where("job_run_id = ?", beforeJob.JobRunID).First(&afterJob).Error)
	require.NoError(t, db.Where("generation_id = ?", beforeGeneration.GenerationID).First(&afterGeneration).Error)
	require.NoError(t, db.Where("approval_id = ?", beforeApproval.ApprovalID).First(&afterApproval).Error)
	require.Equal(t, beforeJob, afterJob)
	require.Equal(t, beforeGeneration, afterGeneration)
	require.Equal(t, beforeApproval, afterApproval)
	executionMySQLNoVote(t, db)
	t.Log("actual history/approval observe paths: MySQL 1213 witnessed; votes, approval, job and generation unchanged")
}
