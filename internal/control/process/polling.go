// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/polling"
)

func validatePollingSubmission(run *model.PipelineRun, parsed *spec.Pipeline) (bool, error) {
	if run == nil {
		return false, nil
	}
	if run.PollingContext == "" && run.PollingContextDigest == "" && run.TriggeredBy != polling.Actor {
		return false, nil
	}
	if !repo.IsPollingPipelineRun(run) {
		return true, fmt.Errorf("polling identity is not valid")
	}
	context, e := polling.ParseRunContext(run.PollingContext)
	if e != nil {
		return true, fmt.Errorf("polling context is not valid")
	}
	digest, e := service.PollingDefinitionDigest(parsed)
	if e != nil || digest != context.Target.ResolvedDefinitionDigest {
		return true, fmt.Errorf("polling definition does not match frozen evidence")
	}
	return true, nil
}
func (e *Process) claimPollingSubmission(run *model.PipelineRun) error {
	if e.repos == nil || e.repos.Pipeline == nil {
		return fmt.Errorf("durable polling admission unavailable")
	}
	starter, ok := e.repos.Pipeline.(interface {
		StartPollingRunIfPending(context.Context, *model.PipelineRun) (polling.AdmissionOutcome, error)
	})
	if !ok {
		return fmt.Errorf("durable polling admission unavailable")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	outcome, err := starter.StartPollingRunIfPending(ctx, run)
	if err != nil {
		return fmt.Errorf("claim polling run: %w", err)
	}
	if outcome.Outcome != polling.OutcomeAdmitted || !outcome.ClaimedNow || outcome.RunID != run.RunID {
		return fmt.Errorf("polling run cannot be newly scheduled")
	}
	return nil
}
