// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package pipeline

import (
	"github.com/arcentrix/arcentra/internal/shared/pipeline/materialize"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func (c *ExecutionContext) PrepareJobEnvironment(job *spec.Job) {
	c.Env = c.ResolveStepEnv(job, nil)
	c.jobEnvResolved = true
}
func (c *ExecutionContext) materializationContext() *materialize.Context {
	uris, refs := c.ArtifactInputSnapshot()
	m := &materialize.Context{Pipeline: c.Pipeline, Env: c.Env, PipelineIDRef: c.PipelineIDRef, PipelineRunID: c.PipelineRunID, JobRunID: c.JobRunID, Runtime: c.Runtime, RuntimePolicy: c.RuntimePolicy, RetryApprovalTimeoutSeconds: c.RetryApprovalTimeoutSeconds, StepRunIDs: c.StepRunIDs, StepAttemptIDs: c.StepAttemptIDs, ArtifactURIs: uris, ArtifactReferences: refs}
	if c.jobEnvResolved {
		m.MarkPrepared()
	}
	return m
}
func (c *ExecutionContext) BuildAgentJobPayload(job *spec.Job, agent string) (*taskqueue.JobRunTaskPayload, *sourceprovenance.Binding, error) {
	return c.materializationContext().BuildAgentJobPayload(job, agent)
}
func (c *ExecutionContext) buildAgentJobPayload(job *spec.Job, agent string, source *taskqueue.SourcePayload) (*taskqueue.JobRunTaskPayload, error) {
	return c.materializationContext().BuildPayloadWithSource(job, agent, source)
}
