// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type rerunDispatchPublisher struct {
	nova.IConsumer
	send func(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error)
}

func (p *rerunDispatchPublisher) EnqueueContext(c context.Context, t *nova.Task, o ...nova.Option) (*nova.Result, error) {
	return p.send(c, t, o...)
}
func dispatcherFixture(t *testing.T, f *rerunBridgeFixture, p *rerunDispatchPublisher, observe rerunDispatchObserver) *copilotRerunDispatcher {
	t.Helper()
	policy := &retryapproval.Policy{ID: "current-policy", Candidates: []string{"actor", "reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	d, err := newCopilotRerunDispatcher(f.repositories, p, policy, rerunSnapshotPolicy(), NewSecretService(nil), observe)
	require.NoError(t, err)
	return d
}
func releaseDispatchLease(t *testing.T, f *rerunBridgeFixture) {
	t.Helper()
	require.NoError(t, f.db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", f.saved.ScheduleID).Update("lease_until", time.Now().UTC().Add(-time.Second)).Error)
}
func TestRerunDispatcherUnknownPublishReplaysOnlyReference(t *testing.T) {
	f := newRerunBridgeFixture(t)
	releaseDispatchLease(t, f)
	var refs []taskqueue.RerunClaimReference
	p := &rerunDispatchPublisher{send: func(c context.Context, task *nova.Task, _ ...nova.Option) (*nova.Result, error) {
		require.Equal(t, taskqueue.TaskTypeCopilotRerunReference, task.Type)
		require.NotContains(t, string(task.Payload), "original-private-command")
		var ref taskqueue.RerunClaimReference
		require.NoError(t, json.Unmarshal(task.Payload, &ref))
		require.NoError(t, ref.Validate())
		refs = append(refs, ref)
		if len(refs) == 1 {
			return nil, errors.New("synthetic accepted but ACK lost")
		}
		return &nova.Result{ID: "notification-only"}, nil
	}}
	d := dispatcherFixture(t, f, p, func(context.Context, repo.CopilotRerunDispatchWork) error {
		t.Fatal("unclaimed row observed as claimed")
		return nil
	})
	work, err := f.repositories.CopilotRerunReadDispatchWork(context.Background(), f.saved.ScheduleID)
	require.NoError(t, err)
	require.ErrorIs(t, d.process(context.Background(), *work), errRerunDispatchUnknown)
	require.Len(t, refs, 1)
	// Active lease suppresses immediate retries; subsequent scan must inspect primary.
	require.Error(t, d.process(context.Background(), *work))
	require.Len(t, refs, 1)
	releaseDispatchLease(t, f)
	work, err = f.repositories.CopilotRerunReadDispatchWork(context.Background(), f.saved.ScheduleID)
	require.NoError(t, err)
	require.NoError(t, d.process(context.Background(), *work))
	require.Len(t, refs, 2)
	require.Equal(t, refs[0], refs[1])
	var job model.JobRun
	require.NoError(t, f.db.Where("job_run_id = ?", f.saved.NewJobRunID).First(&job).Error)
	require.Empty(t, job.ClaimID)
	require.Empty(t, job.InputManifest)
}
func TestRerunDispatcherClaimedObservationNeverPublishes(t *testing.T) {
	f := newRerunBridgeFixture(t)
	_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	observed := 0
	d := dispatcherFixture(t, f, &rerunDispatchPublisher{send: func(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error) {
		t.Fatal("claimed row republished")
		return nil, nil
	}}, func(_ context.Context, w repo.CopilotRerunDispatchWork) error {
		observed++
		require.Equal(t, f.request.ClaimID, w.ClaimID)
		return repo.ErrCopilotRerunConflict
	})
	work, err := f.repositories.CopilotRerunReadDispatchWork(context.Background(), f.saved.ScheduleID)
	require.NoError(t, err)
	require.Error(t, d.process(context.Background(), *work))
	require.Equal(t, 1, observed)
	var row model.CopilotRerunSchedule
	require.NoError(t, f.db.Where("schedule_id = ?", f.saved.ScheduleID).First(&row).Error)
	require.Equal(t, repo.CopilotRerunScheduleClaimed, row.State)
	require.Nil(t, row.CompletedAt)
}
func TestRerunDispatcherCancellationAndCurrentRevocation(t *testing.T) {
	for _, scenario := range []string{"cancelled", "owner-revoked", "publisher-panic"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRerunBridgeFixture(t)
			releaseDispatchLease(t, f)
			calls := 0
			d := dispatcherFixture(t, f, &rerunDispatchPublisher{send: func(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error) {
				calls++
				panic("private synthetic payload")
			}}, func(context.Context, repo.CopilotRerunDispatchWork) error { return nil })
			work, err := f.repositories.CopilotRerunReadDispatchWork(context.Background(), f.saved.ScheduleID)
			require.NoError(t, err)
			if scenario == "cancelled" {
				require.NoError(t, f.db.Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", f.saved.ScheduleID).Update("cancel_requested", true).Error)
			}
			if scenario == "owner-revoked" {
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
			}
			err = d.process(context.Background(), *work)
			require.ErrorIs(t, err, errRerunDispatchUnknown)
			require.NotContains(t, err.Error(), "private synthetic")
			if scenario == "publisher-panic" {
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls)
			}
		})
	}
}
func TestRerunDispatcherStopJoinsPublicationAndRequiresContextPublisher(t *testing.T) {
	f := newRerunBridgeFixture(t)
	releaseDispatchLease(t, f)
	policy := &retryapproval.Policy{ID: "current-policy", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	_, err := newCopilotRerunDispatcher(f.repositories, struct{ nova.IConsumer }{}, policy, rerunSnapshotPolicy(), NewSecretService(nil), func(context.Context, repo.CopilotRerunDispatchWork) error { return nil })
	require.Error(t, err)
	var absent *rerunDispatchPublisher
	_, err = newCopilotRerunDispatcher(f.repositories, absent, policy, rerunSnapshotPolicy(), NewSecretService(nil), func(context.Context, repo.CopilotRerunDispatchWork) error { return nil })
	require.Error(t, err)
	entered := make(chan struct{})
	exited := make(chan struct{})
	d := dispatcherFixture(t, f, &rerunDispatchPublisher{send: func(c context.Context, _ *nova.Task, _ ...nova.Option) (*nova.Result, error) {
		close(entered)
		<-c.Done()
		close(exited)
		return nil, c.Err()
	}}, func(context.Context, repo.CopilotRerunDispatchWork) error { return nil })
	d.cursor = repo.CopilotRerunDispatchCursor{Bucket: 3}
	require.NoError(t, d.Start(context.Background()))
	require.Error(t, d.Start(context.Background()))
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("publication not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, d.Stop(ctx))
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before publication drained")
	}
	var schedule model.CopilotRerunSchedule
	require.NoError(t, f.db.Where("schedule_id = ?", f.saved.ScheduleID).First(&schedule).Error)
	require.Equal(t, repo.CopilotRerunScheduleSealed, schedule.State)
	require.Empty(t, schedule.ClaimedID)
	require.Nil(t, schedule.CompletedAt)
}

func TestRerunDispatcherPendingSealAndFixedReference(t *testing.T) {
	testRerunDispatcherPending(t, false)
}
func TestRerunDispatcherUnknownEnqueueExpiresOnlyUnclaimed(t *testing.T) {
	testRerunDispatcherPending(t, true)
}
func testRerunDispatcherPending(t *testing.T, unknown bool) {
	t.Helper()
	ttl := 5 * time.Minute
	if unknown {
		ttl = time.Second
	}
	db, scope, snapshot, _, _, key, policy := rerunReviewFixture(t)
	scope.Run.TriggerType = 1
	scope.Run.InboxOrigin = model.RunInboxOriginVerifiedManual
	scope.Run.InboxActorID = scope.Run.TriggeredBy
	require.NoError(t, db.AutoMigrate(&model.CopilotDefinitionSnapshot{}, &model.CopilotRerunIntent{}, &model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}, &model.JobRunGeneration{}, &model.StepRunArtifact{}, &model.ProjectArtifact{}, &model.StepRunLog{}, &model.ApprovalRequest{}))
	for _, row := range []any{scope.Run, scope.Job, snapshot} {
		require.NoError(t, db.Create(row).Error)
	}
	r := repo.NewRepositories(rerunBridgeDatabase{db}, nil)
	authorize := func(c context.Context, v *repo.Repositories, s *repo.CopilotRerunScope, owner, action string) error {
		return authorizeRetryActor(c, v, owner, s.Project.ProjectID, s.Pipeline.PipelineID, "")
	}
	review, err := newCopilotRerunReviewBuilder(policy, rerunSnapshotPolicy(), key)
	require.NoError(t, err)
	saved, err := r.CopilotRerunPrepare(context.Background(), repo.CopilotRerunPrepareInput{OwnerUserID: "actor", ProjectID: scope.Project.ProjectID, OriginalJobRunID: scope.Job.JobRunID, RequestID: uuid.NewString(), SnapshotDigest: snapshot.BindingDigest, ExpiresAt: time.Now().Add(ttl)}, authorize, review)
	require.NoError(t, err)
	saved, err = r.CopilotRerunConfirm(context.Background(), repo.CopilotRerunConfirmInput{OwnerUserID: saved.OwnerUserID, ProjectID: saved.ProjectID, IntentID: saved.IntentID, ExpectedVersion: saved.Version, ExpectedIntentDigest: saved.IntentDigest, ExpectedReviewedRunContextDigest: saved.ReviewedRunContextDigest}, authorize, review)
	require.NoError(t, err)
	now := time.Now()
	caps, err := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Agent{AgentID: "dispatch-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}).Error)
	var ref taskqueue.RerunClaimReference
	calls := 0
	publisher := &rerunDispatchPublisher{send: func(c context.Context, task *nova.Task, _ ...nova.Option) (*nova.Result, error) {
		calls++
		require.NoError(t, json.Unmarshal(task.Payload, &ref))
		if unknown {
			return nil, errors.New("synthetic accepted but ACK unknown")
		}
		return &nova.Result{ID: "notification"}, nil
	}}
	d, err := newCopilotRerunDispatcher(r, publisher, policy, rerunSnapshotPolicy(), key, func(context.Context, repo.CopilotRerunDispatchWork) error { return nil })
	require.NoError(t, err)
	if unknown {
		require.Error(t, d.tick(context.Background()))
	} else {
		require.NoError(t, d.tick(context.Background()))
	}
	require.Equal(t, 1, calls)
	require.Equal(t, saved.NewRunID, ref.RunID)
	var schedule model.CopilotRerunSchedule
	require.NoError(t, db.Where("schedule_id = ?", saved.ScheduleID).First(&schedule).Error)
	require.Equal(t, repo.CopilotRerunScheduleSealed, schedule.State)
	require.Equal(t, *schedule.SealID, ref.SealID)
	require.Equal(t, schedule.PayloadDigest, ref.PayloadDigest)
	require.Empty(t, schedule.ClaimedID)
	require.NotContains(t, string(schedule.PayloadCiphertext), "original-private-command")
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&job).Error)
	require.Empty(t, job.InputManifest)
	require.Empty(t, job.ClaimID)
	require.Empty(t, job.RetryApprovalID)
	if unknown {
		time.Sleep(time.Until(saved.ExpiresAt) + time.Millisecond)
		work, err := r.CopilotRerunReadDispatchWork(context.Background(), saved.ScheduleID)
		require.NoError(t, err)
		require.NoError(t, d.process(context.Background(), *work))
		require.Equal(t, 1, calls)
		var closed model.JobRun
		require.NoError(t, db.Where("job_run_id = ?", saved.NewJobRunID).First(&closed).Error)
		require.Equal(t, model.JobRunStatusCancelled, closed.Status)
		require.Equal(t, repo.CopilotRerunExpiredBeforeClaimReason, closed.ErrorMessage)
		require.Empty(t, closed.ClaimID)
	}
}
