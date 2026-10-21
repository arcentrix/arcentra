// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRerunReferenceOperatorOffDoesNotFetchClaim(t *testing.T) {
	ctx, conf, client, fake, m, _, ref := rerunFetchFixture(t)
	conf.Grpc.ServerAddr = "127.0.0.1:1"
	outbox, err := newTerminalOutbox(ctx, t.TempDir(), conf.Agent.ID, conf.Grpc.ServerAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.close() })
	conf.Agent.Sandbox.Containerd.TerminalRerunEnabled = false
	body, err := json.Marshal(ref)
	require.NoError(t, err)
	require.Error(t, handleRerunReference(ctx, conf, client, nil, outbox, m, body))
	require.Empty(t, fake.requests)
	// The identical prepared dependencies reach the claim RPC with opt-in on.
	conf.Agent.Sandbox.Containerd.TerminalRerunEnabled = true
	fake.failures = 100
	require.Error(t, handleRerunReference(ctx, conf, client, nil, outbox, m, body))
	require.Len(t, fake.requests, 3)
}
