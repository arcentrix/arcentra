// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"errors"
	"github.com/arcentrix/arcentra/internal/control/service"
)

// BindTerminalRerunServices supplies the already configured private queue before
// HTTP/gRPC listeners start. Services rejects any repeated binding.
func (e *Process) BindTerminalRerunServices(services *service.Services, enabled bool) error {
	if e == nil || services == nil {
		return errors.New("terminal execution startup is unavailable")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || e.logInitErr != nil {
		return errors.New("terminal execution process is unavailable")
	}
	configured := e.appConf != nil && e.appConf.Copilot.Execution.TerminalRerunEnabled
	if configured != enabled {
		return errors.New("terminal execution startup configuration disagrees")
	}
	return services.ConfigureTerminalRerun(enabled, e.taskQueue)
}
