// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunAPIFixture(t *testing.T) (*CopilotRerunService, *gorm.DB, *repo.CopilotRerunScope, CopilotRerunPrepareRequest) {
	t.Helper()
	db, scope, snapshot, _, _, key, policy := rerunReviewFixture(t)
	scope.Run.TriggerType = int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)
	scope.Run.InboxOrigin = model.RunInboxOriginVerifiedManual
	scope.Run.InboxActorID = scope.Run.TriggeredBy
	scope.Job.ArtifactReferences = "{}"
	scope.Job.ArtifactURIs = "{}"
	for _, row := range []any{scope.Run, snapshot, scope.Job} {
		require.NoError(t, db.Create(row).Error)
	}
	require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: "approval:read"}).Error)
	svc, err := NewCopilotRerunService(repo.NewRepositories(pipelineServiceTestDB{db}, nil), policy, rerunSnapshotPolicy(), key)
	require.NoError(t, err)
	return svc, db, scope, CopilotRerunPrepareRequest{RequestID: "fixed-client-request-1234", OriginalJobRunID: scope.Job.JobRunID, SnapshotDigest: snapshot.BindingDigest}
}
func rerunAPIConfirm(in CopilotRerunIntent) CopilotRerunConfirmRequest {
	return CopilotRerunConfirmRequest{ExpectedVersion: in.Version, IntentDigest: in.IntentDigest, ReviewedRunContextDigest: in.ReviewedRunContextDigest, AcknowledgeNewExecution: true}
}
func TestCopilotRerunAPIReadPrepareRecoveryAndExactConfirm(t *testing.T) {
	s, db, scope, in := rerunAPIFixture(t)
	ctx := context.Background()
	actor := authz.Subject{UserID: "actor"}
	project := scope.Project.ProjectID
	var beforeRun model.PipelineRun
	var beforeJob model.JobRun
	require.NoError(t, db.Where("run_id = ?", scope.Run.RunID).First(&beforeRun).Error)
	require.NoError(t, db.Where("job_run_id = ?", scope.Job.JobRunID).First(&beforeJob).Error)
	for i := 0; i < 2; i++ {
		eligible, err := s.Eligibility(ctx, actor, project, in.OriginalJobRunID)
		require.NoError(t, err)
		require.True(t, eligible.Eligible)
		require.Equal(t, scope.Run.RunID, eligible.Review.OriginalRunID)
	}
	for _, row := range []any{&model.CopilotRerunIntent{}, &model.CopilotRerunSchedule{}, &model.CopilotRerunAdmissionGate{}} {
		var n int64
		require.NoError(t, db.Model(row).Count(&n).Error)
		require.Zero(t, n)
	}
	_, err := s.LookupRequest(ctx, actor, project, in.RequestID)
	require.ErrorIs(t, err, repo.ErrCopilotRerunNotFound)
	prepared, err := s.Prepare(ctx, actor, project, in)
	require.NoError(t, err)
	require.True(t, prepared.CanConfirm)
	require.Equal(t, "prepared", prepared.State)
	recovered, err := s.LookupRequest(ctx, actor, project, in.RequestID)
	require.NoError(t, err)
	require.Equal(t, prepared.IntentID, recovered.IntentID)
	require.Equal(t, prepared.NewRunID, recovered.NewRunID)
	replay, err := s.Prepare(ctx, actor, project, in)
	require.NoError(t, err)
	require.Equal(t, prepared.ExpiresAt, replay.ExpiresAt)
	require.Equal(t, prepared.NewJobRunID, replay.NewJobRunID)
	confirmation := rerunAPIConfirm(prepared)
	confirmed, err := s.Confirm(ctx, actor, project, prepared.IntentID, confirmation)
	require.NoError(t, err)
	require.Equal(t, "confirmed", confirmed.State)
	require.NotNil(t, confirmed.ConfirmedAt)
	require.False(t, confirmed.CanConfirm)
	again, err := s.Confirm(ctx, actor, project, prepared.IntentID, confirmation)
	require.NoError(t, err)
	require.Equal(t, confirmed.NewRunID, again.NewRunID)
	require.Equal(t, confirmed.NewJobRunID, again.NewJobRunID)
	require.Equal(t, confirmed.ConfirmedAt, again.ConfirmedAt)
	var afterRun model.PipelineRun
	var afterJob model.JobRun
	require.NoError(t, db.Where("run_id = ?", scope.Run.RunID).First(&afterRun).Error)
	require.NoError(t, db.Where("job_run_id = ?", scope.Job.JobRunID).First(&afterJob).Error)
	require.Equal(t, beforeRun, afterRun)
	require.Equal(t, beforeJob, afterJob)
	var target model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", confirmed.NewJobRunID).First(&target).Error)
	require.Equal(t, model.JobRunStatusPending, target.Status)
	require.Empty(t, target.ClaimID)
	require.Empty(t, target.RetryApprovalID)
	var n int64
	require.NoError(t, db.Model(&model.CopilotRerunSchedule{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
	raw, err := sonic.Marshal(confirmed)
	require.NoError(t, err)
	for _, private := range []string{"PayloadCiphertext", "payloadCiphertext", "nonce", "OLD", "never-copy", "commands", "ownerUserId", "stepIdentities", "originalEvidenceJSON"} {
		require.NotContains(t, string(raw), private)
	}
}
func TestCopilotRerunAPICancelAndBoundConsent(t *testing.T) {
	s, db, scope, in := rerunAPIFixture(t)
	ctx := context.Background()
	actor := authz.Subject{UserID: "actor"}
	project := scope.Project.ProjectID
	p, err := s.Prepare(ctx, actor, project, in)
	require.NoError(t, err)
	confirm := rerunAPIConfirm(p)
	confirm.AcknowledgeNewExecution = false
	_, err = s.Confirm(ctx, actor, project, p.IntentID, confirm)
	require.ErrorIs(t, err, repo.ErrCopilotRerunInvalid)
	confirm.AcknowledgeNewExecution = true
	confirm.ReviewedRunContextDigest = "v1:" + strings.Repeat("0", 64)
	_, err = s.Confirm(ctx, actor, project, p.IntentID, confirm)
	require.ErrorIs(t, err, repo.ErrCopilotRerunConflict)
	cancelled, err := s.CancelPrepared(ctx, actor, project, p.IntentID, CopilotRerunCancelRequest{p.Version, p.IntentDigest, p.ReviewedRunContextDigest})
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	_, err = s.Confirm(ctx, actor, project, p.IntentID, rerunAPIConfirm(p))
	require.ErrorIs(t, err, repo.ErrCopilotRerunConflict)
	var n int64
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", p.NewRunID).Count(&n).Error)
	require.Zero(t, n)
	require.False(t, cancelled.CancelNewExecutionAvailable)
}
func TestCopilotRerunAPIPermissionRevocationAndRedaction(t *testing.T) {
	for _, tc := range []string{"trigger", "read", "candidate", "credential", "scope"} {
		t.Run(tc, func(t *testing.T) {
			s, db, scope, in := rerunAPIFixture(t)
			ctx := context.Background()
			actor := authz.Subject{UserID: "actor"}
			project := scope.Project.ProjectID
			p, err := s.Prepare(ctx, actor, project, in)
			require.NoError(t, err)
			switch tc {
			case "trigger":
				require.NoError(t, db.Where("role_id = ? AND permission_id = ?", "deploy-role", "pipeline:trigger").Delete(&model.RolePermissionBinding{}).Error)
			case "read":
				require.NoError(t, db.Where("role_id = ? AND permission_id = ?", "deploy-role", "approval:read").Delete(&model.RolePermissionBinding{}).Error)
			case "candidate":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error)
			case "credential":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", project).Update("credential", "fixture.invalid").Error)
			case "scope":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", scope.Run.RunID).Update("project_id", "foreign").Error)
			}
			observed, err := s.Inspect(ctx, actor, project, p.IntentID)
			if tc == "trigger" || tc == "scope" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Nil(t, observed.Review)
				require.False(t, observed.CanConfirm)
			}
			_, err = s.Confirm(ctx, actor, project, p.IntentID, rerunAPIConfirm(p))
			require.Error(t, err)
			var n int64
			require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", p.NewRunID).Count(&n).Error)
			require.Zero(t, n)
		})
	}
}
func TestCopilotRerunAPILegacyAndForeignSource(t *testing.T) {
	for _, tc := range []string{"legacy", "active", "success", "multi-job", "cross-project", "missing", "outsider"} {
		t.Run(tc, func(t *testing.T) {
			s, db, scope, in := rerunAPIFixture(t)
			ctx := context.Background()
			actor := authz.Subject{UserID: "actor"}
			project := scope.Project.ProjectID
			job := in.OriginalJobRunID
			switch tc {
			case "legacy":
				require.NoError(t, db.Where("run_id = ?", scope.Run.RunID).Delete(&model.CopilotDefinitionSnapshot{}).Error)
			case "active":
				require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", scope.Run.RunID).Update("status", model.PipelineStatusRunning).Error)
			case "success":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", job).Update("status", model.JobRunStatusSuccess).Error)
			case "multi-job":
				require.NoError(t, db.Create(&model.JobRun{JobRunID: "other-job", PipelineRunID: scope.Run.RunID, PipelineID: scope.Pipeline.PipelineID}).Error)
			case "cross-project":
				project = "foreign"
			case "missing":
				job = "missing-job"
			case "outsider":
				actor.UserID = "outsider"
			}
			eligible, err := s.Eligibility(ctx, actor, project, job)
			if tc == "cross-project" || tc == "missing" || tc == "outsider" {
				require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
			} else {
				require.NoError(t, err)
				require.False(t, eligible.Eligible)
				require.Nil(t, eligible.Review)
			}
		})
	}
}

func TestCopilotRerunAPIInactiveScopeRecoveryDeniesWithoutChangingDurableTargets(t *testing.T) {
	for _, blocked := range []string{"project-archived", "project-disabled", "pipeline-disabled", "grant-revoked"} {
		for _, state := range []string{"prepared", "confirmed"} {
			t.Run(blocked+"/"+state, func(t *testing.T) {
				s, db, scope, in := rerunAPIFixture(t)
				ctx := context.Background()
				actor := authz.Subject{UserID: "actor"}
				project := scope.Project.ProjectID
				prepared, err := s.Prepare(ctx, actor, project, in)
				require.NoError(t, err)
				if state == "confirmed" {
					_, err = s.Confirm(ctx, actor, project, prepared.IntentID, rerunAPIConfirm(prepared))
					require.NoError(t, err)
				}
				capture := func() map[string][]map[string]any {
					t.Helper()
					rows := make(map[string][]map[string]any)
					for _, table := range []string{"copilot_rerun_intent", "copilot_rerun_schedule", "copilot_rerun_admission_gate", "pipeline_run", "job_run", "step_run", "approval_request", "approval_decision"} {
						var values []map[string]any
						require.NoError(t, db.Table(table).Order("id").Find(&values).Error)
						rows[table] = values
					}
					return rows
				}
				before := capture()
				switch blocked {
				case "project-archived":
					require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", project).Update("status", model.ProjectStatusArchived).Error)
				case "project-disabled":
					require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", project).Update("is_enabled", 0).Error)
				case "pipeline-disabled":
					require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", scope.Pipeline.PipelineID).Update("is_enabled", 0).Error)
				case "grant-revoked":
					require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
				}
				_, err = s.Inspect(ctx, actor, project, prepared.IntentID)
				require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
				_, err = s.LookupRequest(ctx, actor, project, in.RequestID)
				require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
				_, err = s.Prepare(ctx, actor, project, in)
				require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
				fresh := in
				fresh.RequestID = "different-request-1234"
				_, err = s.Prepare(ctx, actor, project, fresh)
				require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
				_, err = s.Confirm(ctx, actor, project, prepared.IntentID, rerunAPIConfirm(prepared))
				require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
				_, err = s.CancelPrepared(ctx, actor, project, prepared.IntentID, CopilotRerunCancelRequest{prepared.Version, prepared.IntentDigest, prepared.ReviewedRunContextDigest})
				require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
				// History denial neither loses fixed recovery identities nor creates,
				// cancels, dispatches, or approves any target or original execution.
				require.Equal(t, before, capture())
			})
		}
	}
}

func TestCopilotRerunAPIExpiryKeepsOnlyFixedRecovery(t *testing.T) {
	s, _, scope, in := rerunAPIFixture(t)
	ctx := context.Background()
	actor := authz.Subject{UserID: "actor"}
	project := scope.Project.ProjectID
	saved, err := s.repos.CopilotRerunPrepare(ctx, repo.CopilotRerunPrepareInput{OwnerUserID: "actor", ProjectID: project, OriginalJobRunID: in.OriginalJobRunID, RequestID: in.RequestID, SnapshotDigest: in.SnapshotDigest, ExpiresAt: time.Now().Add(150 * time.Millisecond)}, copilotRerunAuthorize, s.protectedReview)
	require.NoError(t, err)
	before, err := s.Inspect(ctx, actor, project, saved.IntentID)
	require.NoError(t, err)
	time.Sleep(time.Until(saved.ExpiresAt) + 10*time.Millisecond)
	expired, err := s.LookupRequest(ctx, actor, project, in.RequestID)
	require.NoError(t, err)
	require.Equal(t, "expired", expired.State)
	require.Nil(t, expired.Review)
	require.False(t, expired.CanConfirm)
	require.False(t, expired.CanCancelPrepared)
	replay, err := s.Prepare(ctx, actor, project, in)
	require.NoError(t, err)
	require.Equal(t, expired.ExpiresAt, replay.ExpiresAt)
	require.Equal(t, expired.NewRunID, replay.NewRunID)
	_, err = s.Confirm(ctx, actor, project, saved.IntentID, rerunAPIConfirm(before))
	require.ErrorIs(t, err, repo.ErrCopilotRerunExpired)
	_, err = s.CancelPrepared(ctx, actor, project, saved.IntentID, CopilotRerunCancelRequest{before.Version, before.IntentDigest, before.ReviewedRunContextDigest})
	require.ErrorIs(t, err, repo.ErrCopilotRerunExpired)
}
func TestCopilotRerunAPIUnsafeRepositoryMetadataWithholdsReview(t *testing.T) {
	for _, raw := range []string{"https://user:password@example.test/repo", "https://example.test/repo?token=secret", "https://example.test/repo#secret", "git@example.test:repo", "https://example.test/repo\nsecret", "git://user:password@example.test/repo", "git://example.test/repo?", "/tmp/repo?secret", "/tmp/repo\u0085secret", "/tmp/repo\u202esecret", "git://example.test/repo\u200bsecret"} {
		require.False(t, safeRerunRepository(raw))
	}
	require.True(t, safeRerunRepository("https://example.test/repo.git"))
	require.True(t, safeRerunRepository("git://git:9418/acceptance.git"))
	require.True(t, safeRerunRepository("/tmp/anonymous-source.git"))
	require.False(t, safeRerunSourceRepository("/tmp/anonymous-source.git"))
	require.False(t, safeRerunSourceRepository("ssh://example.test/repo"))
	for _, raw := range []string{"hidden\rname", "hidden\u0085name", "hidden\u202ename", string([]byte{0xff})} {
		require.False(t, rerunDisplayTextSafe(raw))
	}
}

// This projection test does not manufacture a persisted snapshot or expand
// authoritative schema/runtime eligibility. The base schema still rejects
// native/local capture even though the native Agent supports those transports.
func TestCopilotRerunAPIProjectionKeepsNativeAnonymousSourceIdentity(t *testing.T) {
	for _, source := range []string{"git://git:9418/acceptance.git", "/tmp/anonymous-source.git"} {
		t.Run(source, func(t *testing.T) {
			db, scope, snapshot, evidence, ids, key, policy := rerunReviewFixture(t)
			require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: "approval:read"}).Error)
			svc, err := NewCopilotRerunService(repo.NewRepositories(pipelineServiceTestDB{db}, nil), policy, rerunSnapshotPolicy(), key)
			require.NoError(t, err)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				view, finish := repo.NewScopedAuthorizationView(tx)
				reviewed, err := svc.review(context.Background(), view, scope, "actor", snapshot, evidence, ids)
				require.NoError(t, err)
				metadata, err := repo.CopilotRerunReviewRepositories(view)
				require.NoError(t, err)
				displayEvidence := *evidence
				displaySnapshot := *snapshot
				if strings.HasPrefix(source, "/") {
					displaySnapshot.DefinitionRepository = source
				} else {
					displayEvidence.Source.Repository = source
				}
				safe, err := svc.safeReview(context.Background(), metadata, scope, "actor", &displaySnapshot, &displayEvidence, reviewed)
				require.NoError(t, err)
				if strings.HasPrefix(source, "/") {
					require.Equal(t, source, safe.Definition.Repository)
				} else {
					require.Equal(t, source, safe.Source.Repository)
				}
				return finish(context.Background())
			}))
		})
	}
}
