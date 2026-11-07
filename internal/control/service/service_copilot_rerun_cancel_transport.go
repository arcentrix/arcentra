// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"reflect"
	"time"

	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

const protectedCancellationNotificationTimeout = 10 * time.Second

type protectedCancellationTransport struct {
	backend   protectedCancellationRepository
	publisher rerunContextPublisher
}

func newProtectedCancellationTransport(r *repo.Repositories, publisher nova.IConsumer) (*protectedCancellationTransport, error) {
	p, ok := publisher.(rerunContextPublisher)
	if r == nil || !ok || p == nil || (reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil()) {
		return nil, repo.ErrCopilotRerunUnavailable
	}
	return &protectedCancellationTransport{backend: r, publisher: p}, nil
}

// The controller receipt is authorization to request cancellation, never proof
// that the Agent stopped. Recheck the same durable identity and live actor before
// using only the existing per-Agent control queue. No new task identity is made.
func (t *protectedCancellationTransport) NotifyProtectedCancellation(ctx context.Context, n ProtectedCancellationNotification) error {
	if t == nil || t.backend == nil || t.publisher == nil || ctx == nil || ctx.Err() != nil || !rerunAPIID(n.actor) || !n.locator.Protected || !n.locator.Claimed || !cancellationReceiptMatches(&n.locator, &n.receipt) {
		return repo.ErrCopilotRerunUnavailable
	}
	// Fiber's user context may have no request deadline. Bound the complete
	// notification attempt, including its fresh authorization reads, while
	// preserving any earlier caller deadline. Expiry cannot mean not delivered.
	ctx, cancel := context.WithTimeout(ctx, protectedCancellationNotificationTimeout)
	defer cancel()
	current, err := t.backend.LocateCopilotRerunCancellation(ctx, n.locator.PipelineID, n.locator.RunID)
	if err != nil || !sameCancellationTarget(&n.locator, current) || !current.Claimed || current.Identity != n.locator.Identity {
		return repo.ErrCopilotRerunUnavailable
	}
	receipt, err := t.backend.InspectCopilotRerunCancellation(ctx, n.actor, current, authorizePipelineCancellation)
	if err != nil || !cancellationReceiptMatches(current, receipt) || receipt.CompletedAtUnixMilli != n.receipt.CompletedAtUnixMilli {
		return repo.ErrCopilotRerunUnavailable
	}
	if receipt.AgentTerminalAcknowledged {
		// The error-only notifier contract reserves nil for an actual enqueue.
		// A late ACK is safe to inspect, but cannot be mislabeled as delivery.
		return repo.ErrCopilotRerunConflict
	}
	if ctx.Err() != nil {
		return repo.ErrCopilotRerunUnavailable
	}
	data, err := sonic.Marshal(taskqueue.JobCancelTaskPayload{AgentID: receipt.AgentID, JobRunID: receipt.JobRunID, Reason: "Authorized cancellation of protected execution"})
	if err != nil {
		return repo.ErrCopilotRerunUnavailable
	}
	result, err := t.publisher.EnqueueContext(ctx, &nova.Task{Type: taskqueue.TaskTypeJobCancel, Payload: data}, nova.Queue(taskqueue.AgentControlQueue(receipt.AgentID)))
	if err != nil || result == nil {
		return repo.ErrCopilotRerunUnavailable
	}
	return nil
}
