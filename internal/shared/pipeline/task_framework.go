// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// TaskFramework handles task execution lifecycle
// Standard steps: prepare > create > start > queue > wait
type TaskFramework struct {
	execCtx       *ExecutionContext
	logger        log.Logger
	pluginManager *plugin.Manager
}

// NewTaskFramework creates a new task framework
func NewTaskFramework(
	execCtx *ExecutionContext,
	logger log.Logger,
) *TaskFramework {
	return &TaskFramework{
		execCtx:       execCtx,
		logger:        logger,
		pluginManager: execCtx.PluginManager,
	}
}

// Execute executes a task through the standard lifecycle
func (tf *TaskFramework) Execute(ctx context.Context, task *Task) (resultErr error) {
	if task == nil || task.Job == nil {
		return fmt.Errorf("task and job must not be nil")
	}
	tf = NewTaskFramework(tf.execCtx.ForJob(), tf.logger)
	tf.execCtx.Env = tf.execCtx.ResolveStepEnv(task.Job, nil)
	if task.Job.Timeout != "" {
		timeout, err := time.ParseDuration(task.Job.Timeout)
		if err != nil || timeout <= 0 {
			err := fmt.Errorf("invalid job timeout %q", task.Job.Timeout)
			task.MarkCompleted(TaskStateFailed, err)
			return err
		}

		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := tf.checkPause(ctx); err != nil {
		task.MarkCompleted(TaskStateFailed, err)
		return err
	}
	defer func() { tf.finishRecords(ctx, task, resultErr) }()
	if err := tf.createRecords(ctx, task); err != nil {
		task.MarkCompleted(TaskStateFailed, err)
		return err
	}

	// Prepare: validate and prepare task execution
	if err := tf.prepare(ctx, task); err != nil {
		task.MarkCompleted(TaskStateFailed, err)
		return fmt.Errorf("prepare task %s: %w", task.Name, err)
	}

	// Short-circuit when the job's `when` condition evaluated to false.
	if task.State == TaskStateSkipped {
		task.MarkCompleted(TaskStateSkipped, nil)
		tf.emitJobEvent(plugin.EventTypeJobCompleted, task, map[string]any{
			"status": "skipped",
		})
		tf.logger.Infow("task skipped", "task", task.Name)
		return nil
	}

	// Create: create task execution context
	if err := tf.create(ctx, task); err != nil {
		task.MarkCompleted(TaskStateFailed, err)
		return fmt.Errorf("create task %s: %w", task.Name, err)
	}

	// Start: start task execution
	if err := tf.start(ctx, task); err != nil {
		task.MarkCompleted(TaskStateFailed, err)
		tf.emitJobEvent(plugin.EventTypeJobFailed, task, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return fmt.Errorf("start task %s: %w", task.Name, err)
	}

	// Queue: queue task for execution (if needed)
	if err := tf.queue(ctx, task); err != nil {
		task.MarkCompleted(TaskStateFailed, err)
		return fmt.Errorf("queue task %s: %w", task.Name, err)
	}

	// Wait: wait for task completion
	if err := tf.wait(ctx, task); err != nil {
		if ctx.Err() == context.Canceled {
			task.MarkCompleted(TaskStateFailed, err)
			tf.emitJobEvent(plugin.EventTypeJobCancelled, task, map[string]any{
				"status": "cancelled",
			})
		} else {
			task.MarkCompleted(TaskStateFailed, err)
			tf.emitJobEvent(plugin.EventTypeJobFailed, task, map[string]any{
				"status": "failed",
				"error":  err.Error(),
			})
		}
		return fmt.Errorf("wait task %s: %w", task.Name, err)
	}

	// Backflow artifact URIs so downstream jobs can reference them.
	tf.backflowArtifactURIs(ctx, task)

	task.MarkCompleted(TaskStateSucceeded, nil)
	tf.emitJobEvent(plugin.EventTypeJobCompleted, task, map[string]any{
		"status": "succeeded",
	})
	return nil
}

// prepare validates and prepares task execution
func (tf *TaskFramework) prepare(_ context.Context, task *Task) error {
	task.State = TaskStatePrepared

	// Inject upstream job artifact URIs into this job's environment so that
	// steps can reference them as ${{ ARTIFACT_<jobName>_<artifactName> }}.
	if artifacts := tf.execCtx.ArtifactSnapshot(); len(artifacts) > 0 {
		for key, uri := range artifacts {
			tf.execCtx.Env["ARTIFACT_"+strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(key)] = uri
		}
	}

	// Evaluate when condition
	if task.Job.When != "" {
		jobContext := map[string]any{
			"job": map[string]any{
				"name":        task.Job.Name,
				"description": task.Job.Description,
			},
		}
		ok, err := tf.execCtx.EvalConditionWithContext(task.Job.When, jobContext)
		if err != nil {
			return fmt.Errorf("evaluate when condition: %w", err)
		}
		if !ok {
			task.State = TaskStateSkipped
			return nil
		}
	}

	tf.logger.Infow("task prepared", "task", task.Name)
	return nil
}

// create creates task execution context
func (tf *TaskFramework) create(ctx context.Context, task *Task) error {
	task.State = TaskStateCreated

	// Handle source if specified
	if task.Job.Source != nil && !isAgentJob(task.Job) {
		if err := tf.handleSource(ctx, task); err != nil {
			return fmt.Errorf("handle source: %w", err)
		}
	}

	// Handle approval if required
	if task.Job.Approval != nil && task.Job.Approval.Required {
		if err := tf.handleApproval(ctx, task); err != nil {
			return fmt.Errorf("approval failed: %w", err)
		}
	}

	tf.logger.Infow("task created", "task", task.Name)
	return nil
}

// start starts task execution
func (tf *TaskFramework) start(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	now := time.Now()
	task.StartedAt = &now
	task.State = TaskStateStarted
	if store := tf.getJobRunStore(); store != nil && !isAgentJob(task.Job) {
		if err := store.UpdateJobRun(ctx, tf.execCtx.JobRunID, map[string]any{"status": model.JobRunStatusRunning, "start_time": now}); err != nil {
			return err
		}
	}

	tf.logger.Infow("task started", "task", task.Name)
	tf.emitJobEvent(plugin.EventTypeJobStarted, task, map[string]any{
		"status": "started",
	})
	return nil
}

// isAgentJob returns true when the job should be dispatched to an Agent.
// A job is considered an agent job when any of its steps has RunOnAgent set.
func isAgentJob(job *spec.Job) bool {
	for _, s := range job.Steps {
		if s != nil && s.RunOnAgent {
			return true
		}
	}
	return false
}

// queue queues a task (job) for execution. For Agent jobs the entire Job is
// persisted as a JobRun/StepRun in the DB and enqueued to Kafka as a single
// message. Local jobs skip this phase entirely.
func (tf *TaskFramework) queue(ctx context.Context, task *Task) error {
	task.State = TaskStateQueued
	if !isAgentJob(task.Job) {
		return nil
	}
	if tf.execCtx.TaskQueue == nil {
		return fmt.Errorf("agent job requires a configured task queue")
	}
	if tf.getJobRunStore() == nil {
		return fmt.Errorf("agent job requires a job run store")
	}
	if tf.execCtx.SelectAgent == nil {
		return fmt.Errorf("agent selection is not configured")
	}
	agentID, err := tf.execCtx.SelectAgent(ctx, task.Job)
	if err != nil {
		return fmt.Errorf("select agent: %w", err)
	}
	task.Set("agentID", agentID)
	if err := tf.getJobRunStore().UpdateJobRun(ctx, tf.execCtx.JobRunID, map[string]any{"agent_id": agentID, "status": model.JobRunStatusQueued}); err != nil {
		return err
	}
	steps := make([]taskqueue.StepPayload, 0, len(task.Job.Steps))
	for i, step := range task.Job.Steps {
		if step == nil {
			continue
		}
		stepRunID := tf.execCtx.StepRunIDs[step.Name]
		if err := tf.getJobRunStore().UpdateStepRun(ctx, stepRunID, map[string]any{"agent_id": agentID, "status": 2}); err != nil {
			return err
		}
		steps = append(steps, taskqueue.StepPayload{Name: step.Name, StepIndex: int32(i), StepRunID: stepRunID, Uses: step.Uses, Action: step.Action,
			Args: tf.execCtx.ResolveVariables(spec.StructAsMap(step.Args)), Env: tf.execCtx.ResolveStepEnv(task.Job, step), ContinueOnError: step.ContinueOnError, Timeout: step.Timeout, When: step.When})
	}
	payload := &taskqueue.JobRunTaskPayload{PipelineID: tf.execCtx.PipelineIDRef, PipelineRunID: tf.execCtx.PipelineRunID,
		JobRunID: tf.execCtx.JobRunID, JobName: task.Job.Name, AgentID: agentID, Steps: steps,
		Env: tf.execCtx.ResolveStepEnv(task.Job, nil), Variables: tf.execCtx.Pipeline.Variables,
		Timeout: task.Job.Timeout, ArtifactURIs: tf.execCtx.ArtifactSnapshot(), ArtifactReferences: tf.execCtx.ArtifactReferenceSnapshot()}
	if source := task.Job.Source; source != nil {
		payload.Source = &taskqueue.SourcePayload{Type: source.Type, Repo: tf.execCtx.ResolveVariable(source.Repo), Branch: tf.execCtx.ResolveVariable(source.Branch)}
		if source.Auth != nil {
			payload.Source.Auth = map[string]string{"username": tf.execCtx.ResolveVariable(source.Auth.Username), "password": tf.execCtx.ResolveVariable(source.Auth.Password), "token": tf.execCtx.ResolveVariable(source.Auth.Token)}
		}
	}
	data, err := sonic.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal job run: %w", err)
	}
	if _, err = tf.execCtx.TaskQueue.Enqueue(&nova.Task{Type: taskqueue.TaskTypeJobRun, Payload: data}, nova.Queue(taskqueue.AgentQueue(agentID))); err != nil {
		return fmt.Errorf("enqueue job run: %w", err)
	}
	return nil
}

// wait waits for task completion. For Agent jobs it polls the DB for the
// JobRun terminal status; for local jobs it executes steps sequentially.
func (tf *TaskFramework) wait(ctx context.Context, task *Task) error {
	task.State = TaskStateRunning

	if isAgentJob(task.Job) {
		return tf.waitForAgentJob(ctx, task)
	}
	return tf.waitForLocalJob(ctx, task)
}

// waitForAgentJob polls the JobRun record in the DB until a terminal status
// is reached or the context is cancelled.
func (tf *TaskFramework) waitForAgentJob(ctx context.Context, task *Task) error {
	store := tf.getJobRunStore()
	if store == nil {
		return fmt.Errorf("job run store not available")
	}

	jobRunID, _ := task.Get("jobRunID")
	jrID, ok := jobRunID.(string)
	if !ok || jrID == "" {
		return fmt.Errorf("jobRunID not set on task %s", task.Name)
	}

	pollInterval := 2 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := store.GetJobRunStatus(ctx, jrID)
			if err != nil {
				tf.logger.Warnw("poll job run status failed", "jobRunId", jrID, "error", err)
				continue
			}
			if model.IsJobRunTerminal(status) {
				if status == model.JobRunStatusSuccess {
					tf.sendSuccessNotification(ctx, task)
					return nil
				}
				tf.sendFailureNotification(ctx, task)
				return fmt.Errorf("agent job %s finished with status %d", task.Job.Name, status)
			}
		}
	}
}

// waitForLocalJob executes steps sequentially in the control-plane process.
func (tf *TaskFramework) waitForLocalJob(ctx context.Context, task *Task) error {
	for i := range task.Job.Steps {
		step := task.Job.Steps[i]
		if step == nil {
			continue
		}
		if err := tf.checkPause(ctx); err != nil {
			return err
		}
		if err := tf.executeStep(ctx, task, step); err != nil {
			tf.sendFailureNotification(ctx, task)
			return fmt.Errorf("step %s failed: %w", step.Name, err)
		}
	}

	if task.Job.Target != nil {
		if err := tf.handleTarget(ctx, task); err != nil {
			return fmt.Errorf("handle target: %w", err)
		}
	}

	tf.sendSuccessNotification(ctx, task)
	return nil
}

// checkPause blocks if the pipeline run is paused.
func (tf *TaskFramework) checkPause(ctx context.Context) error {
	if tf.execCtx.RunCoordinator != nil {
		return tf.execCtx.RunCoordinator.WaitIfPaused(ctx)
	}
	return nil
}

func (tf *TaskFramework) sendSuccessNotification(ctx context.Context, task *Task) {
	if task.Job.Notify != nil && task.Job.Notify.OnSuccess != nil {
		_ = tf.execCtx.SendNotification(ctx, task.Job.Notify.OnSuccess, true)
	}
}

func (tf *TaskFramework) sendFailureNotification(ctx context.Context, task *Task) {
	if task.Job.Notify != nil && task.Job.Notify.OnFailure != nil {
		_ = tf.execCtx.SendNotification(ctx, task.Job.Notify.OnFailure, false)
	}
}

// executeStep executes a single step
func (tf *TaskFramework) executeStep(ctx context.Context, task *Task, step *spec.Step) error {
	return tf.executeStepOnce(ctx, task, step)
}

// executeStepOnce executes a step once
func (tf *TaskFramework) executeStepOnce(ctx context.Context, task *Task, step *spec.Step) error {
	start := time.Now()

	// Evaluate when condition
	if step.When != "" {
		stepContext := map[string]any{
			"env": tf.execCtx.ResolveStepEnv(task.Job, step),
			"job": map[string]any{
				"name": task.Job.Name,
			},
			"step": map[string]any{
				"name": step.Name,
			},
		}
		ok, err := tf.execCtx.EvalConditionWithContext(step.When, stepContext)
		if err != nil {
			tf.recordStep(ctx, task, step, 5, start, err)
			return fmt.Errorf("evaluate when condition: %w", err)
		}
		if !ok {
			tf.recordStep(ctx, task, step, 8, start, nil)
			return nil // Step skipped
		}
	}

	if store := tf.getJobRunStore(); store != nil {
		if err := store.UpdateStepRun(ctx, tf.execCtx.StepRunIDs[step.Name], map[string]any{"status": 3, "start_time": start, "workspace": tf.execCtx.JobWorkspace(task.Name)}); err != nil {
			return err
		}
	}
	tf.emitStepEvent(plugin.EventTypeStepStarted, task, step.Name, map[string]any{
		"status": "started",
	})

	// Create step runner and execute
	stepRunner := NewStepRunner(tf.execCtx, task.Job, step)
	if err := stepRunner.Run(ctx); err != nil {
		status := stepFailureStatus(ctx, err)
		tf.recordStep(ctx, task, step, status, start, err)
		if step.ContinueOnError && ctx.Err() == nil {
			tf.logger.Warnw("step failed but continuing", "task", task.Name, "step", step.Name, "error", err)
			tf.emitStepEvent(plugin.EventTypeStepFailed, task, step.Name, map[string]any{
				"status": "failed",
				"error":  err.Error(),
			})
			return nil
		}
		tf.emitStepEvent(plugin.EventTypeStepFailed, task, step.Name, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return err
	}

	tf.recordStep(ctx, task, step, 4, start, nil)
	tf.emitStepEvent(plugin.EventTypeStepCompleted, task, step.Name, map[string]any{
		"status": "completed",
	})

	return nil
}

func stepFailureStatus(ctx context.Context, err error) int {
	executionCause := ctx.Err()
	if executionCause == nil {
		executionCause = executor.ExecutionContextCause(err)
	}
	switch {
	case errors.Is(executionCause, context.DeadlineExceeded):
		return 7
	case errors.Is(executionCause, context.Canceled):
		return 6
	case errors.Is(err, executor.ErrBuildLogPersistence):
		return 5
	case errors.Is(err, context.DeadlineExceeded):
		return 7
	case errors.Is(err, context.Canceled):
		return 6
	default:
		return 5
	}
}

// handleSource handles source configuration by invoking the appropriate
// source plugin (e.g. "git") to populate the job workspace.
func (tf *TaskFramework) handleSource(ctx context.Context, task *Task) error {
	src := task.Job.Source
	if src == nil {
		return nil
	}

	workspace := tf.execCtx.JobWorkspace(task.Job.Name)
	sourceType := src.Type
	if sourceType == "" || sourceType == "git" {
		auth := scm.GitAuth{}
		if src.Auth != nil {
			auth = scm.NewGitAuthFromMap(map[string]string{"username": tf.execCtx.ResolveVariable(src.Auth.Username), "password": tf.execCtx.ResolveVariable(src.Auth.Password), "token": tf.execCtx.ResolveVariable(src.Auth.Token)})
		}
		return scm.CloneContext(ctx, scm.GitCloneRequest{Workdir: workspace, RepoURL: tf.execCtx.ResolveVariable(src.Repo), Branch: tf.execCtx.ResolveVariable(src.Branch), Auth: auth})
	}
	if tf.pluginManager == nil {
		return fmt.Errorf("source plugin manager is unavailable")
	}
	p, err := tf.pluginManager.GetPlugin(sourceType)
	if err != nil {
		return fmt.Errorf("source plugin %q not found: %w", sourceType, err)
	}

	params := map[string]any{
		"repo":      src.Repo,
		"branch":    src.Branch,
		"workspace": workspace,
	}
	paramsJSON, err := sonic.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal source params: %w", err)
	}

	action := "clone"
	if _, execErr := p.Execute(action, paramsJSON, nil); execErr != nil {
		return fmt.Errorf("source %s clone: %w", sourceType, execErr)
	}

	tf.logger.Infow("source cloned", "job", task.Name, "type", sourceType, "workspace", workspace)
	return nil
}

// jobRunStoreIface is the subset of IJobRunStore that TaskFramework needs.
type jobRunStoreIface interface {
	CreateJobRun(ctx context.Context, jr *model.JobRun) error
	GetJobRun(ctx context.Context, jobRunID string) (*model.JobRun, error)
	GetJobRunStatus(ctx context.Context, jobRunID string) (int, error)
	UpdateJobRun(ctx context.Context, jobRunID string, updates map[string]any) error
	CreateStepRun(ctx context.Context, sr *model.StepRun) error
	UpdateStepRun(ctx context.Context, stepRunID string, updates map[string]any) error
}

// getJobRunStore extracts the IJobRunStore from ExecutionContext.
// Returns nil when the process layer did not inject one (e.g. standalone library usage).
func (tf *TaskFramework) getJobRunStore() jobRunStoreIface {
	if tf.execCtx.JobRunStore == nil {
		return nil
	}
	store, ok := tf.execCtx.JobRunStore.(jobRunStoreIface)
	if !ok {
		return nil
	}
	return store
}

// backflowArtifactURIs reads the completed job's artifact URIs from the DB
// and writes them into execCtx.ArtifactURIs so downstream jobs can consume them.
func (tf *TaskFramework) backflowArtifactURIs(ctx context.Context, task *Task) {
	jobRunIDVal, ok := task.Get("jobRunID")
	if !ok {
		return
	}
	jrID, ok := jobRunIDVal.(string)
	if !ok || jrID == "" {
		return
	}
	store := tf.getJobRunStore()
	if store == nil {
		return
	}
	jr, err := store.GetJobRun(ctx, jrID)
	if err != nil || jr == nil {
		tf.logger.Warnw("failed to read job run for artifact backflow", "jobRunId", jrID, "error", err)
		return
	}
	if jr.ArtifactURIs == "" {
		return
	}
	var uris map[string]string
	if err := sonic.UnmarshalString(jr.ArtifactURIs, &uris); err != nil {
		tf.logger.Warnw("failed to unmarshal artifact_uris", "jobRunId", jrID, "error", err)
		return
	}
	var references map[string]taskqueue.ArtifactReference
	if jr.ArtifactReferences != "" {
		if err := sonic.UnmarshalString(jr.ArtifactReferences, &references); err != nil {
			tf.logger.Warnw("invalid artifact storage references", "jobRunId", jrID)
			return
		}
	}
	for key, uri := range uris {
		tf.execCtx.RecordArtifact(task.Job.Name+"/"+key, uri, references[key])
	}
}

// handleApproval handles approval configuration by creating an approval request
// via ApprovalManager, then blocking until the request is approved/rejected/expired.
func (tf *TaskFramework) handleApproval(ctx context.Context, task *Task) error {
	approval := task.Job.Approval
	if approval == nil {
		return nil
	}

	am := NewApprovalManager(tf.pluginManager, tf.logger)
	if tf.execCtx.EventEmitter != nil {
		am.SetEventEmitter(tf.execCtx.EventEmitter)
	}

	params := make(map[string]any)
	if approval.Params != nil {
		params = spec.StructAsMap(approval.Params)
	}

	req, err := am.CreateApproval(ctx, task.Job.Name, "", approval.Plugin, params)
	if err != nil {
		return fmt.Errorf("create approval: %w", err)
	}

	tf.logger.Infow("waiting for approval", "task", task.Name, "approvalId", req.ID)

	approved, err := am.WaitForApproval(ctx, req.ID)
	if err != nil {
		return fmt.Errorf("wait for approval: %w", err)
	}
	if !approved {
		return fmt.Errorf("approval rejected for job %s", task.Job.Name)
	}

	tf.logger.Infow("approval granted", "task", task.Name, "approvalId", req.ID)
	return nil
}

// handleTarget handles target deployment
func (tf *TaskFramework) handleTarget(ctx context.Context, task *Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target := task.Job.Target
	if target == nil {
		return nil
	}
	if tf.pluginManager == nil || target.Type == "" {
		return fmt.Errorf("deployment target plugin is not configured")
	}
	client, err := tf.pluginManager.GetPlugin(target.Type)
	if err != nil {
		return fmt.Errorf("deployment target plugin %q: %w", target.Type, err)
	}
	params, err := sonic.Marshal(tf.execCtx.ResolveVariables(spec.StructAsMap(target.Config)))
	if err != nil {
		return err
	}
	opts, err := sonic.Marshal(map[string]any{"workspace": tf.execCtx.JobWorkspace(task.Name), "env": tf.execCtx.ResolveStepEnv(task.Job, nil)})
	if err != nil {
		return err
	}
	result, err := client.Execute("deploy", params, opts)
	if err != nil {
		return err
	}
	return checkPluginResult(result)
}
