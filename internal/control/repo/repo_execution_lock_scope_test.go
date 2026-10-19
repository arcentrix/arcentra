package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
	"path/filepath"
	"testing"
)

func TestExecutionScopePrefixOrderAndTransactionProof(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.AutoMigrate(&model.Organization{}))
	require.NoError(t, db.Create(&model.Organization{OrgID: "org"}).Error)
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("org_id", "org").Error)
	var locked []string
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("execution-lock-order", func(q *gorm.DB) {
		if _, ok := q.Statement.Clauses["FOR"]; ok {
			locked = append(locked, q.Statement.Table)
		}
	}))
	_, err := lockExecutionJob(ctx, db, "job", executionLockActive)
	require.Error(t, err)
	var captured executionScopeProof
	require.NoError(t, executionTransaction(ctx, db, "job", func(tx *gorm.DB) error {
		job, err := lockExecutionJob(ctx, tx, "job", executionLockActive)
		require.NoError(t, err)
		require.NoError(t, requireExecutionScope(tx, job))
		v, _ := tx.Statement.Settings.Load(executionScopeProofKey)
		captured = v.(executionScopeProof)
		other := *job
		other.JobRunID = "other"
		require.Error(t, requireExecutionScope(tx, &other))
		return nil
	}))
	require.Equal(t, []string{"pipeline", "organization", "project", "pipeline_run", "job_run"}, locked)
	require.NoError(t, executionTransaction(ctx, db, "job", func(tx *gorm.DB) error {
		tx.Statement.Settings.Store(executionScopeProofKey, captured)
		require.Error(t, requireExecutionScope(tx, &model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run"}))
		return nil
	}))
}
func TestExecutionScopeMissingModernScopeFailsClosed(t *testing.T) {
	for _, kind := range []string{"pipeline", "project", "run", "run-project", "case-alias"} {
		t.Run(kind, func(t *testing.T) {
			ctx, db, _, _ := sourceTestRepository(t)
			switch kind {
			case "pipeline":
				require.NoError(t, db.Where("pipeline_id = ?", "pipeline").Delete(&model.Pipeline{}).Error)
			case "project":
				require.NoError(t, db.Where("project_id = ?", "project").Delete(&model.Project{}).Error)
			case "run":
				require.NoError(t, db.Where("run_id = ?", "run").Delete(&model.PipelineRun{}).Error)
			case "run-project":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("project_id", "").Error)
			case "case-alias":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("pipeline_id", "PIPELINE").Error)

			}
			require.Error(t, executionTransaction(ctx, db, "job", func(tx *gorm.DB) error { _, err := lockExecutionJob(ctx, tx, "job", executionLockActive); return err }))
		})
	}
}
func TestExecutionScopeMissingProjectIsNotMissingLegacyParent(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("runtime_type", "container").Error)
	require.NoError(t, db.Where("project_id = ?", "project").Delete(&model.Project{}).Error)
	r := &StepRunRepo{IDatabase: statusTestDB{db}}
	require.Error(t, r.ValidateRuntimeStepClaim(ctx, &model.StepRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run"}, "claim", model.JobRunStatusRunning))
	require.NoError(t, r.ValidateRuntimeStepClaim(ctx, &model.StepRun{JobRunID: "absent"}, "claim", model.JobRunStatusRunning))
}
func TestExecutionScopeHistoricalAndLegacyModesCannotReadSharedContext(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.Where("pipeline_id = ?", "pipeline").Delete(&model.Pipeline{}).Error)
	for _, mode := range []executionLockMode{executionLockLegacy, executionLockInspect, executionLockTerminalReceipt} {
		if mode == executionLockTerminalReceipt {
			require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", model.JobRunStatusFailed).Error)
		}
		require.NoError(t, executionTransaction(ctx, db, "job", func(tx *gorm.DB) error {
			job, err := lockExecutionJob(ctx, tx, "job", mode)
			require.NoError(t, err)
			require.Error(t, requireExecutionScope(tx, job))
			_, err = retryApprovalContextRead(context.Background(), tx, job, true)
			require.Error(t, err)
			return nil
		}))
	}
}

func TestExecutionScopeCapturedEmptyProjectAndInactiveIdentity(t *testing.T) {
	f := gatedPinFixture(t)
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("project_id", "").Error)
	// The prefix protects identities only. Current authorization is checked later.
	require.NoError(t, f.db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Updates(map[string]any{"is_enabled": 0, "is_deleted": 1}).Error)
	require.NoError(t, executionTransaction(f.ctx, f.db, "job", func(tx *gorm.DB) error {
		job, err := lockExecutionJob(f.ctx, tx, "job", executionLockActive)
		require.NoError(t, err)
		return requireExecutionScope(tx, job)
	}))
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("retry_approval_context_digest", "invalid").Error)
	require.Error(t, executionTransaction(f.ctx, f.db, "job", func(tx *gorm.DB) error { _, err := lockExecutionJob(f.ctx, tx, "job", executionLockActive); return err }))
}
func TestExecutionScopeRejectsChangedPrefetchedParent(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.Error(t, executionTransaction(ctx, db, "job", func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("pipeline_run_id", "replacement").Error)
		_, err := lockExecutionJob(ctx, tx, "job", executionLockActive)
		return err
	}))
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", "job").First(&job).Error)
	require.Equal(t, "run", job.PipelineRunID)
}

func TestExecutionScopeMissingParentHintCannotBypassNewModernParent(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	err := executionTransaction(ctx, db, "late-job", func(tx *gorm.DB) error {
		require.NoError(t, tx.Create(&model.JobRun{JobRunID: "late-job", PipelineID: "pipeline", PipelineRunID: "run", RuntimeType: "container", Status: model.JobRunStatusRunning}).Error)
		return validateRuntimeStepClaim(ctx, tx, &model.StepRun{JobRunID: "late-job", PipelineID: "pipeline", PipelineRunID: "run"}, "claim", model.JobRunStatusRunning)
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, errExecutionParentMissing)
}

func TestExecutionScopeRejectsNestedTransactionPrefetch(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		called := false
		err := executionTransaction(ctx, tx, "job", func(*gorm.DB) error { called = true; return nil })
		require.Error(t, err)
		require.False(t, called)
		return nil
	}))
}

func TestExecutionScopeNonpendingFinishReceiptSurvivesDeletedScope(t *testing.T) {
	f := gatedPinFixture(t)
	f.admission(t)
	f.pin(t)
	approvals, a := retryApprovalTestRow(t, f)
	require.NoError(t, f.db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", a.ApprovalID).Update("status", model.ApprovalStatusApproved).Error)
	require.NoError(t, f.db.Where("pipeline_id = ?", "pipeline").Delete(&model.Pipeline{}).Error)
	for _, cancel := range []bool{false, true} {
		var got *model.ApprovalRequest
		var err error
		if cancel {
			got, err = approvals.Cancel(f.ctx, a.ApprovalID, "late cancellation")
		} else {
			got, err = approvals.Expire(f.ctx, a.ApprovalID)
		}
		require.NoError(t, err)
		require.Equal(t, model.ApprovalStatusApproved, got.Status)
	}
}
func TestExecutionScopeStaleNonpendingFinishCannotEnterPendingPath(t *testing.T) {
	f := gatedPinFixture(t)
	f.admission(t)
	f.pin(t)
	approvals, a := retryApprovalTestRow(t, f)
	require.NoError(t, f.db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", a.ApprovalID).Update("status", model.ApprovalStatusApproved).Error)
	changed := false
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register("stale-approval-locator", func(q *gorm.DB) {
		if changed || q.Statement.Table != "approval_request" {
			return
		}
		if _, locked := q.Statement.Clauses["FOR"]; locked {
			return
		}
		changed = true
		require.NoError(t, f.db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", a.ApprovalID).Update("status", model.ApprovalStatusPending).Error)
	}))
	_, err := approvals.Cancel(f.ctx, a.ApprovalID, "must not cancel")
	require.True(t, changed)
	require.ErrorIs(t, err, ErrApprovalConflict)
	var current model.ApprovalRequest
	require.NoError(t, f.db.Where("approval_id = ?", a.ApprovalID).First(&current).Error)
	require.Equal(t, model.ApprovalStatusPending, current.Status)
}

func TestExecutionScopeHistoricalVoteReceiptSurvivesDeletedScopeAndRevocation(t *testing.T) {
	f := gatedPinFixture(t)
	f.admission(t)
	f.pin(t)
	installRetryApprovalTestAuthority(t, f, nil)
	approvals, a := retryApprovalTestRow(t, f)
	_, err := approvals.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, "original")
	require.NoError(t, err)
	require.NoError(t, f.db.Where("pipeline_id = ?", "pipeline").Delete(&model.Pipeline{}).Error)
	require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error)
	require.NoError(t, approvals.ValidateRetryApprovalDecision(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove))
	got, err := approvals.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, "repeat")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusApproved, got.Status)
	_, err = approvals.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionReject, "different")
	require.Error(t, err)
	var count int64
	require.NoError(t, f.db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", a.ApprovalID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
func TestExecutionScopeStaleNonpendingVoteCannotCreateDecision(t *testing.T) {
	f := gatedPinFixture(t)
	f.admission(t)
	f.pin(t)
	approvals, a := retryApprovalTestRow(t, f)
	hint := *a
	hint.Status = model.ApprovalStatusApproved
	_, err := approvals.decideRetryApproval(f.ctx, &hint, "voter", model.ApprovalDecisionApprove, "must not vote")
	require.ErrorIs(t, err, ErrApprovalConflict)
	var count int64
	require.NoError(t, f.db.Model(&model.ApprovalDecision{}).Where("approval_id = ?", a.ApprovalID).Count(&count).Error)
	require.Zero(t, count)
}

func TestExecutionScopeLegacyOrganizationSourceCapture(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-absent", true: "present"}[present], func(t *testing.T) {
			ctx, db, jobs, binding := sourceTestRepository(t)
			require.NoError(t, db.AutoMigrate(&model.Organization{}))
			require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("org_id", "ci-acceptance-org").Error)
			if present {
				require.NoError(t, db.Create(&model.Organization{OrgID: "ci-acceptance-org"}).Error)
			}
			require.NoError(t, jobs.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", binding))
			job, err := jobs.GetByJobRunID(ctx, "job")
			require.NoError(t, err)
			require.True(t, job.SourceExpectationCaptured)
			require.Equal(t, "agent", job.AgentID)
		})
	}
}
func TestExecutionScopeOrganizationAliasesAndAmbiguityFailClosed(t *testing.T) {
	for _, ids := range [][]string{{"ORG"}, {"org", "org"}} {
		t.Run(ids[0]+string(rune('0'+len(ids))), func(t *testing.T) {
			ctx, db, jobs, binding := sourceTestRepository(t)
			require.NoError(t, db.Exec("CREATE TABLE organization (org_id TEXT COLLATE NOCASE)").Error)
			for _, id := range ids {
				require.NoError(t, db.Exec("INSERT INTO organization (org_id) VALUES (?)", id).Error)
			}
			require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Update("org_id", "org").Error)
			require.EqualError(t, jobs.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", binding), "execution organization identity changed")
			job, err := jobs.GetByJobRunID(ctx, "job")
			require.NoError(t, err)
			require.False(t, job.SourceExpectationCaptured)
			require.Empty(t, job.AgentID)
		})
	}
}
func TestExecutionScopeLocatorReadsUsePrimary(t *testing.T) {
	ctx, db, jobs, binding := sourceTestRepository(t)
	// The replica intentionally has no tables. A locator must not read it.
	require.NoError(t, db.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(filepath.Join(t.TempDir(), "empty-replica.db"))}})))
	require.NoError(t, jobs.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", binding))
}
