// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"
)

// CopilotRerunCapturePolicy is startup-only. Its zero value disables capture;
// enabling capture does not enable rerun APIs, claims, or dispatch.
type CopilotRerunCapturePolicy string

const (
	CopilotRerunCaptureOff                    CopilotRerunCapturePolicy = "off"
	CopilotRerunCaptureEligibleVerifiedManual CopilotRerunCapturePolicy = "eligible_verified_manual"
)

func (s *Services) ConfigureCopilotRerunCapture(policy CopilotRerunCapturePolicy) error {
	if s == nil || (policy != "" && policy != CopilotRerunCaptureOff && policy != CopilotRerunCaptureEligibleVerifiedManual) {
		return errCopilotDefinitionCapture
	}
	s.rerunCapturePolicy = policy
	return nil
}

// makeCopilotDefinitionReceipt consumes the original trigger's already loaded,
// expanded and validated definition. It never fetches, expands, or parses a spec.
func makeCopilotDefinitionReceipt(p *model.Pipeline, q *model.Project, original, resolved, revision string, parsed *pipelinev1.Spec, variables map[string]string) (*copilotDefinitionLoadReceipt, error) {
	if p == nil || q == nil || parsed == nil || original == "" || resolved == "" || len(original) > repo.CopilotRerunMaxDefinitionBytes || len(resolved) > repo.CopilotRerunMaxDefinitionBytes || len(revision) != 40 || strings.ToLower(revision) != revision || !sourceprovenance.ValidCommit(revision) {
		return nil, errCopilotDefinitionCapture
	}
	config, err := repo.CopilotDefinitionRepositoryConfigDigest(p, q)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	repository, err := sourceprovenance.CanonicalRepository(p.RepoURL)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(parsed)
	if err != nil || len(encoded) > repo.CopilotRerunMaxDefinitionBytes {
		return nil, errCopilotDefinitionCapture
	}
	variables = maps.Clone(variables)
	if variables == nil {
		variables = map[string]string{}
	}
	vars, err := json.Marshal(variables)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	if _, err = repo.CopilotRerunTriggerVariablesDigest(string(vars)); err != nil {
		return nil, errCopilotDefinitionCapture
	}
	return &copilotDefinitionLoadReceipt{projectID: q.ProjectID, pipelineID: p.PipelineID, repository: repository, path: p.PipelineFilePath, revision: revision, configDigest: config, originalContentDigest: definitionBytesDigest("arcentra-definition-loaded-v1", []byte(original)), resolvedContentDigest: definitionBytesDigest("arcentra-definition-resolved-v1", []byte(resolved)), resolvedSpec: bytes.Clone(encoded), triggerVariables: variables}, nil
}

// A nonempty reason means ordinary execution remains supported but no prospective
// snapshot was requested. Once eligible, any receipt/crypto failure is an error.
// Reasons contain no user-authored definition, credential or variable text.
func (s *PipelineServiceImpl) prospectiveDefinitionCapture(ctx context.Context, req *pipelinev1.TriggerPipelineRequest, p *model.Pipeline, q *model.Project, run *model.PipelineRun, parsed *pipelinev1.Spec, original, resolved string) (*model.CopilotDefinitionSnapshot, string, error) {
	if s.services == nil || s.services.rerunCapturePolicy == "" || s.services.rerunCapturePolicy == CopilotRerunCaptureOff {
		return nil, "capture_disabled", nil
	}
	if s.services.rerunCapturePolicy != CopilotRerunCaptureEligibleVerifiedManual {
		return nil, "", errCopilotDefinitionCapture
	}
	actor := VerifiedPipelineActor(ctx)
	if actor == "" || req.GetTriggeredBy() != actor {
		return nil, "verified_manual_required", nil
	}
	if run.RetryApprovalContext == "" {
		return nil, "unsupported_definition", nil
	}
	captured, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		return nil, "", errCopilotDefinitionCapture
	}
	if _, err := copilotDefinitionLayout(parsed, s.offlineRuntimePolicy, captured.Policy.TimeoutSeconds); err != nil {
		return nil, "unsupported_definition", nil
	}
	if len(copilotDefinitionSecretReferences(parsed, req.GetVariables())) != 0 {
		return nil, "managed_secret_references_unsupported", nil
	}
	// Legacy ordinary loading skips includes without a resolver. Do not attest to
	// expansion when the original document contains any include directive.
	if s.templateResolver == nil {
		var doc map[string]any
		if yaml.Unmarshal([]byte(original), &doc) != nil {
			return nil, "definition_provenance_unavailable", nil
		}
		if _, ok := doc["include"]; ok {
			return nil, "definition_provenance_unavailable", nil
		}
	}
	receipt, err := makeCopilotDefinitionReceipt(p, q, original, resolved, run.DefinitionCommitSha, parsed, req.GetVariables())
	if err != nil {
		return nil, "", err
	}
	snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, s.offlineRuntimePolicy, s.services.Secret, time.Now())
	if err != nil {
		return nil, "", errCopilotDefinitionCapture
	}
	return snapshot, "", nil
}
func createPipelineTriggerRunWithDefinition(ctx context.Context, repository repo.IPipelineRepository, run *model.PipelineRun, snapshot *model.CopilotDefinitionSnapshot) error {
	if snapshot == nil {
		return createPipelineTriggerRun(ctx, repository, run)
	}
	actor := VerifiedPipelineActor(ctx)
	creator, ok := repository.(repo.IVerifiedManualDefinitionCreator)
	if actor == "" || actor != run.TriggeredBy || !ok {
		return repo.ErrCopilotRerunUnavailable
	}
	return creator.CreateVerifiedManualTriggerRunWithDefinition(ctx, actor, run, snapshot, authorizeManualInboxTrigger)
}
