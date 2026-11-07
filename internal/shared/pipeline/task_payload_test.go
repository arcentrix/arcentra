// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package pipeline

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/materialize"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type payloadCaptureStore struct{ jobRunStoreIface }

func (*payloadCaptureStore) CaptureSourceExpectation(context.Context, string, string, string, string, *sourceprovenance.Binding) error {
	return nil
}

type payloadCaptureQueue struct {
	nova.TaskQueue
	body []byte
}

func (q *payloadCaptureQueue) Enqueue(task *nova.Task, _ ...nova.Option) (*nova.Result, error) {
	q.body = append([]byte(nil), task.Payload...)
	return &nova.Result{}, nil
}

func TestAgentPayloadPureMaterializationMatchesOrdinaryQueue(t *testing.T) {
	p := &spec.Pipeline{Runtime: &spec.Runtime{Env: map[string]string{"VALUE": "runtime", "EMPTY": "x"}}, Variables: map[string]string{"VALUE": "pipeline", "SINGLE": "${UNKNOWN}"}}
	job := &spec.Job{Name: "job", Env: map[string]string{"VALUE": "${VALUE}-job", "EMPTY": ""}, Source: &spec.Source{Type: "git", Repo: "https://example.com/repo.git", Branch: "${VALUE}"}, Steps: []*spec.Step{{Name: "shell", Uses: "shell", RunOnAgent: true, Env: map[string]string{"VALUE": "${VALUE}-step", "SINGLE": "${SINGLE}"}, Args: spec.MapAsStruct(map[string]any{"script": "${VALUE}|${EMPTY}|${MISSING}|${SINGLE}", "nested": []any{map[string]any{"v": "${VALUE}"}}})}}}
	c := NewExecutionContext(p, nil, "", log.Logger{SugaredLogger: zap.NewNop().Sugar()})
	// Same authorized run-level precedence as Coordinator.executeDAG.
	maps.Copy(c.Env, map[string]string{"VALUE": "trigger"})
	c.Pipeline.Variables = maps.Clone(c.Env)
	c.PipelineIDRef = "pipeline"
	c.PipelineRunID = "run"
	c.JobRunID = "job-id"
	c.StepRunIDs = map[string]string{"shell": "step"}
	_, _, err := c.BuildAgentJobPayload(job, "agent")
	require.Error(t, err)
	c.PrepareJobEnvironment(job)
	payload, binding, err := c.BuildAgentJobPayload(job, "agent")
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, "trigger-job", payload.Env["VALUE"])
	require.Equal(t, "trigger-job-step||${MISSING}|${UNKNOWN}", payload.Steps[0].Args["script"])
	require.Equal(t, "trigger-job", payload.Source.Branch)
	require.Equal(t, "trigger", payload.Variables["VALUE"])
	require.Empty(t, payload.Env["EMPTY"])
	require.Equal(t, "trigger-job", c.Env["VALUE"])
	expected, err := sonic.Marshal(payload)
	require.NoError(t, err)
	q := &payloadCaptureQueue{}
	c.TaskQueue = q
	c.JobRunStore = &payloadCaptureStore{}
	c.SelectAgent = func(context.Context, *spec.Job) (string, error) { return "agent", nil }
	tf := &TaskFramework{execCtx: c}
	require.NoError(t, tf.queue(context.Background(), &Task{Job: job}))
	var decoded any
	require.NoError(t, json.Unmarshal(q.body, &decoded))
	var expectedDecoded any
	require.NoError(t, json.Unmarshal(expected, &expectedDecoded))
	canonicalExpected, _ := json.Marshal(expectedDecoded)
	canonicalActual, _ := json.Marshal(decoded)
	require.Equal(t, string(canonicalExpected), string(canonicalActual))
	// Repeated materialization does not apply the job layer a second time.
	repeated, _, err := c.BuildAgentJobPayload(job, "agent")
	require.NoError(t, err)
	require.Equal(t, payload, repeated)
}

func TestAgentPayloadPureSourceValidationPreserved(t *testing.T) {
	for _, source := range []*spec.Source{{Type: "git", Repo: "https://example.com/${secrets.token}.git"}, {Type: "git", Repo: "https://user:secret-value@example.com/repo"}, {Type: "svn", Repo: "https://example.com/repo"}} {
		c := NewExecutionContext(&spec.Pipeline{}, nil, "", log.Logger{SugaredLogger: zap.NewNop().Sugar()})
		c.Env["secrets.token"] = "secret-value"
		c.PipelineIDRef = "pipeline"
		c.PipelineRunID = "run"
		c.JobRunID = "job"
		job := &spec.Job{Source: source}
		c.PrepareJobEnvironment(job)
		_, _, err := c.BuildAgentJobPayload(job, "agent")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret-value")
	}
}

func TestAgentPayloadPureWholeJobPlanParity(t *testing.T) {
	framework, task, _, queue := runtimeDispatchFixture(t)
	c := framework.execCtx
	c.RuntimePolicy.AllowWholeJobRetry = true
	c.RuntimePolicy.AllowWholeJobRetryApproval = true
	c.RetryApprovalTimeoutSeconds = 60
	task.Job.Retry = &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
	task.Job.Approval = &spec.Approval{Required: true, Type: "manual", Plugin: "builtin:retry-approval"}
	c.StepAttemptIDs = map[string][]string{"shell": {"first", "second"}}
	c.StepRunIDs = map[string]string{"shell": "first"}
	c.PrepareJobEnvironment(task.Job)
	pure := materialize.NewAgentPayloadContext(proto.Clone(c.Pipeline).(*spec.Pipeline), nil)
	pure.PipelineIDRef = c.PipelineIDRef
	pure.PipelineRunID = c.PipelineRunID
	pure.JobRunID = c.JobRunID
	pure.Runtime = c.Runtime
	pure.RuntimePolicy = c.RuntimePolicy
	pure.RetryApprovalTimeoutSeconds = 60
	pure.StepRunIDs = c.StepRunIDs
	pure.StepAttemptIDs = c.StepAttemptIDs
	pure.PrepareJobEnvironment(task.Job)
	expected, _, err := pure.BuildAgentJobPayload(task.Job, "agent")
	require.NoError(t, err)
	require.NoError(t, framework.queue(context.Background(), task))
	var actual taskqueue.JobRunTaskPayload
	require.NoError(t, json.Unmarshal(queue.data, &actual))
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	actualJSON, err := json.Marshal(&actual)
	require.NoError(t, err)
	require.Equal(t, string(expectedJSON), string(actualJSON))
	require.Equal(t, expected.RetryPlanDigest, actual.RetryPlanDigest)
	require.Equal(t, []string{"first", "second"}, actual.Steps[0].AttemptRunIDs)
	require.NoError(t, taskqueue.ValidateJobRetryPlan(&actual))
}
