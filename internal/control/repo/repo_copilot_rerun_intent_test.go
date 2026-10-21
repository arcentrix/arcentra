// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func permitRerun(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
	return nil
}
func rerunIntentFixture(t *testing.T) (*Repositories, CopilotRerunPrepareInput) {
	t.Helper()
	r, run, snapshot := rerunCaptureFixture(t)
	db := r.Database()
	require.NoError(t, db.AutoMigrate(&model.CopilotRerunIntent{}, &model.StepRun{}))
	require.NoError(t, r.CreateVerifiedManualTriggerRunWithDefinition(context.Background(), "actor", run, snapshot, permitManualInbox))
	_, terminal, job, _ := rerunTerminalEvidenceFixture(t)
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Updates(map[string]any{"status": terminal.Status, "end_time": terminal.EndTime, "project_id": "project"}).Error)
	job.ArtifactReferences = "{}"
	job.ArtifactURIs = "{}"
	require.NoError(t, db.Create(job).Error)
	return NewRepositories(statusTestDB{db}, nil), CopilotRerunPrepareInput{OwnerUserID: "actor", ProjectID: "project", OriginalJobRunID: job.JobRunID, RequestID: "explicit-request", SnapshotDigest: snapshot.BindingDigest, ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Truncate(time.Millisecond)}
}
func TestCopilotRerunPrepareAllocatesStablePrivateIdentitiesOnly(t *testing.T) {
	repositories, in := rerunIntentFixture(t)
	ctx := context.Background()
	first, err := testRerunPrepare(repositories, ctx, in, permitRerun)
	require.NoError(t, err)
	require.Equal(t, CopilotRerunPrepared, first.State)
	require.EqualValues(t, 1, first.Version)
	second, err := testRerunPrepare(repositories, ctx, in, permitRerun)
	require.NoError(t, err)
	require.Equal(t, first.IntentID, second.IntentID)
	require.Equal(t, first.IntentDigest, second.IntentDigest)
	require.Equal(t, first.StepIdentitiesJSON, second.StepIdentitiesJSON)
	require.NotContains(t, first.OriginalEvidenceJSON, "MUST_NOT_COPY")
	require.NotContains(t, first.StepIdentitiesJSON, "MUST_NOT_COPY")
	var ids CopilotRerunExecutionIdentities
	require.NoError(t, json.Unmarshal([]byte(first.StepIdentitiesJSON), &ids))
	require.True(t, rerunUniqueUUIDs(ids.RunID, ids.JobRunID, ids.ScheduleID, ids.Steps[0].Attempts[0], ids.Steps[0].Attempts[1]))
	require.NotEqual(t, "run", ids.RunID)
	require.NotEqual(t, "job", ids.JobRunID)
	for _, entity := range []any{&model.PipelineRun{}, &model.JobRun{}, &model.CopilotRerunIntent{}} {
		var count int64
		require.NoError(t, repositories.database.Database().Model(entity).Count(&count).Error)
		require.EqualValues(t, 1, count, "preparation does not create a new run or job")
	}
	var old model.JobRun
	require.NoError(t, repositories.database.Database().Where("job_run_id = ?", "job").First(&old).Error)
	require.Equal(t, model.JobRunStatusFailed, old.Status)
	require.Equal(t, `{"old":"MUST_NOT_COPY"}`, old.Env)
	changed := in
	changed.ExpiresAt = changed.ExpiresAt.Add(time.Second)
	_, err = testRerunPrepare(repositories, ctx, changed, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	_, err = testRerunPrepare(repositories, ctx, in, func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
		return errors.New("revoked")
	})
	require.ErrorIs(t, err, ErrCopilotRerunForbidden, "same-request receipt still needs current read/prepare authority")
}
func TestCopilotRerunPrepareFailsClosed(t *testing.T) {
	for _, scenario := range []string{"foreign-owner", "foreign-project", "revoked", "disabled-owner", "not-terminal", "multiple-jobs", "missing-snapshot", "changed-evidence", "wrong-snapshot", "expired", "too-long", "write-failure"} {
		t.Run(scenario, func(t *testing.T) {
			repositories, in := rerunIntentFixture(t)
			db := repositories.database.Database()
			authorize := CopilotRerunAuthorize(permitRerun)
			switch scenario {
			case "foreign-owner":
				in.OwnerUserID = "other"
			case "foreign-project":
				in.ProjectID = "other"
			case "revoked":
				authorize = func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
					return errors.New("revoked")
				}
			case "disabled-owner":
				require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error)
			case "not-terminal":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", model.JobRunStatusRunning).Error)
			case "multiple-jobs":
				require.NoError(t, db.Create(&model.JobRun{JobRunID: "sibling", PipelineID: "pipeline", PipelineRunID: "run", Env: "{}", ArtifactReferences: "{}", ArtifactURIs: "{}"}).Error)
			case "missing-snapshot":
				require.NoError(t, db.Where("run_id = ?", "run").Delete(&model.CopilotDefinitionSnapshot{}).Error)
			case "changed-evidence":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("input_manifest_digest", "changed").Error)
			case "wrong-snapshot":
				in.SnapshotDigest = "v1:0000000000000000000000000000000000000000000000000000000000000000"
			case "expired":
				in.ExpiresAt = time.Now().Add(-time.Second)
			case "too-long":
				in.ExpiresAt = time.Now().Add(time.Hour)
			case "write-failure":
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("reject_intent", func(tx *gorm.DB) {
					if tx.Statement.Table == "copilot_rerun_intent" {
						tx.AddError(errors.New("synthetic storage failure"))
					}
				}))
			}
			result, err := testRerunPrepare(repositories, context.Background(), in, authorize)
			require.Error(t, err)
			require.Nil(t, result)
			require.NotContains(t, err.Error(), "synthetic storage failure")
			var count int64
			require.NoError(t, db.Model(&model.CopilotRerunIntent{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

// SQLite proves receipt semantics only. Scope/request-key contention and unique
// constraint races require the separate real-MySQL two-collation acceptance gate.
func TestCopilotRerunPrepareExpiredRequestIsOnlyAnAuthorizedReceipt(t *testing.T) {
	repositories, in := rerunIntentFixture(t)
	ctx := context.Background()
	in.ExpiresAt = time.Now().Add(500 * time.Millisecond).UTC().Truncate(time.Millisecond)
	first, err := testRerunPrepare(repositories, ctx, in, permitRerun)
	require.NoError(t, err)
	time.Sleep(time.Until(in.ExpiresAt) + time.Millisecond)
	replayed, err := testRerunPrepare(repositories, ctx, in, permitRerun)
	require.NoError(t, err)
	require.Equal(t, first, replayed, "receipt must not renew expiry, version, state, or fixed identities")
	fresh := in
	fresh.RequestID = "different-request"
	_, err = testRerunPrepare(repositories, ctx, fresh, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunExpired)
	_, err = testRerunPrepare(repositories, ctx, in, func(context.Context, *Repositories, *CopilotRerunScope, string, string) error {
		return errors.New("revoked")
	})
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	require.NoError(t, repositories.database.Database().Model(&model.User{}).Where("user_id = ?", in.OwnerUserID).Update("is_enabled", 0).Error)
	_, err = testRerunPrepare(repositories, ctx, in, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunForbidden)
	for _, entity := range []any{&model.PipelineRun{}, &model.JobRun{}, &model.CopilotRerunIntent{}} {
		var count int64
		require.NoError(t, repositories.database.Database().Model(entity).Count(&count).Error)
		require.EqualValues(t, 1, count, "replay cannot create execution records")
	}
}

func TestCopilotRerunPrepareRequestKeyCannotMoveBetweenPipelines(t *testing.T) {
	r, in := rerunIntentFixture(t)
	ctx, db := context.Background(), r.database.Database()
	first, err := testRerunPrepare(r, ctx, in, permitRerun)
	require.NoError(t, err)
	var pipeline model.Pipeline
	var project model.Project
	require.NoError(t, db.Where("pipeline_id = ?", "pipeline").First(&pipeline).Error)
	require.NoError(t, db.Where("project_id = ?", "project").First(&project).Error)
	pipeline.ID, pipeline.PipelineID = 0, "second-pipeline"
	require.NoError(t, db.Create(&pipeline).Error)
	run, input, now := rerunValueFixture(t)
	run.RunID, run.PipelineID = "second-run", pipeline.PipelineID
	context, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	require.NoError(t, err)
	context.PipelineID, context.PipelineRunID = run.PipelineID, run.RunID
	run.RetryApprovalContext, err = context.Canonical()
	require.NoError(t, err)
	run.RetryApprovalContextDigest, err = context.Digest()
	require.NoError(t, err)
	input.RepositoryConfigDigest, err = CopilotDefinitionRepositoryConfigDigest(&pipeline, &project)
	require.NoError(t, err)
	run.ProjectID, run.Branch, run.RequestID = "", "main", "second-capture"
	run.TotalJobs, run.TriggerType = 1, int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)
	snapshot, err := newTestCopilotDefinitionSnapshot(run, input, now)
	require.NoError(t, err)
	creator := r.Pipeline.(IVerifiedManualDefinitionCreator)
	require.NoError(t, creator.CreateVerifiedManualTriggerRunWithDefinition(ctx, "actor", run, snapshot, permitManualInbox))
	_, terminal, job, _ := rerunTerminalEvidenceFixture(t)
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Updates(map[string]any{"status": terminal.Status, "end_time": terminal.EndTime, "project_id": "project"}).Error)
	job.JobRunID, job.PipelineID, job.PipelineRunID = "second-job", run.PipelineID, run.RunID
	source, err := sourceprovenance.NewBinding(job.PipelineID, job.PipelineRunID, job.JobRunID, job.SourceType, job.SourceRepository, job.SourceRef)
	require.NoError(t, err)
	job.SourceBindingDigest = source.Digest
	runtime := repoRuntimeRequest(t)
	job.RuntimeBindingDigest, err = taskqueue.RuntimeBindingDigest(job.PipelineID, job.PipelineRunID, job.JobRunID, job.AgentID, runtime)
	require.NoError(t, err)
	plan, err := taskqueue.ParseJobRetryPlan(job.RetryPlan)
	require.NoError(t, err)
	job.RetryPlanDigest, err = plan.Digest(job.PipelineID, job.PipelineRunID, job.JobRunID, job.AgentID, job.RuntimeBindingDigest)
	require.NoError(t, err)
	manifest, err := taskqueue.ParseJobInputManifest(job.InputManifest)
	require.NoError(t, err)
	job.InputManifestDigest, err = manifest.Digest(generationIdentity(job))
	require.NoError(t, err)
	job.ArtifactReferences, job.ArtifactURIs = "{}", "{}"
	require.NoError(t, db.Create(job).Error)
	other := in
	other.OriginalJobRunID, other.SnapshotDigest = job.JobRunID, snapshot.BindingDigest
	_, err = testRerunPrepare(r, ctx, other, permitRerun)
	require.ErrorIs(t, err, ErrCopilotRerunConflict)
	other.RequestID = "second-independent-request"
	second, err := testRerunPrepare(r, ctx, other, permitRerun)
	require.NoError(t, err, "second source is independently eligible")
	require.NotEqual(t, first.IntentID, second.IntentID)
	replayed, err := testRerunPrepare(r, ctx, in, permitRerun)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
}

func testRerunPrepare(r *Repositories, ctx context.Context, in CopilotRerunPrepareInput, auth CopilotRerunAuthorize) (*model.CopilotRerunIntent, error) {
	return r.CopilotRerunPrepare(ctx, in, auth, testRerunReview)
}
func testRerunReview(ctx context.Context, repos *Repositories, scope *CopilotRerunScope, owner string, snapshot *model.CopilotDefinitionSnapshot, evidence *CopilotRerunOriginalEvidence, ids *CopilotRerunExecutionIdentities) (*CopilotRerunReviewedTarget, error) {
	reviewed, err := retryapproval.ParseRunContext(snapshot.RetryApprovalContext)
	if err != nil {
		return nil, err
	}
	reviewed.PipelineRunID, reviewed.ActorID = ids.RunID, owner
	body, err := reviewed.Canonical()
	if err != nil {
		return nil, err
	}
	digest, err := reviewed.Digest()
	if err != nil {
		return nil, err
	}
	layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
	if err != nil {
		return nil, err
	}
	return &CopilotRerunReviewedTarget{RunContext: body, RunContextDigest: digest, AdmissionPolicyDigest: snapshot.RuntimePolicyDigest, ExpectedSourceCommit: evidence.SourceCommit, Layout: layout}, nil
}
