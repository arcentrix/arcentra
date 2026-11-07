// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package materialize

import (
	"fmt"
	"maps"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// NewAgentPayloadContext creates only materialization state. The supplied parsed
// spec is private to the caller; its Variables becomes the ordinary run snapshot.
// Precedence matches NewContext then Coordinator.executeDAG.
func NewAgentPayloadContext(p *spec.Pipeline, triggerVariables map[string]string) *Context {
	env := make(map[string]string)
	if p != nil && p.Runtime != nil {
		maps.Copy(env, p.Runtime.Env)
	}
	if p != nil {
		maps.Copy(env, p.Variables)
	}
	maps.Copy(env, triggerVariables)
	if p != nil {
		p.Variables = maps.Clone(env)
	}
	return &Context{Pipeline: p, Env: env, ArtifactURIs: make(map[string]string), ArtifactReferences: make(map[string]taskqueue.ArtifactReference)}
}

// PrepareJobEnvironment applies the ordinary job layer exactly once. Callers
// supply an isolated per-job context with the authorized run-level environment.
// No source access, state persistence or external action occurs here.
func (c *Context) PrepareJobEnvironment(job *spec.Job) {
	c.Env = c.ResolveStepEnv(job, nil)
	c.jobEnvResolved = true
}

// BuildAgentJobPayload materializes the ordinary Agent payload without selecting
// an Agent, accessing a repository, capturing expectations or enqueueing work.
// The context must already have its job environment prepared exactly once.
func (c *Context) BuildAgentJobPayload(job *spec.Job, agentID string) (*taskqueue.JobRunTaskPayload, *sourceprovenance.Binding, error) {
	if c == nil || c.Pipeline == nil || job == nil || !c.jobEnvResolved {
		return nil, nil, fmt.Errorf("prepared job context required")
	}
	source, binding, err := c.ResolveSource(job)
	if err != nil {
		return nil, nil, err
	}
	payload, err := c.BuildPayloadWithSource(job, agentID, source)
	return payload, binding, err
}

func (c *Context) BuildPayloadWithSource(job *spec.Job, agentID string, source *taskqueue.SourcePayload) (*taskqueue.JobRunTaskPayload, error) {
	var err error
	steps := make([]taskqueue.StepPayload, 0, len(job.Steps))
	for i, step := range job.Steps {
		if step == nil {
			continue
		}
		stepRunID := c.StepRunIDs[step.Name]
		stepCtx := *c
		stepCtx.Env = c.ResolveStepEnv(job, step)
		steps = append(steps, taskqueue.StepPayload{AttemptRunIDs: c.StepAttemptIDs[step.Name], Name: step.Name, StepIndex: int32(i), StepRunID: stepRunID, Uses: step.Uses, Action: step.Action,
			Args: stepCtx.ResolveVariables(spec.StructAsMap(step.Args)), Env: maps.Clone(stepCtx.Env), ContinueOnError: step.ContinueOnError, Timeout: step.Timeout, When: step.When})
	}
	artifactURIs, artifactReferences := c.ArtifactURIs, c.ArtifactReferences
	payload := &taskqueue.JobRunTaskPayload{PipelineID: c.PipelineIDRef, PipelineRunID: c.PipelineRunID,
		JobRunID: c.JobRunID, JobName: job.Name, AgentID: agentID, Steps: steps, Retry: job.Retry,
		Env: c.ResolveStepEnv(job, nil), Variables: c.Pipeline.Variables,
		Timeout: job.Timeout, ArtifactURIs: artifactURIs, ArtifactReferences: artifactReferences}
	payload.Source = source
	payload.RetryApprovalRequired = validation.IsDurableRetryApproval(job, c.RuntimePolicy)
	if payload.RetryApprovalRequired {
		payload.RetryApprovalTimeoutSeconds = c.RetryApprovalTimeoutSeconds
	}
	if c.Runtime != nil {
		runtime := *c.Runtime
		runtime.BindingDigest, err = taskqueue.RuntimeBindingDigest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, &runtime)
		if err != nil {
			return nil, err
		}
		payload.Runtime = &runtime
		if payload.Retry != nil {
			plan, planErr := taskqueue.NewJobRetryPlan(payload)
			if planErr != nil {
				return nil, planErr
			}
			payload.RetryPlan = plan
			payload.RetryPlanDigest, planErr = plan.Digest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, payload.Runtime.BindingDigest)
			if planErr != nil {
				return nil, planErr
			}
		}
		if err := taskqueue.ValidateJobRuntime(payload, c.RuntimePolicy); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

// Context contains materialization data only; it holds no service or I/O handles.
type Context struct {
	Pipeline                               *spec.Pipeline
	Env                                    map[string]string
	jobEnvResolved                         bool
	PipelineIDRef, PipelineRunID, JobRunID string
	Runtime                                *taskqueue.RuntimeRequest
	RuntimePolicy                          taskqueue.OfflineRuntimePolicy
	RetryApprovalTimeoutSeconds            int64
	StepRunIDs                             map[string]string
	StepAttemptIDs                         map[string][]string
	ArtifactURIs                           map[string]string
	ArtifactReferences                     map[string]taskqueue.ArtifactReference
}

// MarkPrepared is used by the ordinary pipeline wrapper after its job layer.
func (c *Context) MarkPrepared() { c.jobEnvResolved = true }
