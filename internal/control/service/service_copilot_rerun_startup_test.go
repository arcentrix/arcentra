// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTerminalRerunStartupOffIsOnceOnlyAndInert(t *testing.T) {
	s := &Services{Agent: &AgentService{}}
	require.NoError(t, s.ConfigureTerminalRerun(false, nil))
	require.Nil(t, s.TerminalRerunService())
	require.Nil(t, s.Agent.rerunClaim)
	require.Empty(t, s.rerunCapturePolicy)
	require.NoError(t, s.StartTerminalRerun(context.Background()))
	require.NoError(t, s.StopTerminalRerun(context.Background()))
	require.Error(t, s.ConfigureTerminalRerun(false, nil))
	require.Error(t, s.ConfigureTerminalRerun(true, nil))
}
func TestTerminalRerunStartupPrerequisiteFailurePublishesNothing(t *testing.T) {
	s := &Services{Agent: &AgentService{}}
	require.Error(t, s.ConfigureTerminalRerun(true, nil))
	require.Nil(t, s.TerminalRerunService())
	require.Nil(t, s.Agent.rerunClaim)
	require.Empty(t, s.rerunCapturePolicy)
	require.False(t, s.rerunStartupBound)
}
