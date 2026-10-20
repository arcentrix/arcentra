// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func addCancelOnlyActor(t *testing.T, db *gorm.DB, project string) {
	t.Helper()
	for _, row := range []any{&model.User{UserID: "cancel-actor", IsEnabled: 1}, &model.Role{RoleID: "cancel-role", Name: "cancel only", IsEnabled: 1}, &model.RolePermissionBinding{RoleID: "cancel-role", PermissionID: "pipeline:cancel"}, &model.RoleGrant{GrantID: "cancel-grant", SubjectType: "user", SubjectID: "cancel-actor", RoleID: "cancel-role", ScopeType: "project", ScopeID: project, IsEnabled: 1}} {
		require.NoError(t, db.Create(row).Error)
	}
}
func pendingCancellationFixture(t *testing.T) (*ProtectedRunCancellationService, *gorm.DB, *repo.CopilotRerunScope, CopilotRerunIntent) {
	t.Helper()
	api, db, scope, in := rerunAPIFixture(t)
	ctx := context.Background()
	actor := authz.Subject{UserID: "actor"}
	prepared, err := api.Prepare(ctx, actor, scope.Project.ProjectID, in)
	require.NoError(t, err)
	confirmed, err := api.Confirm(ctx, actor, scope.Project.ProjectID, prepared.IntentID, rerunAPIConfirm(prepared))
	require.NoError(t, err)
	addCancelOnlyActor(t, db, scope.Project.ProjectID)
	s, err := NewProtectedRunCancellationService(api.repos, nil)
	require.NoError(t, err)
	return s, db, scope, confirmed
}

type cancelNotifier struct {
	calls         []repo.CopilotRerunTerminalReceipt
	notifications []ProtectedCancellationNotification
	err           error
}

func (n *cancelNotifier) NotifyProtectedCancellation(_ context.Context, in ProtectedCancellationNotification) error {
	n.calls = append(n.calls, in.Receipt())
	n.notifications = append(n.notifications, in)
	return n.err
}

type cancellationFaults struct {
	protectedCancellationRepository
	beforePreclaim                               func()
	lostPreclaim, lostClaimed, errorWithoutWrite error
	preclaimCalls, claimedCalls, reads, locates  int
}

func (f *cancellationFaults) LocateCopilotRerunCancellation(ctx context.Context, p, r string) (*repo.CopilotRerunCancellationLocator, error) {
	f.locates++
	return f.protectedCancellationRepository.LocateCopilotRerunCancellation(ctx, p, r)
}
func (f *cancellationFaults) CopilotRerunCancelUnclaimedAs(ctx context.Context, actor string, in repo.CopilotRerunTargetCancelInput, a repo.CopilotRerunAuthorize) (*repo.CopilotRerunTargetCancelReceipt, error) {
	f.preclaimCalls++
	if f.beforePreclaim != nil {
		f.beforePreclaim()
	}
	if f.errorWithoutWrite != nil {
		return nil, f.errorWithoutWrite
	}
	out, err := f.protectedCancellationRepository.CopilotRerunCancelUnclaimedAs(ctx, actor, in, a)
	if err == nil && f.lostPreclaim != nil {
		return nil, f.lostPreclaim
	}
	return out, err
}
func (f *cancellationFaults) CopilotRerunCancelClaimed(ctx context.Context, actor string, in taskqueue.JobInputIdentity, a repo.CopilotRerunAuthorize) (*repo.CopilotRerunTerminalReceipt, error) {
	f.claimedCalls++
	if f.errorWithoutWrite != nil {
		return nil, f.errorWithoutWrite
	}
	out, err := f.protectedCancellationRepository.CopilotRerunCancelClaimed(ctx, actor, in, a)
	if err == nil && f.lostClaimed != nil {
		return nil, f.lostClaimed
	}
	return out, err
}
func (f *cancellationFaults) InspectCopilotRerunCancellation(ctx context.Context, actor string, in *repo.CopilotRerunCancellationLocator, a repo.CopilotRerunAuthorize) (*repo.CopilotRerunTerminalReceipt, error) {
	f.reads++
	return f.protectedCancellationRepository.InspectCopilotRerunCancellation(ctx, actor, in, a)
}

func TestProtectedCancellationNonOwnerCancelOnlyAndCurrentRevocation(t *testing.T) {
	s, db, scope, confirmed := pendingCancellationFixture(t)
	ctx := context.Background()
	req := &pipelinev1.StopPipelineRequest{PipelineId: scope.Pipeline.PipelineID, RunId: confirmed.NewRunID}
	var original model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", scope.Run.RunID).First(&original).Error)
	_, err := s.stop(ctx, authz.Subject{UserID: "actor"}, req, nil)
	require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
	// Request-supplied memberships cannot grant permission.
	_, err = s.stop(ctx, authz.Subject{UserID: "actor", TeamIDs: []string{"cancel-role"}}, req, nil)
	require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error)
	receipt, err := s.stop(ctx, authz.Subject{UserID: "cancel-actor"}, req, nil)
	require.NoError(t, err)
	require.True(t, receipt.Protected)
	require.True(t, receipt.ControllerRequested)
	require.Equal(t, "cancelled_before_claim", receipt.State)
	require.False(t, receipt.AgentTerminalAcknowledged)
	require.Equal(t, "not_applicable", receipt.NotificationStatus)
	var saved model.CopilotRerunIntent
	require.NoError(t, db.Where("intent_id = ?", confirmed.IntentID).First(&saved).Error)
	require.Equal(t, "actor", saved.OwnerUserID)
	var after model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", scope.Run.RunID).First(&after).Error)
	require.Equal(t, original, after)
	again, err := s.stop(ctx, authz.Subject{UserID: "cancel-actor"}, req, nil)
	require.NoError(t, err)
	require.Equal(t, receipt, again)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "cancel-grant").Update("is_enabled", 0).Error)
	_, err = s.stop(ctx, authz.Subject{UserID: "cancel-actor"}, req, nil)
	require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
	var unchanged model.CopilotRerunIntent
	require.NoError(t, db.Where("intent_id = ?", saved.IntentID).First(&unchanged).Error)
	require.Equal(t, saved, unchanged)
	raw, err := sonic.Marshal(receipt)
	require.NoError(t, err)
	for _, private := range []string{"claimId", "agentId", "digest", "cipher", "nonce", "owner", "Stopped", "cleanup"} {
		require.NotContains(t, string(raw), private)
	}
}
func TestProtectedCancellationDisabledExpiredAndWrongScopeDenied(t *testing.T) {
	for _, scenario := range []string{"expired", "disabled-user", "disabled-role", "wrong-scope", "foreign-run", "agent"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, scope, confirmed := pendingCancellationFixture(t)
			req := &pipelinev1.StopPipelineRequest{PipelineId: scope.Pipeline.PipelineID, RunId: confirmed.NewRunID}
			actor := authz.Subject{UserID: "cancel-actor"}
			switch scenario {
			case "expired":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "cancel-grant").Update("expires_at", time.Now().Add(-time.Minute)).Error)
			case "disabled-user":
				require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", actor.UserID).Update("is_enabled", 0).Error)
			case "disabled-role":
				require.NoError(t, db.Model(&model.Role{}).Where("role_id = ?", "cancel-role").Update("is_enabled", 0).Error)
			case "wrong-scope":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "cancel-grant").Update("scope_id", "foreign-project").Error)
			case "foreign-run":
				req.PipelineId = "foreign-pipeline"
			case "agent":
				actor.IsAgent = true
			}
			_, err := s.stop(context.Background(), actor, req, nil)
			require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", confirmed.NewJobRunID).First(&job).Error)
			require.Equal(t, model.JobRunStatusPending, job.Status)
		})
	}
}
func TestProtectedCancellationClaimedNotificationAndExactReplay(t *testing.T) {
	for _, scenario := range []string{"enqueued", "unknown", "unavailable", "lost-ack", "no-commit"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRerunBridgeFixture(t)
			_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
			require.NoError(t, err)
			addCancelOnlyActor(t, f.db, f.saved.ProjectID)
			n := &cancelNotifier{}
			s, err := NewProtectedRunCancellationService(f.repositories, n)
			require.NoError(t, err)
			faults := &cancellationFaults{protectedCancellationRepository: f.repositories}
			s.backend = faults
			if scenario == "unknown" {
				n.err = context.DeadlineExceeded
			}
			if scenario == "unavailable" {
				s.notifier = nil
			}
			if scenario == "lost-ack" {
				faults.lostClaimed = errors.New("private database ACK lost")
			}
			if scenario == "no-commit" {
				faults.errorWithoutWrite = errors.New("private database uncertainty")
			}
			req := &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}
			actor := authz.Subject{UserID: "cancel-actor"}
			_, err = s.stop(context.Background(), authz.Subject{UserID: "actor"}, req, nil)
			require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
			// Count only the authorized request after proving trigger-only denial.
			faults.claimedCalls = 0
			faults.reads = 0
			faults.locates = 0
			out, err := s.stop(context.Background(), actor, req, nil)
			if scenario == "no-commit" {
				require.Error(t, err)
				require.Empty(t, n.calls)
				require.Equal(t, 1, faults.claimedCalls)
				require.Equal(t, 1, faults.reads)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "cancellation_recorded", out.State)
			require.True(t, out.ControllerRequested)
			require.False(t, out.AgentTerminalAcknowledged)
			expected := scenario
			if expected == "lost-ack" {
				expected = "enqueued"
			}
			require.Equal(t, expected, out.NotificationStatus)
			require.Equal(t, 1, faults.claimedCalls)
			require.Equal(t, 1, faults.locates)
			if scenario == "lost-ack" {
				require.Equal(t, 1, faults.reads)
			}
			if scenario != "unavailable" {
				require.Len(t, n.calls, 1)
				require.Equal(t, f.request.ClaimID, n.calls[0].ClaimID)
				require.Equal(t, f.request.Reference.AgentID, n.calls[0].AgentID)
				require.Equal(t, "cancel-actor", n.notifications[0].actor)
				require.Equal(t, f.request.ClaimID, n.notifications[0].locator.Identity.ClaimID)
				require.Equal(t, f.saved.NewRunID, n.notifications[0].locator.RunID)
			}
			var first model.JobRun
			var gate model.CopilotRerunAdmissionGate
			var schedule model.CopilotRerunSchedule
			require.NoError(t, f.db.Where("job_run_id = ?", f.saved.NewJobRunID).First(&first).Error)
			require.NoError(t, f.db.Where("gate_id = ?", f.saved.GateID).First(&gate).Error)
			require.NoError(t, f.db.Where("schedule_id = ?", f.saved.ScheduleID).First(&schedule).Error)
			again, err := s.stop(context.Background(), actor, req, nil)
			require.NoError(t, err)
			require.Equal(t, out, again)
			var after model.JobRun
			var afterGate model.CopilotRerunAdmissionGate
			var afterSchedule model.CopilotRerunSchedule
			require.NoError(t, f.db.Where("job_run_id = ?", f.saved.NewJobRunID).First(&after).Error)
			require.NoError(t, f.db.Where("gate_id = ?", f.saved.GateID).First(&afterGate).Error)
			require.NoError(t, f.db.Where("schedule_id = ?", f.saved.ScheduleID).First(&afterSchedule).Error)
			require.Equal(t, first, after)
			require.Equal(t, gate, afterGate)
			require.Equal(t, schedule, afterSchedule)
			if scenario != "unavailable" {
				require.Len(t, n.calls, 2)
				require.Equal(t, n.calls[0], n.calls[1])
			}
			require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "cancel-grant").Update("is_enabled", 0).Error)
			_, err = s.stop(context.Background(), actor, req, nil)
			require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
			if scenario != "unavailable" {
				require.Len(t, n.calls, 2)
			}
		})
	}
}
func TestProtectedCancellationClaimRaceRelocatesOnceAfterGateRelease(t *testing.T) {
	f := newRerunBridgeFixture(t)
	addCancelOnlyActor(t, f.db, f.saved.ProjectID)
	n := &cancelNotifier{}
	s, err := NewProtectedRunCancellationService(f.repositories, n)
	require.NoError(t, err)
	faults := &cancellationFaults{protectedCancellationRepository: f.repositories, beforePreclaim: func() { _, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request); require.NoError(t, err) }}
	s.backend = faults
	out, err := s.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}, nil)
	require.NoError(t, err)
	require.Equal(t, "cancellation_recorded", out.State)
	require.Equal(t, 1, faults.preclaimCalls)
	require.Equal(t, 1, faults.claimedCalls)
	require.Equal(t, 2, faults.locates)
	require.Len(t, n.calls, 1)
}
func TestProtectedCancellationUnknownPreclaimRecoversWithoutRetry(t *testing.T) {
	s, _, scope, confirmed := pendingCancellationFixture(t)
	faults := &cancellationFaults{protectedCancellationRepository: s.repos, lostPreclaim: errors.New("commit ACK lost")}
	s.backend = faults
	out, err := s.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, &pipelinev1.StopPipelineRequest{PipelineId: scope.Pipeline.PipelineID, RunId: confirmed.NewRunID}, nil)
	require.NoError(t, err)
	require.Equal(t, "cancelled_before_claim", out.State)
	require.Equal(t, 1, faults.preclaimCalls)
	require.Equal(t, 1, faults.reads)
	require.Equal(t, 1, faults.locates)
}
func TestProtectedCancellationOrdinaryDelegationAndNoMalformedFallback(t *testing.T) {
	s, db, scope, confirmed := pendingCancellationFixture(t)
	calls := 0
	ordinary := func(_ context.Context, req *pipelinev1.StopPipelineRequest) (*pipelinev1.StopPipelineResponse, error) {
		calls++
		require.Equal(t, "keep reason", req.Reason)
		return &pipelinev1.StopPipelineResponse{Success: true}, nil
	}
	req := &pipelinev1.StopPipelineRequest{PipelineId: scope.Pipeline.PipelineID, RunId: scope.Run.RunID, Reason: "keep reason"}
	out, err := s.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, req, ordinary)
	require.NoError(t, err)
	require.False(t, out.Protected)
	require.Equal(t, 1, calls)
	_, err = s.stop(context.Background(), authz.Subject{UserID: "actor"}, req, ordinary)
	require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
	require.Equal(t, 1, calls)
	require.NoError(t, db.Model(&model.PipelineRun{}).Where("run_id = ?", confirmed.NewRunID).Update("copilot_rerun_gate_id", "").Error)
	req.RunId = confirmed.NewRunID
	_, err = s.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, req, ordinary)
	require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
	require.Equal(t, 1, calls)
}

func TestProtectedCancellationAcknowledgmentComesOnlyFromExactAgentReceipts(t *testing.T) {
	f := newRerunBridgeFixture(t)
	_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	addCancelOnlyActor(t, f.db, f.saved.ProjectID)
	n := &cancelNotifier{}
	s, err := NewProtectedRunCancellationService(f.repositories, n)
	require.NoError(t, err)
	req := &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}
	actor := authz.Subject{UserID: "cancel-actor"}
	ctx := context.Background()
	out, err := s.stop(ctx, actor, req, nil)
	require.NoError(t, err)
	require.False(t, out.AgentTerminalAcknowledged)
	require.Len(t, n.calls, 1)
	jobs := f.repositories.JobRun.(*repo.JobRunRepo)
	job, err := jobs.GetByJobRunID(ctx, f.saved.NewJobRunID)
	require.NoError(t, err)
	require.NoError(t, jobs.CommitCancellationReceipt(ctx, job, f.request.ClaimID, strings.Repeat("a", 64)))
	out, err = s.stop(ctx, actor, req, nil)
	require.NoError(t, err)
	require.False(t, out.AgentTerminalAcknowledged)
	require.Len(t, n.calls, 2)
	var steps []model.StepRun
	require.NoError(t, f.db.Where("job_run_id = ?", job.JobRunID).Find(&steps).Error)
	for i := range steps {
		step := &steps[i]
		require.NoError(t, f.repositories.StepRun.(*repo.StepRunRepo).CommitCancellationReceipt(ctx, step, agentidentity.StepClaimID(f.request.ClaimID, step.StepRunID), strings.Repeat("b", 64)))
	}
	out, err = s.stop(ctx, actor, req, nil)
	require.NoError(t, err)
	require.True(t, out.AgentTerminalAcknowledged)
	require.Equal(t, "not_needed", out.NotificationStatus)
	require.Len(t, n.calls, 2)
	hint, err := f.repositories.LocateCopilotRerunCancellation(ctx, req.PipelineId, req.RunId)
	require.NoError(t, err)
	recovered, err := f.repositories.InspectCopilotRerunCancellation(ctx, actor.UserID, hint, authorizePipelineCancellation)
	require.NoError(t, err)
	require.True(t, recovered.AgentTerminalAcknowledged)
}

func TestProtectedCancellationOrdinaryOutcomeAndDeferredBodyValidation(t *testing.T) {
	s, db, scope, confirmed := pendingCancellationFixture(t)
	ctx := context.Background()
	actor := authz.Subject{UserID: "cancel-actor"}
	req := &pipelinev1.StopPipelineRequest{PipelineId: scope.Pipeline.PipelineID, RunId: scope.Run.RunID}
	ordinaryCalls := 0
	sentinel := errors.New("original ordinary transport error")
	for _, code := range []int32{0, 404, 409, 503} {
		response := &pipelinev1.StopPipelineResponse{Success: code == 0, Error: &pipelinev1.Error{Code: code, Message: "original ordinary message"}}
		out, err := s.stop(ctx, actor, req, func(context.Context, *pipelinev1.StopPipelineRequest) (*pipelinev1.StopPipelineResponse, error) {
			ordinaryCalls++
			return response, sentinel
		})
		require.NoError(t, err)
		require.Same(t, response, out.OrdinaryOutcome().Response)
		require.ErrorIs(t, out.OrdinaryOutcome().Err, sentinel)
		raw, err := sonic.Marshal(out)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "original ordinary")
	}
	before := ordinaryCalls
	out, err := s.stop(ctx, actor, req, func(context.Context, *pipelinev1.StopPipelineRequest) (*pipelinev1.StopPipelineResponse, error) {
		ordinaryCalls++
		return nil, nil
	}, PipelineStopBodyValidation{})
	require.NoError(t, err)
	require.True(t, out.OrdinaryOutcome().BodyInvalid)
	require.Equal(t, before, ordinaryCalls)
	req.RunId = confirmed.NewRunID
	_, err = s.stop(ctx, actor, req, nil, PipelineStopBodyValidation{OrdinaryValid: true, ProtectedValid: false})
	require.ErrorIs(t, err, repo.ErrCopilotRerunInvalid)
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", confirmed.NewJobRunID).First(&job).Error)
	require.Equal(t, model.JobRunStatusPending, job.Status)
	_, err = s.stop(ctx, authz.Subject{UserID: "actor"}, req, nil, PipelineStopBodyValidation{OrdinaryValid: true, ProtectedValid: false})
	require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
}
