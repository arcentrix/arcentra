// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
)

type captureCancellationNotification struct {
	value ProtectedCancellationNotification
}

func (c *captureCancellationNotification) NotifyProtectedCancellation(_ context.Context, n ProtectedCancellationNotification) error {
	c.value = n
	return nil
}

func TestProtectedCancellationTransportRevalidatesActorAndExactIdentity(t *testing.T) {
	f := newRerunBridgeFixture(t)
	_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	addCancelOnlyActor(t, f.db, f.saved.ProjectID)
	capture := &captureCancellationNotification{}
	s, err := NewProtectedRunCancellationService(f.repositories, capture)
	require.NoError(t, err)
	out, err := s.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}, nil)
	require.NoError(t, err)
	require.False(t, out.AgentTerminalAcknowledged)
	calls := 0
	publisher := &rerunDispatchPublisher{send: func(ctx context.Context, task *nova.Task, opts ...nova.Option) (*nova.Result, error) {
		require.NoError(t, ctx.Err())
		require.Equal(t, taskqueue.TaskTypeJobCancel, task.Type)
		require.Len(t, opts, 1)
		// Nova intentionally keeps Option.apply private. Queue returns a function;
		// invoke that exact option without unsafe access or a broker connection.
		options := &nova.TaskOpts{}
		reflect.ValueOf(opts[0]).Call([]reflect.Value{reflect.ValueOf(options)})
		require.Equal(t, taskqueue.AgentControlQueue(f.request.Reference.AgentID), options.Queue)
		require.True(t, options.ProcessAt.IsZero())
		var body taskqueue.JobCancelTaskPayload
		require.NoError(t, json.Unmarshal(task.Payload, &body))
		require.Equal(t, f.saved.NewJobRunID, body.JobRunID)
		require.Equal(t, f.request.Reference.AgentID, body.AgentID)
		require.Equal(t, "Authorized cancellation of protected execution", body.Reason)
		calls++
		return &nova.Result{ID: "same-control-notification"}, nil
	}}
	transport, err := newProtectedCancellationTransport(f.repositories, publisher)
	require.NoError(t, err)
	require.Error(t, transport.NotifyProtectedCancellation(context.Background(), ProtectedCancellationNotification{}))
	require.Error(t, transport.NotifyProtectedCancellation(nil, capture.value))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, transport.NotifyProtectedCancellation(cancelled, capture.value))
	require.Zero(t, calls)
	forged := capture.value
	forged.locator.Identity.ClaimID = "foreign"
	require.Error(t, transport.NotifyProtectedCancellation(context.Background(), forged))
	require.Zero(t, calls)
	require.NoError(t, transport.NotifyProtectedCancellation(context.Background(), capture.value))
	require.Equal(t, 1, calls)
	// A fresh primary locator must reject a changed assignment even though the
	// opaque notification still contains its previously authorized exact tuple.
	require.NoError(t, f.db.Model(&model.JobRun{}).Where("job_run_id = ?", f.saved.NewJobRunID).Update("agent_id", "foreign-agent").Error)
	require.Error(t, transport.NotifyProtectedCancellation(context.Background(), capture.value))
	require.Equal(t, 1, calls)
	require.NoError(t, f.db.Model(&model.JobRun{}).Where("job_run_id = ?", f.saved.NewJobRunID).Update("agent_id", f.request.Reference.AgentID).Error)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "cancel-grant").Update("is_enabled", 0).Error)
	require.Error(t, transport.NotifyProtectedCancellation(context.Background(), capture.value))
	require.Equal(t, 1, calls)
	// A separate user retry can notify the same already-recorded claim after
	// current cancellation authority is restored; it cannot create a new claim.
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "cancel-grant").Update("is_enabled", 1).Error)
	repeated, err := NewProtectedRunCancellationService(f.repositories, transport)
	require.NoError(t, err)
	out, err = repeated.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}, nil)
	require.NoError(t, err)
	require.Equal(t, "enqueued", out.NotificationStatus)
	require.True(t, out.ControllerRequested)
	require.False(t, out.AgentTerminalAcknowledged)
	require.Equal(t, 2, calls)
	fixed, err := f.repositories.InspectCopilotRerunCancellation(context.Background(), "cancel-actor", &capture.value.locator, authorizePipelineCancellation)
	require.NoError(t, err)
	require.Equal(t, capture.value.receipt, *fixed)
}

func TestProtectedCancellationTransportUnknownPublishIsNotRetried(t *testing.T) {
	f := newRerunBridgeFixture(t)
	_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	addCancelOnlyActor(t, f.db, f.saved.ProjectID)
	calls := 0
	publisher := &rerunDispatchPublisher{send: func(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error) {
		calls++
		return nil, context.DeadlineExceeded
	}}
	transport, err := newProtectedCancellationTransport(f.repositories, publisher)
	require.NoError(t, err)
	s, err := NewProtectedRunCancellationService(f.repositories, transport)
	require.NoError(t, err)
	out, err := s.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}, nil)
	require.NoError(t, err)
	require.True(t, out.ControllerRequested)
	require.False(t, out.AgentTerminalAcknowledged)
	require.Equal(t, "unknown", out.NotificationStatus)
	require.Equal(t, 1, calls)
}

func TestProtectedCancellationTransportRejectsMissingContextAndTypedNil(t *testing.T) {
	f := newRerunBridgeFixture(t)
	var typedNil *rerunDispatchPublisher
	for _, publisher := range []nova.IConsumer{nil, typedNil, &legacyCancellationPublisher{}} {
		transport, err := newProtectedCancellationTransport(f.repositories, publisher)
		require.ErrorIs(t, err, repo.ErrCopilotRerunUnavailable)
		require.Nil(t, transport)
	}
	publisher := &rerunDispatchPublisher{send: func(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error) {
		t.Fatal("no enqueue for absent or cancelled context")
		return nil, nil
	}}
	transport, err := newProtectedCancellationTransport(nil, publisher)
	require.Error(t, err)
	require.Nil(t, transport)
	transport, err = newProtectedCancellationTransport(f.repositories, publisher)
	require.NoError(t, err)
	require.Error(t, transport.NotifyProtectedCancellation(nil, ProtectedCancellationNotification{}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, transport.NotifyProtectedCancellation(ctx, ProtectedCancellationNotification{}))
}

type legacyCancellationPublisher struct{ nova.IConsumer }
type cancellationInspectionHook struct {
	protectedCancellationRepository
	before func()
	after  func()
}

func (h *cancellationInspectionHook) InspectCopilotRerunCancellation(ctx context.Context, actor string, locator *repo.CopilotRerunCancellationLocator, a repo.CopilotRerunAuthorize) (*repo.CopilotRerunTerminalReceipt, error) {
	if h.before != nil {
		h.before()
	}
	receipt, err := h.protectedCancellationRepository.InspectCopilotRerunCancellation(ctx, actor, locator, a)
	if h.after != nil {
		h.after()
	}
	return receipt, err
}
func TestProtectedCancellationTransportLateAckRevocationAndContextStayUnknown(t *testing.T) {
	for _, scenario := range []string{"late-ack", "revoked", "context-after-inspection", "nil-publish-result"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRerunBridgeFixture(t)
			_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
			require.NoError(t, err)
			addCancelOnlyActor(t, f.db, f.saved.ProjectID)
			calls := 0
			publisher := &rerunDispatchPublisher{send: func(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error) { calls++; return nil, nil }}
			transport, err := newProtectedCancellationTransport(f.repositories, publisher)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hook := &cancellationInspectionHook{protectedCancellationRepository: f.repositories}
			transport.backend = hook
			switch scenario {
			case "late-ack":
				hook.before = func() {
					jobs := f.repositories.JobRun.(*repo.JobRunRepo)
					job, err := jobs.GetByJobRunID(context.Background(), f.saved.NewJobRunID)
					require.NoError(t, err)
					require.Equal(t, model.JobRunStatusCancelled, job.Status)
					require.NoError(t, jobs.CommitCancellationReceipt(context.Background(), job, f.request.ClaimID, strings.Repeat("a", 64)))
					var steps []model.StepRun
					require.NoError(t, f.db.Where("job_run_id = ?", job.JobRunID).Find(&steps).Error)
					for i := range steps {
						step := &steps[i]
						require.NoError(t, f.repositories.StepRun.(*repo.StepRunRepo).CommitCancellationReceipt(context.Background(), step, agentidentity.StepClaimID(f.request.ClaimID, step.StepRunID), strings.Repeat("b", 64)))
					}
				}
			case "revoked":
				hook.before = func() {
					require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "cancel-grant").Update("is_enabled", 0).Error)
				}
			case "context-after-inspection":
				hook.after = cancel
			}
			s, err := NewProtectedRunCancellationService(f.repositories, transport)
			require.NoError(t, err)
			out, err := s.stop(ctx, authz.Subject{UserID: "cancel-actor"}, &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}, nil)
			require.NoError(t, err)
			require.True(t, out.ControllerRequested)
			require.False(t, out.AgentTerminalAcknowledged)
			require.Equal(t, "unknown", out.NotificationStatus)
			expected := 0
			if scenario == "nil-publish-result" {
				expected = 1
			}
			require.Equal(t, expected, calls)
			var saved model.JobRun
			require.NoError(t, f.db.Where("job_run_id = ?", f.saved.NewJobRunID).First(&saved).Error)
			require.Equal(t, model.JobRunStatusCancelled, saved.Status)
			require.Equal(t, f.request.ClaimID, saved.CancellationClaimID)
		})
	}
}
func TestProtectedCancellationTransportStartupOffPublishesNothing(t *testing.T) {
	services := &Services{Agent: &AgentService{}}
	require.NoError(t, services.ConfigureTerminalRerun(false, nil))
	require.Nil(t, services.ProtectedRunCancellation())
	require.Nil(t, services.TerminalRerunService())
	var absent *Services
	require.Nil(t, absent.ProtectedRunCancellation())
}

type cancellationDeadlineRepository struct {
	protectedCancellationRepository
	check func(context.Context)
}

func (r *cancellationDeadlineRepository) LocateCopilotRerunCancellation(ctx context.Context, pipeline, run string) (*repo.CopilotRerunCancellationLocator, error) {
	r.check(ctx)
	return r.protectedCancellationRepository.LocateCopilotRerunCancellation(ctx, pipeline, run)
}
func (r *cancellationDeadlineRepository) InspectCopilotRerunCancellation(ctx context.Context, actor string, locator *repo.CopilotRerunCancellationLocator, authorize repo.CopilotRerunAuthorize) (*repo.CopilotRerunTerminalReceipt, error) {
	r.check(ctx)
	return r.protectedCancellationRepository.InspectCopilotRerunCancellation(ctx, actor, locator, authorize)
}
func TestProtectedCancellationTransportBoundsReadsAndEnqueueDeadline(t *testing.T) {
	f := newRerunBridgeFixture(t)
	_, err := f.bridge.ClaimAndFetchCopilotRerun(f.ctx, f.request)
	require.NoError(t, err)
	addCancelOnlyActor(t, f.db, f.saved.ProjectID)
	capture := &captureCancellationNotification{}
	service, err := NewProtectedRunCancellationService(f.repositories, capture)
	require.NoError(t, err)
	_, err = service.stop(context.Background(), authz.Subject{UserID: "cancel-actor"}, &pipelinev1.StopPipelineRequest{PipelineId: f.saved.PipelineID, RunId: f.saved.NewRunID}, nil)
	require.NoError(t, err)
	for _, earlier := range []bool{false, true} {
		name := "background"
		if earlier {
			name = "earlier-caller"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var expected time.Time
			if earlier {
				expected = time.Now().Add(5 * time.Second)
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, expected)
				defer cancel()
			}
			var deadlines []time.Time
			check := func(ctx context.Context) {
				deadline, ok := ctx.Deadline()
				require.True(t, ok, "notification reads and enqueue must be bounded")
				require.True(t, deadline.After(time.Now()))
				require.True(t, !deadline.After(time.Now().Add(protectedCancellationNotificationTimeout)))
				if earlier {
					require.Equal(t, expected, deadline)
				}
				deadlines = append(deadlines, deadline)
			}
			publisher := &rerunDispatchPublisher{send: func(ctx context.Context, _ *nova.Task, _ ...nova.Option) (*nova.Result, error) {
				check(ctx)
				return &nova.Result{ID: "bounded-existing-notification"}, nil
			}}
			transport, err := newProtectedCancellationTransport(f.repositories, publisher)
			require.NoError(t, err)
			transport.backend = &cancellationDeadlineRepository{protectedCancellationRepository: f.repositories, check: check}
			require.NoError(t, transport.NotifyProtectedCancellation(ctx, capture.value))
			require.Len(t, deadlines, 3, "fresh locator, current authorized receipt, then one enqueue")
			require.Equal(t, deadlines[0], deadlines[1])
			require.Equal(t, deadlines[0], deadlines[2])
		})
	}
}
