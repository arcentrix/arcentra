// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTerminalRerunProcessBindingRejectsConfigMismatch(t *testing.T) {
	e := &Process{appConf: &config.AppConfig{}}
	s := &service.Services{}
	require.Error(t, e.BindTerminalRerunServices(s, true))
	require.Nil(t, s.TerminalRerunService())
	require.NoError(t, e.BindTerminalRerunServices(s, false))
	require.Error(t, e.BindTerminalRerunServices(s, false))
}
