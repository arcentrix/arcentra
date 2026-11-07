// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	sharedpipeline "github.com/arcentrix/arcentra/internal/shared/pipeline/materialize"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

type copilotRerunMaterial struct {
	Reviewed *repo.CopilotRerunReviewedTarget
	Payload  *taskqueue.JobRunTaskPayload
}
type copilotRerunMaterializer func(context.Context, *repo.Repositories, *repo.CopilotRerunScope, *model.CopilotDefinitionSnapshot, *repo.CopilotRerunOriginalEvidence, *repo.CopilotRerunExecutionIdentities, *model.CopilotRerunIntent, *model.Agent, string) (*copilotRerunMaterial, error)

// Internal, inert builder. It consumes only locked target metadata and the
// authenticated frozen snapshot; it neither loads definitions nor queues work.
func newCopilotRerunMaterializer(config *retryapproval.Policy, runtime taskqueue.OfflineRuntimePolicy, secrets *SecretService) (copilotRerunMaterializer, error) {
	policy, err := normalizeRetryApprovalPolicy(config)
	if err != nil || policy == nil || runtime.Validate() != nil || !runtime.Enabled || !runtime.AllowWholeJobRetry || !runtime.AllowWholeJobRetryApproval || secrets == nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	_, err = newCopilotRerunCipher(secrets.encryptKey)
	if err != nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	runtime = runtime.Clone()
	key := &SecretService{encryptKey: slices.Clone(secrets.encryptKey)}
	return func(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, ids *repo.CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, sealID string) (*copilotRerunMaterial, error) {
		fail := func() (*copilotRerunMaterial, error) { return nil, repo.ErrCopilotRerunUnsupported }
		if ctx.Err() != nil || view == nil || scope == nil || scope.Project == nil || scope.Pipeline == nil || scope.Run == nil || scope.Job == nil || snapshot == nil || evidence == nil || ids == nil || intent == nil || agent == nil {
			return fail()
		}
		if scope.Project.ProjectID != snapshot.ProjectID || scope.Pipeline.PipelineID != snapshot.PipelineID || scope.Pipeline.ProjectID != snapshot.ProjectID || intent.ProjectID != snapshot.ProjectID || intent.PipelineID != snapshot.PipelineID || intent.SnapshotID != snapshot.SnapshotID || intent.SnapshotDigest != snapshot.BindingDigest || intent.OriginalRunID != snapshot.RunID || evidence.SnapshotID != snapshot.SnapshotID || evidence.SnapshotDigest != snapshot.BindingDigest || evidence.Identity.PipelineRunID != intent.OriginalRunID || evidence.Identity.JobRunID != intent.OriginalJobRunID || evidence.Identity.PipelineID != intent.PipelineID || evidence.SourceCommit != intent.ExpectedSourceCommit || !time.Now().Before(intent.ExpiresAt) || !time.Now().Before(snapshot.ExpiresAt) {
			return fail()
		}
		currentConfig, err := repo.CopilotDefinitionRepositoryConfigDigest(scope.Pipeline, scope.Project)
		if err != nil || currentConfig != snapshot.RepositoryConfigDigest {
			return nil, repo.ErrCopilotRerunConflict
		}
		evidenceDigest, err := evidence.Digest()
		if err != nil || evidenceDigest != intent.OriginalEvidenceDigest {
			return fail()
		}
		encodedEvidence, err := json.Marshal(evidence)
		if err != nil || string(encodedEvidence) != intent.OriginalEvidenceJSON {
			return fail()
		}
		if scope.Run.RunID != ids.RunID || scope.Job.JobRunID != ids.JobRunID || scope.Job.PipelineRunID != ids.RunID || scope.Run.ProjectID != intent.ProjectID || scope.Run.PipelineID != intent.PipelineID || scope.Job.PipelineID != intent.PipelineID || intent.NewRunID != ids.RunID || intent.NewJobRunID != ids.JobRunID || intent.ScheduleID != ids.ScheduleID || intent.GateID != ids.GateID || scope.Run.TriggeredBy != intent.OwnerUserID || scope.Run.RetryApprovalContext != intent.ReviewedRunContext || scope.Run.RetryApprovalContextDigest != intent.ReviewedRunContextDigest {
			return fail()
		}
		parsed, variables, refs, err := openCopilotDefinitionSnapshot(snapshot, runtime, key)
		if err != nil || len(refs) != 0 {
			return fail()
		}
		if !copilotRerunSealAgentEligible(agent, parsed.Jobs[0]) {
			return fail()
		}
		layout, err := copilotDefinitionLayout(parsed, runtime, policy.TimeoutSeconds)
		if err != nil || ids.Validate(&layout) != nil {
			return fail()
		}
		layoutJSON, err := layout.Canonical()
		if err != nil || layoutJSON != snapshot.LayoutJSON {
			return fail()
		}
		selected, err := copilotRerunCurrentApprovalPolicy(ctx, view, intent.OwnerUserID, intent.ProjectID, intent.PipelineID, policy)
		if err != nil {
			return nil, err
		}
		fresh := retryapproval.RunContext{Version: 1, ProjectID: intent.ProjectID, PipelineID: intent.PipelineID, PipelineRunID: ids.RunID, ActorID: intent.OwnerUserID, DefinitionCommitSHA: snapshot.DefinitionRevision, DefinitionPath: snapshot.DefinitionPath, ResolvedDefinitionDigest: snapshot.ResolvedSpecDigest, GatedJobs: []string{snapshot.JobName}, Policy: *selected}
		body, err := fresh.Canonical()
		if err != nil {
			return fail()
		}
		digest, err := fresh.Digest()
		if err != nil {
			return fail()
		}
		admission, err := json.Marshal(struct {
			Configured retryapproval.Policy
			Selected   retryapproval.Policy
			Runtime    taskqueue.OfflineRuntimePolicy
		}{*policy, *selected, runtime})
		if err != nil {
			return fail()
		}
		reviewed := &repo.CopilotRerunReviewedTarget{RunContext: body, RunContextDigest: digest, AdmissionPolicyDigest: definitionBytesDigest("arcentra-rerun-reviewed-admission-v1", admission), ExpectedSourceCommit: intent.ExpectedSourceCommit, Layout: &layout}
		if body != intent.ReviewedRunContext || digest != intent.ReviewedRunContextDigest || reviewed.AdmissionPolicyDigest != intent.ReviewedAdmissionPolicyDigest {
			return nil, repo.ErrCopilotRerunConflict
		}
		c := sharedpipeline.NewAgentPayloadContext(parsed, variables)
		c.PipelineIDRef = intent.PipelineID
		c.PipelineRunID = ids.RunID
		c.JobRunID = ids.JobRunID
		c.Runtime = &layout.Runtime
		c.RuntimePolicy = runtime
		c.RetryApprovalTimeoutSeconds = layout.ApprovalTimeoutSeconds
		c.StepRunIDs = map[string]string{}
		c.StepAttemptIDs = map[string][]string{}
		for _, step := range ids.Steps {
			c.StepRunIDs[step.Name] = step.Attempts[0]
			c.StepAttemptIDs[step.Name] = slices.Clone(step.Attempts)
		}
		c.PrepareJobEnvironment(parsed.Jobs[0])
		payload, source, err := c.BuildAgentJobPayload(parsed.Jobs[0], agent.AgentID)
		if err != nil || source == nil || source.Type != evidence.Source.Type || source.Repository != evidence.Source.Repository || source.Ref != evidence.Source.Ref {
			return fail()
		}
		// Empty auth is credential-free but native Git rejects map presence.
		// Normalize only this rerun payload after frozen layout rejected all values.
		for _, value := range payload.Source.Auth {
			if value != "" {
				return fail()
			}
		}
		payload.Source.Auth = nil
		payload.SourceExpectation = &taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: ids.ScheduleID, SealID: sealID, ExpectedSourceCommit: intent.ExpectedSourceCommit}
		if _, err = payload.SourceExpectation.Canonical(); err != nil {
			return fail()
		}
		// The new expectation is part of the complete dispatch definition. Rebind
		// the plan after adding it, before computing payload/AAD digests.
		payload.RetryPlan, err = taskqueue.NewJobRetryPlan(payload)
		if err != nil {
			return fail()
		}
		payload.RetryPlanDigest, err = payload.RetryPlan.Digest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, payload.Runtime.BindingDigest)
		if err != nil {
			return fail()
		}
		if taskqueue.ValidateJobRuntime(payload, runtime) != nil {
			return fail()
		}
		return &copilotRerunMaterial{Reviewed: reviewed, Payload: payload}, nil
	}, nil
}
