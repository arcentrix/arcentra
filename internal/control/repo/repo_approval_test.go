package repo

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type approvalTestDatabase struct{ db *gorm.DB }

func (d approvalTestDatabase) Database() *gorm.DB { return d.db }
func approvalDatabase(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=10000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}
func approvalFixture(t *testing.T) (*gorm.DB, IApprovalRepository, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "approvals.db")
	db := approvalDatabase(t, path)
	require.NoError(t, db.WithContext(context.Background()).AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.DeploymentRelease{}, &model.DeploymentEnvironment{}, &model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}))
	return db, NewApprovalRepo(approvalTestDatabase{db}), path
}
func approvalRecord(id string, quorum int) *model.ApprovalRequest {
	expires := time.Now().UTC().Add(time.Hour)
	return &model.ApprovalRequest{ApprovalID: id, PipelineRunID: "run", Mode: "any", RequiredApproverCount: quorum, CandidateApprovers: `["alice","bob"]`, RequestedBy: "requester", ExpiresAt: &expires}
}
func approvalVotes(t *testing.T, db *gorm.DB, id string) []model.ApprovalDecision {
	t.Helper()
	var values []model.ApprovalDecision
	require.NoError(t, db.WithContext(context.Background()).Select(approvalDecisionColumns).Where("approval_id = ?", id).Find(&values).Error)
	return values
}
func TestApprovalCandidateAndImmutableSnapshot(t *testing.T) {
	db, r, _ := approvalFixture(t)
	ctx := context.Background()
	for _, candidates := range []string{"", `null`, `[]`, `["alice","alice"]`, `["requester"]`, `[" "]`} {
		value := approvalRecord("invalid", 1)
		value.CandidateApprovers = candidates
		require.ErrorIs(t, r.Create(ctx, value), ErrApprovalInvalid)
	}
	require.NoError(t, r.Create(ctx, approvalRecord("gate", 2)))
	for _, actor := range []string{"", "system", "requester", "outsider"} {
		_, err := r.Decide(ctx, "gate", actor, model.ApprovalDecisionApprove, "")
		require.Error(t, err)
	}
	require.Empty(t, approvalVotes(t, db, "gate"))
	for _, patch := range []map[string]any{{"status": model.ApprovalStatusApproved}, {"status": model.ApprovalStatusCanceled, "candidate_approvers": `["outsider"]`}, {"approved_count": 5}} {
		require.ErrorIs(t, r.UpdateByApprovalID(ctx, "gate", patch), ErrApprovalInvalid)
	}
	value, err := r.Decide(ctx, "gate", "alice", model.ApprovalDecisionApprove, "first")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusPending, value.Status)
	require.Equal(t, 1, value.ApprovedCount)
	// 相同方向重试不会改写原始审计理由
	value, err = r.Decide(ctx, "gate", "alice", model.ApprovalDecisionApprove, "changed")
	require.NoError(t, err)
	require.Equal(t, 1, value.ApprovedCount)
	require.Equal(t, "first", approvalVotes(t, db, "gate")[0].Comment)
	_, err = r.Decide(ctx, "gate", "alice", model.ApprovalDecisionReject, "")
	require.ErrorIs(t, err, ErrApprovalConflict)
}

func TestApprovalConcurrentVotesAcrossDatabaseConnectionsAndRestart(t *testing.T) {
	db, first, path := approvalFixture(t)
	secondDB := approvalDatabase(t, path)
	second := NewApprovalRepo(approvalTestDatabase{secondDB})
	ctx := context.Background()
	require.NoError(t, first.Create(ctx, approvalRecord("gate", 2)))
	start := make(chan struct{})
	results := make(chan error, 16)
	var work sync.WaitGroup
	for i := 0; i < 16; i++ {
		actor, r := "alice", first
		if i%2 == 1 {
			actor, r = "bob", second
		}
		work.Add(1)
		safe.Go(func() {
			defer work.Done()
			<-start
			_, err := r.Decide(ctx, "gate", actor, model.ApprovalDecisionApprove, "ok")
			results <- err
		})
	}
	close(start)
	work.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Len(t, approvalVotes(t, db, "gate"), 2)
	sqlDB, _ := db.DB()
	require.NoError(t, sqlDB.Close())
	sqlDB, _ = secondDB.DB()
	require.NoError(t, sqlDB.Close())
	restartedDB := approvalDatabase(t, path)
	restarted := NewApprovalRepo(approvalTestDatabase{restartedDB})
	value, err := restarted.GetByApprovalID(ctx, "gate")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusApproved, value.Status)
	require.Equal(t, 2, value.ApprovedCount)
	require.EqualValues(t, 3, value.Version)
	replay, err := restarted.Decide(ctx, "gate", "alice", model.ApprovalDecisionApprove, "retry after restart")
	require.NoError(t, err)
	require.Equal(t, value.Version, replay.Version)
	require.Len(t, approvalVotes(t, restartedDB, "gate"), 2)
	// 数据库约束本身也阻止同用户重复投票
	require.Error(t, restartedDB.WithContext(ctx).Create(&model.ApprovalDecision{DecisionID: "extra", ApprovalID: "gate", UserID: "alice", Decision: model.ApprovalDecisionReject}).Error)
}

func TestApprovalOpposingVoteAndCancelRaces(t *testing.T) {
	db, r, path := approvalFixture(t)
	other := NewApprovalRepo(approvalTestDatabase{approvalDatabase(t, path)})
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, approvalRecord("opposing", 1)))
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, direction := range []string{model.ApprovalDecisionApprove, model.ApprovalDecisionReject} {
		safe.Go(func() { <-start; _, err := r.Decide(ctx, "opposing", "alice", direction, ""); results <- err })
	}
	close(start)
	first, second := <-results, <-results
	require.True(t, (first == nil && errors.Is(second, ErrApprovalConflict)) || (second == nil && errors.Is(first, ErrApprovalConflict)))
	require.Len(t, approvalVotes(t, db, "opposing"), 1)
	require.NoError(t, r.Create(ctx, approvalRecord("cancel", 1)))
	start = make(chan struct{})
	safe.Go(func() {
		<-start
		_, err := r.Decide(ctx, "cancel", "alice", model.ApprovalDecisionApprove, "")
		results <- err
	})
	safe.Go(func() { <-start; _, err := other.Cancel(ctx, "cancel", "run canceled"); results <- err })
	close(start)
	for i := 0; i < 2; i++ {
		err := <-results
		require.True(t, err == nil || errors.Is(err, ErrApprovalConflict), "%v", err)
	}
	value, err := r.GetByApprovalID(ctx, "cancel")
	require.NoError(t, err)
	votes := approvalVotes(t, db, "cancel")
	switch value.Status {
	case model.ApprovalStatusApproved:
		require.Len(t, votes, 1)
		require.Equal(t, 1, value.ApprovedCount)
	case model.ApprovalStatusCanceled:
		require.Empty(t, votes)
		require.Zero(t, value.ApprovedCount)
	default:
		t.Fatalf("nonterminal race result %+v", value)
	}
}

func TestApprovalExpiredVotesAndTerminalCAS(t *testing.T) {
	db, r, _ := approvalFixture(t)
	ctx := context.Background()
	for _, direction := range []string{model.ApprovalDecisionApprove, model.ApprovalDecisionReject} {
		value := approvalRecord(direction, 1)
		past := time.Now().Add(-time.Minute)
		value.ExpiresAt = &past
		require.NoError(t, r.Create(ctx, value))
		actual, err := r.Decide(ctx, value.ApprovalID, "alice", direction, "")
		require.ErrorIs(t, err, ErrApprovalExpired)
		require.Equal(t, model.ApprovalStatusExpired, actual.Status)
		require.Empty(t, approvalVotes(t, db, value.ApprovalID))
		actual, err = r.Cancel(ctx, value.ApprovalID, "cannot overwrite expiry")
		require.NoError(t, err)
		require.Equal(t, model.ApprovalStatusExpired, actual.Status)
	}
	require.NoError(t, r.Create(ctx, approvalRecord("pending", 2)))
	value, err := r.Expire(ctx, "pending")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusPending, value.Status)
	_, err = r.Decide(ctx, "pending", "alice", model.ApprovalDecisionApprove, "")
	require.NoError(t, err)
	_, err = r.Cancel(ctx, "pending", "stop")
	require.NoError(t, err)
	value, err = r.Decide(ctx, "pending", "alice", model.ApprovalDecisionApprove, "")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusCanceled, value.Status)
	require.Equal(t, 1, value.ApprovedCount)
}

func TestApprovalTransactionRollsBackInsertedVote(t *testing.T) {
	db, r, _ := approvalFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, approvalRecord("gate", 1)))
	injected := errors.New("injected aggregate write failure")
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("approval:fail_aggregate", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Model.(*model.ApprovalRequest); !ok {
			return
		}
		if patch, ok := tx.Statement.Dest.(map[string]any); ok && patch["approved_count"] != nil {
			tx.AddError(injected)
		}
	}))
	_, err := r.Decide(ctx, "gate", "alice", model.ApprovalDecisionApprove, "")
	require.ErrorIs(t, err, injected)
	require.Empty(t, approvalVotes(t, db, "gate"))
	value, err := r.GetByApprovalID(ctx, "gate")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusPending, value.Status)
	require.Zero(t, value.ApprovedCount)
	require.EqualValues(t, 1, value.Version)
	require.NoError(t, db.Callback().Update().Remove("approval:fail_aggregate"))
	_, err = r.Decide(ctx, "gate", "alice", model.ApprovalDecisionApprove, "")
	require.NoError(t, err)
}

func TestApprovalBoundReleaseRequiresLiveExactOwnership(t *testing.T) {
	cases := []string{"valid", "run-canceled", "run-other", "release-other", "evidence-other", "reservation-other", "step-other", "project-other"}
	for _, change := range cases {
		t.Run(change, func(t *testing.T) {
			db, r, _ := approvalFixture(t)
			ctx := context.Background()
			stepID, approvalID, releaseID := "step", "gate", "release"
			req := approvalRecord(approvalID, 1)
			req.DeploymentReleaseID = &releaseID
			req.EvidenceHash = "evidence"
			req.Environment = "env"
			records := []any{
				&model.Project{ProjectID: "project"},
				&model.Pipeline{PipelineID: "pipeline", ProjectID: "project"},
				&model.PipelineRun{RunID: "run", PipelineID: "pipeline", Status: model.PipelineStatusRunning, AllowDeployments: true, DeploymentActorID: "requester", DeploymentPlanHash: "plan", DeploymentTargetFingerprint: "target", DefinitionCommitSha: "commit"},
				&model.JobRun{JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline", Status: model.JobRunStatusRunning},
				&model.StepRun{StepRunID: stepID, JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline", Status: 3, DeploymentReleaseID: releaseID},
				&model.DeploymentEnvironment{EnvironmentID: "env", ProjectID: "project", ActiveReleaseID: releaseID},
				&model.DeploymentRelease{ReleaseID: releaseID, ProjectID: "project", RequestID: "request", Origin: "pipeline_job", PipelineID: "pipeline", PipelineRunID: "run", ExecutionJobRunID: "job", ExecutionStepRunID: &stepID, EnvironmentID: "env", Status: "queued", ApprovalID: &approvalID, ApprovalEvidenceHash: "evidence", CreatedBy: "requester", DeploymentPlanHash: "plan", TargetFingerprint: "target", CommitSHA: "commit"},
			}
			for _, record := range records {
				require.NoError(t, db.WithContext(ctx).Create(record).Error)
			}
			require.NoError(t, r.Create(ctx, req))
			var mutation *gorm.DB
			switch change {
			case "run-canceled":
				mutation = db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("status", model.PipelineStatusCancelled)
			case "run-other":
				mutation = db.Model(&model.DeploymentRelease{}).Where("release_id = ?", releaseID).Update("pipeline_run_id", "other")
			case "release-other":
				mutation = db.Model(&model.DeploymentRelease{}).Where("release_id = ?", releaseID).Update("approval_id", "other")
			case "evidence-other":
				mutation = db.Model(&model.DeploymentRelease{}).Where("release_id = ?", releaseID).Update("approval_evidence_hash", "other")
			case "reservation-other":
				mutation = db.Model(&model.DeploymentEnvironment{}).Where("environment_id = ?", "env").Update("active_release_id", "other")
			case "step-other":
				mutation = db.Model(&model.StepRun{}).Where("step_run_id = ?", stepID).Update("deployment_release_id", "other")
			case "project-other":
				mutation = db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("project_id", "other")
			}
			if mutation != nil {
				require.NoError(t, mutation.Error)
			}
			_, err := r.Decide(ctx, approvalID, "alice", model.ApprovalDecisionApprove, "")
			if change == "valid" {
				require.NoError(t, err)
				require.Len(t, approvalVotes(t, db, approvalID), 1)
			} else {
				require.ErrorIs(t, err, ErrApprovalConflict)
				require.Empty(t, approvalVotes(t, db, approvalID))
			}
		})
	}
}

type legacyApprovalDecision struct {
	model.BaseModel
	DecisionID string `gorm:"column:decision_id;not null;uniqueIndex;type:varchar(64)"`
	ApprovalID string `gorm:"column:approval_id;not null;index:idx_approval_user,priority:1;type:varchar(64)"`
	UserID     string `gorm:"column:user_id;not null;index:idx_approval_user,priority:2;type:varchar(64)"`
	Decision   string `gorm:"column:decision;not null"`
	Comment    string `gorm:"column:comment;type:text"`
}

func (legacyApprovalDecision) TableName() string { return "approval_decision" }

func TestApprovalExplicitMigrationAddsUniqueIndexOrRejectsDuplicateHistory(t *testing.T) {
	for _, duplicates := range []bool{false, true} {
		name := "clean-history"
		if duplicates {
			name = "duplicate-history"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := approvalDatabase(t, filepath.Join(t.TempDir(), "upgrade.db"))
			require.NoError(t, db.WithContext(ctx).AutoMigrate(&legacyApprovalDecision{}))
			require.NoError(t, db.WithContext(ctx).Create(&legacyApprovalDecision{DecisionID: "first", ApprovalID: "gate", UserID: "alice", Decision: model.ApprovalDecisionApprove}).Error)
			if duplicates {
				require.NoError(t, db.WithContext(ctx).Create(&legacyApprovalDecision{DecisionID: "second", ApprovalID: "gate", UserID: "alice", Decision: model.ApprovalDecisionReject}).Error)
			}
			require.True(t, db.WithContext(ctx).Migrator().HasIndex(&legacyApprovalDecision{}, "idx_approval_user"))
			err := db.WithContext(ctx).AutoMigrate(&model.ApprovalDecision{})
			var votes []model.ApprovalDecision
			require.NoError(t, db.WithContext(ctx).Select(approvalDecisionColumns).Where("approval_id = ?", "gate").Find(&votes).Error)
			if duplicates {
				require.Error(t, err, "migration must not silently accept or rewrite duplicate vote evidence")
				require.Len(t, votes, 2)
			} else {
				require.NoError(t, err)
				require.Len(t, votes, 1)
				require.True(t, db.WithContext(ctx).Migrator().HasIndex(&model.ApprovalDecision{}, "uk_approval_decision_user"))
				require.Error(t, db.WithContext(ctx).Create(&model.ApprovalDecision{DecisionID: "second", ApprovalID: "gate", UserID: "alice", Decision: model.ApprovalDecisionReject}).Error)
				require.NoError(t, db.WithContext(ctx).AutoMigrate(&model.ApprovalDecision{}))
			}
		})
	}
}

func TestApprovalExpiryRejectAndCancelRaceCannotInsertLateVote(t *testing.T) {
	db, r, path := approvalFixture(t)
	other := NewApprovalRepo(approvalTestDatabase{approvalDatabase(t, path)})
	ctx := context.Background()
	value := approvalRecord("race", 2)
	past := time.Now().Add(-time.Minute)
	value.ExpiresAt = &past
	require.NoError(t, r.Create(ctx, value))
	start := make(chan struct{})
	results := make(chan error, 4)
	actions := []func() error{
		func() error { _, err := r.Expire(ctx, "race"); return err },
		func() error { _, err := other.Cancel(ctx, "race", "cancelled run"); return err },
		func() error { _, err := r.Decide(ctx, "race", "alice", model.ApprovalDecisionApprove, ""); return err },
		func() error { _, err := other.Decide(ctx, "race", "bob", model.ApprovalDecisionReject, ""); return err },
	}
	for _, action := range actions {
		safe.Go(func() { <-start; results <- action() })
	}
	close(start)
	for range actions {
		err := <-results
		require.True(t, err == nil || errors.Is(err, ErrApprovalConflict) || errors.Is(err, ErrApprovalExpired), "%v", err)
	}
	actual, err := r.GetByApprovalID(ctx, "race")
	require.NoError(t, err)
	require.True(t, actual.Status == model.ApprovalStatusExpired || actual.Status == model.ApprovalStatusCanceled)
	require.Empty(t, approvalVotes(t, db, "race"))
	require.Zero(t, actual.ApprovedCount)
	require.Zero(t, actual.RejectedCount)
}

func TestApprovalConcurrentDistinctVoterRejectRemainsTerminal(t *testing.T) {
	db, r, path := approvalFixture(t)
	other := NewApprovalRepo(approvalTestDatabase{approvalDatabase(t, path)})
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, approvalRecord("gate", 2)))
	start := make(chan struct{})
	results := make(chan error, 2)
	safe.Go(func() {
		<-start
		_, err := r.Decide(ctx, "gate", "alice", model.ApprovalDecisionApprove, "")
		results <- err
	})
	safe.Go(func() {
		<-start
		_, err := other.Decide(ctx, "gate", "bob", model.ApprovalDecisionReject, "")
		results <- err
	})
	close(start)
	for i := 0; i < 2; i++ {
		err := <-results
		require.True(t, err == nil || errors.Is(err, ErrApprovalConflict), "%v", err)
	}
	actual, err := r.GetByApprovalID(ctx, "gate")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusRejected, actual.Status)
	require.Equal(t, 1, actual.RejectedCount)
	require.Len(t, approvalVotes(t, db, "gate"), actual.ApprovedCount+1)
	// 既有拒绝投票重试返回原终态，不能重复计数
	_, err = r.Decide(ctx, "gate", "bob", model.ApprovalDecisionReject, "")
	require.NoError(t, err)
}

func TestApprovalExpiryDuringDatabaseWorkNeverCommitsLateDecision(t *testing.T) {
	for _, delayAt := range []string{"linked-lock", "vote-insert", "aggregate-write"} {
		t.Run(delayAt, func(t *testing.T) {
			db, r, _ := approvalFixture(t)
			ctx := context.Background()
			stepID, approvalID, releaseID := "step", "gate", "release"
			records := []any{
				&model.Project{ProjectID: "project"},
				&model.Pipeline{PipelineID: "pipeline", ProjectID: "project"},
				&model.PipelineRun{RunID: "run", PipelineID: "pipeline", Status: model.PipelineStatusRunning, AllowDeployments: true, DeploymentActorID: "requester", DeploymentPlanHash: "plan", DeploymentTargetFingerprint: "target", DefinitionCommitSha: "commit"},
				&model.JobRun{JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline", Status: model.JobRunStatusRunning},
				&model.StepRun{StepRunID: stepID, JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline", Status: 3, DeploymentReleaseID: releaseID},
				&model.DeploymentEnvironment{EnvironmentID: "env", ProjectID: "project", ActiveReleaseID: releaseID},
				&model.DeploymentRelease{ReleaseID: releaseID, ProjectID: "project", RequestID: "request", Origin: "pipeline_job", PipelineID: "pipeline", PipelineRunID: "run", ExecutionJobRunID: "job", ExecutionStepRunID: &stepID, EnvironmentID: "env", Status: "queued", ApprovalID: &approvalID, ApprovalEvidenceHash: "evidence", CreatedBy: "requester", DeploymentPlanHash: "plan", TargetFingerprint: "target", CommitSHA: "commit"},
			}
			for _, record := range records {
				require.NoError(t, db.WithContext(ctx).Create(record).Error)
			}
			deadline := time.Now().Add(time.Second)
			req := approvalRecord(approvalID, 1)
			req.DeploymentReleaseID = &releaseID
			req.Environment = "env"
			req.EvidenceHash = "evidence"
			req.ExpiresAt = &deadline
			require.NoError(t, r.Create(ctx, req))
			delayed := false
			delay := func() {
				if !delayed {
					delayed = true
					time.Sleep(time.Until(deadline) + 20*time.Millisecond)
				}
			}
			switch delayAt {
			case "linked-lock":
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("approval:delay_lock", func(tx *gorm.DB) {
					if tx.Statement.Table == "deployment_environment" {
						delay()
					}
				}))
			case "vote-insert":
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("approval:delay_vote", func(tx *gorm.DB) {
					if tx.Statement.Table == "approval_decision" {
						delay()
					}
				}))
			case "aggregate-write":
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("approval:delay_aggregate", func(tx *gorm.DB) {
					if patch, ok := tx.Statement.Dest.(map[string]any); ok && patch["approved_count"] != nil {
						delay()
					}
				}))
			}
			actual, err := r.Decide(ctx, approvalID, "alice", model.ApprovalDecisionApprove, "late")
			require.ErrorIs(t, err, ErrApprovalExpired)
			require.True(t, delayed, "regression did not exercise the delayed database operation")
			require.Equal(t, model.ApprovalStatusExpired, actual.Status)
			require.Empty(t, approvalVotes(t, db, approvalID))
			persisted, err := r.GetByApprovalID(ctx, approvalID)
			require.NoError(t, err)
			require.Equal(t, model.ApprovalStatusExpired, persisted.Status)
			require.Zero(t, persisted.ApprovedCount)
			require.Zero(t, persisted.RejectedCount)
			require.EqualValues(t, 2, persisted.Version)
		})
	}
}
