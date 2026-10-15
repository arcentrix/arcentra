// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"fmt"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/serde"
	"maps"
	"slices"
	"strings"
)

func normalizeRetryApprovalPolicy(p *retryapproval.Policy) (*retryapproval.Policy, error) {
	if p == nil {
		return nil, nil
	}
	copy := *p
	copy.Candidates = slices.Clone(p.Candidates)
	slices.Sort(copy.Candidates)
	digest, err := copy.Digest()
	if err != nil {
		return nil, err
	}
	copy.Fingerprint = digest
	if err := copy.Validate(""); err != nil {
		return nil, err
	}
	return &copy, nil
}

// ConfigureRetryApprovalPolicy 固定可信的独立重试审批策略，禁止请求覆盖
func (s *Services) ConfigureRetryApprovalPolicy(p *retryapproval.Policy) error {
	normalized, err := normalizeRetryApprovalPolicy(p)
	if err != nil {
		return err
	}
	s.retryApprovalPolicy = normalized
	return nil
}

func (s *PipelineServiceImpl) captureRetryApprovalTrigger(ctx context.Context, req *pipelinev1.TriggerPipelineRequest, pipeline *model.Pipeline, run *model.PipelineRun, parsed *pipelinev1.Spec) error {
	names, err := validation.RetryApprovalJobNames(parsed, s.offlineRuntimePolicy)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	actor := verifiedPipelineActor(ctx)
	if actor == "" || strings.TrimSpace(req.GetTriggeredBy()) != actor || req.GetTriggeredBy() != actor {
		return fmt.Errorf("retry approval requires a verified manual requester")
	}
	if s.services == nil || s.services.retryApprovalPolicy == nil {
		return fmt.Errorf("trusted retry approval policy unavailable")
	}
	repositories := s.services.Repos()
	if err := authorizeRetryActor(ctx, repositories, actor, pipeline.ProjectID, pipeline.PipelineID, ""); err != nil {
		return err
	}
	policy := *s.services.retryApprovalPolicy
	policy.Candidates = nil
	for _, candidate := range s.services.retryApprovalPolicy.Candidates {
		if candidate != actor && authorizeRetryActor(ctx, repositories, candidate, pipeline.ProjectID, pipeline.PipelineID, "approval:approve") == nil {
			policy.Candidates = append(policy.Candidates, candidate)
		}
	}
	// 不降低操作者配置的 quorum；过滤后不足时拒绝整个触发
	if err := policy.Validate(actor); err != nil {
		return err
	}
	definition, err := validation.ResolvedDefinitionDigest(parsed)
	if err != nil {
		return err
	}
	slices.Sort(names)
	capture := &retryapproval.RunContext{Version: 1, ProjectID: pipeline.ProjectID, PipelineID: pipeline.PipelineID, PipelineRunID: run.RunID, ActorID: actor, DefinitionCommitSHA: run.DefinitionCommitSha, DefinitionPath: run.DefinitionPath, ResolvedDefinitionDigest: definition, GatedJobs: names, Policy: policy}
	run.RetryApprovalContext, err = capture.Canonical()
	if err != nil {
		return err
	}
	run.RetryApprovalContextDigest, err = capture.Digest()
	run.CommitSha = ""
	return err
}

func validateRetryApprovalDuplicate(ctx context.Context, req *pipelinev1.TriggerPipelineRequest, run *model.PipelineRun) error {
	if run.RetryApprovalContext == "" && run.RetryApprovalContextDigest == "" {
		return nil
	}
	capture, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		return fmt.Errorf("retry approval request identity unavailable")
	}
	digest, err := capture.Digest()
	if err != nil || digest != run.RetryApprovalContextDigest || verifiedPipelineActor(ctx) != capture.ActorID || req.GetTriggeredBy() != capture.ActorID || !maps.Equal(req.GetVariables(), serde.UnmarshalStringMap(run.Env)) || (req.GetExpectedDefinitionCommitSha() != "" && req.GetExpectedDefinitionCommitSha() != capture.DefinitionCommitSHA) {
		return fmt.Errorf("requestId belongs to another retry approval actor, definition or variables")
	}
	return nil
}

func validatePipelineTriggerDuplicate(ctx context.Context, req *pipelinev1.TriggerPipelineRequest, run *model.PipelineRun) error {
	if err := validateDeploymentDuplicate(ctx, req, run); err != nil {
		return err
	}
	return validateRetryApprovalDuplicate(ctx, req, run)
}

func submitPipelineRun(ctx context.Context, engine IPipelineEngine, run *model.PipelineRun, parsed *pipelinev1.Spec) error {
	if run.RetryApprovalContext == "" {
		return engine.Submit(run, parsed)
	}
	verified, ok := engine.(interface {
		SubmitVerified(context.Context, *model.PipelineRun, *pipelinev1.Spec) error
	})
	if !ok {
		return fmt.Errorf("verified retry approval submission unavailable")
	}
	return verified.SubmitVerified(ctx, run, parsed)
}

func createPipelineTriggerRun(ctx context.Context, repository repo.IPipelineRepository, run *model.PipelineRun) error {
	if run.RetryApprovalContext == "" {
		return repository.CreateRun(ctx, run)
	}
	creator, ok := repository.(interface {
		CreateRetryApprovalRun(context.Context, *model.PipelineRun) error
	})
	if !ok {
		return fmt.Errorf("durable retry approval run capture unavailable")
	}
	return creator.CreateRetryApprovalRun(ctx, run)
}
