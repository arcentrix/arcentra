// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"encoding/json"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type rerunDefiniteRejectionClient struct{ *rerunFetchClient }

func (c *rerunDefiniteRejectionClient) ClaimAndFetchCopilotRerun(_ context.Context, in *agentv1.ClaimAndFetchCopilotRerunRequest, _ ...grpc.CallOption) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, proto.Clone(in).(*agentv1.ClaimAndFetchCopilotRerunRequest))
	return nil, status.Error(codes.FailedPrecondition, "protected rerun claim or disclosure rejected")
}
func TestRerunDefiniteRejectionNeverStartsReportsOrReallocatesClaim(t *testing.T) {
	ctx, conf, client, fake, m, h, ref := rerunFetchFixture(t)
	conf.Grpc.ServerAddr = "127.0.0.1:1"
	client.AgentClient = &rerunDefiniteRejectionClient{fake}
	payload, err := fetchRerunClaim(ctx, conf, client, m, h, ref)
	require.Nil(t, payload)
	require.ErrorIs(t, err, errRerunIncarnationUnconfirmed)
	require.Len(t, fake.requests, 1)
	first := fake.requests[0]
	_, err = m.takeStartToken(h)
	require.Error(t, err)
	outbox, err := newTerminalOutbox(ctx, t.TempDir(), conf.Agent.ID, conf.Grpc.ServerAddr)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.close()) })
	body, err := json.Marshal(ref)
	require.NoError(t, err)
	handler := newWorkerHandler(ctx, conf, client, nil, outbox, m)
	require.NoError(t, handler.ProcessTask(ctx, &nova.Task{Type: taskpayload.TaskTypeCopilotRerunReference, Payload: body}))
	require.Len(t, fake.requests, 2)
	require.Equal(t, first.ClaimId, fake.requests[1].ClaimId)
	require.Equal(t, first.AdmissionNonce, fake.requests[1].AdmissionNonce)
	require.Zero(t, fake.reports)
	require.Empty(t, fake.generationClaims)
	require.Zero(t, offlineRuntimeFromContext(ctx).(*fakeGenerationRuntime).allocations)
	require.Empty(t, outbox.entries)
	m.mu.Lock()
	entry, err := m.entryLocked(h)
	require.NoError(t, err)
	require.Equal(t, rerunClaimPending, entry.phase)
	m.mu.Unlock()
}
