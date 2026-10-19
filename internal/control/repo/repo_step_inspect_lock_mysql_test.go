// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The inspector reads its locator before the first admission commits, then
// waits on the exact parent Job lock. Its later ledger must see the committed
// receipt under MySQL REPEATABLE READ, without issuing a second admission.
func TestStepRetryApprovalMySQLFreshAdmissionInspectWait(t *testing.T) {
	f, schema := stepRetryApprovalMySQLFixture(t, 1)
	approveRetryApproval(t, f.generationFixture)
	stepRetryApprovalStartup(t, f)
	request := f.request(t, 0)
	inspect := request
	inspect.Operation = "inspect"
	var err error
	inspect.RequestDigest, err = inspect.Digest()
	require.NoError(t, err)
	var beforeGeneration model.JobRunGeneration
	require.NoError(t, f.db.Where("generation_id = ?", f.generation.GenerationID).First(&beforeGeneration).Error)
	beforeJob := stepAttemptMySQLReadJob(t, f)
	var fresh, observed *taskqueue.StepAttemptResult
	var firstComplete, hintBeforeCommit atomic.Bool
	const callback = "test:mysql-inspect-locator-before-commit"
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Context.Value(stepAttemptMySQLOrderKey{}) != "second" || tx.Statement.Table != (model.JobRun{}).TableName() {
			return
		}
		lock, _ := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
		if lock.Strength != "" {
			return
		}
		if tx.RowsAffected != 1 || !stepAttemptMySQLExactDestination(tx.Statement.Dest, "JobRunID", f.identity.JobRunID) {
			return
		}
		if _, insideTransaction := tx.Statement.ConnPool.(gorm.TxCommitter); insideTransaction {
			tx.AddError(errors.New("inspect locator ran inside a transaction"))
			return
		}
		if !firstComplete.Load() {
			hintBeforeCommit.Store(true)
		}
	}))
	defer func() { require.NoError(t, f.db.Callback().Query().Remove(callback)) }()
	admit := func(ctx context.Context) error {
		var e error
		fresh, e = f.jobs.ReconcileStepAttempt(ctx, request)
		firstComplete.Store(true)
		return e
	}
	read := func(ctx context.Context) error {
		var e error
		observed, e = f.jobs.ReconcileStepAttempt(ctx, inspect)
		return e
	}
	outcomes := stepAttemptMySQLOrderedRace(t, f, schema, admit, read)
	require.NoError(t, outcomes[0])
	require.NoError(t, outcomes[1])
	require.True(t, hintBeforeCommit.Load(), "inspect locator must precede first commit and transaction begin")
	require.NotNil(t, fresh)
	require.NotNil(t, observed)
	require.True(t, fresh.ExecutionAllowed)
	require.False(t, observed.ExecutionAllowed)
	require.Equal(t, fresh.Admission, observed.Admission)
	require.NotNil(t, fresh.Admission)
	original, err := fresh.Admission.Canonical()
	require.NoError(t, err)
	seen, err := observed.Admission.Canonical()
	require.NoError(t, err)
	require.Equal(t, original, seen)
	assertStepAttemptMySQLLedger(t, f, request, fresh.Admission)
	var count int64
	require.NoError(t, f.db.Model(&model.StepRun{}).Where("job_run_id = ? AND attempt_admission <> ?", f.identity.JobRunID, "").Count(&count).Error)
	require.EqualValues(t, 1, count)
	var afterGeneration model.JobRunGeneration
	require.NoError(t, f.db.Where("generation_id = ?", f.generation.GenerationID).First(&afterGeneration).Error)
	require.Equal(t, beforeGeneration, afterGeneration)
	require.Equal(t, beforeJob, stepAttemptMySQLReadJob(t, f))
	require.NoError(t, f.db.Model(&model.ApprovalDecision{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	t.Log("pretransaction inspect locator preceded exact Job lock wait; original committed receipt returned byte-for-byte without execution authority or extra admission")
}
