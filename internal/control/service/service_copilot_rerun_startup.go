// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

var errRerunStartup = errors.New("terminal whole-job execution startup prerequisites are unavailable")

type copilotRerunBundle struct {
	api          *CopilotRerunService
	claim        *agentRerunClaimBridge
	dispatcher   *copilotRerunDispatcher
	cancellation *ProtectedRunCancellationService
}

// ConfigureTerminalRerun is a once-only startup binding, before listeners start.
// All dependencies are prepared before publishing any capability.
func (s *Services) ConfigureTerminalRerun(enabled bool, publisher nova.IConsumer) error {
	if s == nil {
		return errRerunStartup
	}
	s.rerunStartupMu.Lock()
	defer s.rerunStartupMu.Unlock()
	if s.rerunStartupBound {
		return errRerunStartup
	}
	if !enabled {
		s.rerunStartupBound = true
		return nil
	}
	if !taskqueue.TerminalRerunAllowed(s.offlineRuntimePolicy) || s.Agent == nil || s.Secret == nil || s.retryApprovalPolicy == nil || s.repos == nil || s.Agent.rerunClaim != nil || (s.rerunCapturePolicy != "" && s.rerunCapturePolicy != CopilotRerunCaptureOff) {
		return errRerunStartup
	}
	api, err := NewCopilotRerunService(s.repos, s.retryApprovalPolicy, s.offlineRuntimePolicy, s.Secret)
	if err != nil {
		return errRerunStartup
	}
	claim, err := newAgentRerunClaimBridge(s.repos, s.retryApprovalPolicy, s.offlineRuntimePolicy, s.Secret)
	if err != nil {
		return errRerunStartup
	}
	dispatcher, err := newCopilotRerunDispatcher(s.repos, publisher, s.retryApprovalPolicy, s.offlineRuntimePolicy, s.Secret, s.observeTerminalRerun)
	if err != nil {
		return errRerunStartup
	}
	transport, err := newProtectedCancellationTransport(s.repos, publisher)
	if err != nil {
		return errRerunStartup
	}
	cancellation, err := NewProtectedRunCancellationService(s.repos, transport)
	if err != nil {
		return errRerunStartup
	}
	bundle := &copilotRerunBundle{api: api, claim: claim, dispatcher: dispatcher, cancellation: cancellation}
	s.Agent.rerunClaim = claim
	s.rerunCapturePolicy = CopilotRerunCaptureEligibleVerifiedManual
	s.rerunBundle.Store(bundle)
	s.rerunStartupBound = true
	return nil
}
func (s *Services) TerminalRerunService() *CopilotRerunService {
	if s == nil {
		return nil
	}
	b := s.rerunBundle.Load()
	if b == nil {
		return nil
	}
	return b.api
}
func (s *Services) StartTerminalRerun(ctx context.Context) error {
	if s == nil {
		return nil
	}
	b := s.rerunBundle.Load()
	if b == nil {
		return nil
	}
	return b.dispatcher.Start(ctx)
}
func (s *Services) StopTerminalRerun(ctx context.Context) error {
	if s == nil {
		return nil
	}
	b := s.rerunBundle.Load()
	if b == nil {
		return nil
	}
	return b.dispatcher.Stop(ctx)
}
func (s *Services) observeTerminalRerun(ctx context.Context, work repo.CopilotRerunDispatchWork) error {
	jobs, ok := s.repos.JobRun.(*repo.JobRunRepo)
	if !ok {
		return errRerunStartup
	}
	var current model.JobRun
	err := jobs.Database().WithContext(ctx).Clauses(database.Write()).Where("job_run_id = ?", work.JobRunID).First(&current).Error
	job := &current
	if err != nil || job == nil || job.JobRunID != work.JobRunID || job.PipelineRunID != work.RunID || job.AgentID != work.AgentID || job.ClaimID != work.ClaimID || job.CopilotRerunIntentID != work.IntentID || job.CopilotRerunGateID == "" {
		return errRerunDispatchUnknown
	}
	identity := taskqueue.JobInputIdentity{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
	_, err = jobs.FinalizeCopilotRerun(ctx, identity)
	return err
}

// ProtectedRunCancellation is bound with the immutable opt-in startup bundle.
func (s *Services) ProtectedRunCancellation() *ProtectedRunCancellationService {
	if s == nil {
		return nil
	}
	b := s.rerunBundle.Load()
	if b == nil {
		return nil
	}
	return b.cancellation
}
