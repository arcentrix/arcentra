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
	"encoding/json"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/retry"
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
func (tf *TaskFramework) Execute(ctx context.Context, task *Task) error {
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

	// Apply timeout if specified
	waitCtx := ctx
	var cancel context.CancelFunc
	if task.Job.Timeout != "" {
		timeout, err := time.ParseDuration(task.Job.Timeout)
		if err != nil {
			task.MarkCompleted(TaskStateFailed, err)
			return fmt.Errorf("invalid timeout format: %w", err)
		}
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Wait: wait for task completion
	if err := tf.wait(waitCtx, task); err != nil {
		if waitCtx.Err() == context.Canceled {
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
	if len(tf.execCtx.ArtifactURIs) > 0 {
		for key, uri := range tf.execCtx.ArtifactURIs {
			tf.execCtx.Env["ARTIFACT_"+key] = uri
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
	if task.Job.Source != nil {
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
func (tf *TaskFramework) start(_ context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	now := time.Now()
	task.StartedAt = &now
	task.State = TaskStateStarted

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
	tf.logger.Infow("task queued", "task", task.Name)

	if tf.execCtx.TaskQueue == nil || !isAgentJob(task.Job) {
		return nil
	}

	store := tf.getJobRunStore()
	if store == nil {
		return nil
	}

	jobRunID := id.GetUild()
	task.Set("jobRunID", jobRunID)

	envJSON, _ := sonic.MarshalString(tf.execCtx.ResolveStepEnv(task.Job, nil))
	jr := &model.JobRun{
		JobRunID:      jobRunID,
		PipelineID:    tf.execCtx.PipelineIDRef,
		PipelineRunID: tf.execCtx.PipelineRunID,
		JobName:       task.Job.Name,
		Status:        model.JobRunStatusQueued,
		Priority:      5,
		Env:           envJSON,
		Workspace:     tf.execCtx.JobWorkspace(task.Job.Name),
		Timeout:       task.Job.Timeout,
		TotalSteps:    len(task.Job.Steps),
	}
	if err := store.CreateJobRun(ctx, jr); err != nil {
		return fmt.Errorf("create job run: %w", err)
	}

	stepPayloads := make([]taskqueue.StepPayload, 0, len(task.Job.Steps))
	for i, step := range task.Job.Steps {
		if step == nil {
			continue
		}
		stepRunID := id.GetUild()
		sr := &model.StepRun{
			StepRunID:     stepRunID,
			PipelineID:    tf.execCtx.PipelineIDRef,
			PipelineRunID: tf.execCtx.PipelineRunID,
			JobID:         fmt.Sprintf("%s-%s", tf.execCtx.PipelineIDRef, task.Job.Name),
			JobRunID:      jobRunID,
			Name:          step.Name,
			StepIndex:     i,
			Status:        1, // pending
			Uses:          step.Uses,
			Action:        step.Action,
			Workspace:     tf.execCtx.StepWorkspace(task.Job.Name, step.Name),
			Timeout:       step.Timeout,
		}
		if argsMap := spec.StructAsMap(step.Args); len(argsMap) > 0 {
			if raw, err := sonic.MarshalString(argsMap); err == nil {
				sr.Args = raw
			}
		}
		if stepEnv := tf.execCtx.ResolveStepEnv(task.Job, step); len(stepEnv) > 0 {
			if raw, err := sonic.MarshalString(stepEnv); err == nil {
				sr.Env = raw
			}
		}
		if err := store.CreateStepRun(ctx, sr); err != nil {
			return fmt.Errorf("create step run %s: %w", step.Name, err)
		}

		stepPayloads = append(stepPayloads, taskqueue.StepPayload{
			Name:            step.Name,
			StepIndex:       int32(i),
			StepRunID:       stepRunID,
			Uses:            step.Uses,
			Action:          step.Action,
			Args:            spec.StructAsMap(step.Args),
			Env:             tf.execCtx.ResolveStepEnv(task.Job, step),
			ContinueOnError: step.ContinueOnError,
			Timeout:         step.Timeout,
			When:            step.When,
		})
	}

	payload := &taskqueue.JobRunTaskPayload{
		PipelineID:    tf.execCtx.PipelineIDRef,
		PipelineRunID: tf.execCtx.PipelineRunID,
		JobRunID:      jobRunID,
		JobName:       task.Job.Name,
		Steps:         stepPayloads,
		Env:           tf.execCtx.ResolveStepEnv(task.Job, nil),
		Workspace:     tf.execCtx.JobWorkspace(task.Job.Name),
		Timeout:       task.Job.Timeout,
		ArtifactURIs:  tf.execCtx.ArtifactURIs,
	}
	if task.Job.Source != nil {
		payload.Source = &taskqueue.SourcePayload{
			Type:   task.Job.Source.Type,
			Repo:   task.Job.Source.Repo,
			Branch: task.Job.Source.Branch,
		}
	}

	payloadBytes, err := sonic.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal job run payload: %w", err)
	}
	if _, err = tf.execCtx.TaskQueue.Enqueue(&nova.Task{
		Type:    taskqueue.TaskTypeJobRun,
		Payload: payloadBytes,
	}, nova.Queue("DEFAULT")); err != nil {
		return fmt.Errorf("enqueue job run task: %w", err)
	}

	tf.logger.Infow("job run enqueued to kafka", "jobRunId", jobRunID, "job", task.Job.Name)
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
		if err := tf.checkPause(ctx); err != nil {
			return err
		}
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
	// Use retry if configured
	if task.Job.Retry != nil && task.Job.Retry.MaxAttempts > 0 {
		delay := time.Duration(0)
		if task.Job.Retry.Delay != "" {
			var err error
			delay, err = time.ParseDuration(task.Job.Retry.Delay)
			if err != nil {
				return fmt.Errorf("invalid retry delay format: %w", err)
			}
		}

		var lastErr error
		err := retry.Do(ctx, func(ctx context.Context) error {
			task.RetryCount++
			lastErr = tf.executeStepOnce(ctx, task, step)
			return lastErr
		}, retry.WithMaxAttempts(int(task.Job.Retry.MaxAttempts)), retry.WithBackoff(retry.Fixed(delay)))
		if err != nil {
			return fmt.Errorf("step execution failed after retries: %w", lastErr)
		}
		return nil
	}

	return tf.executeStepOnce(ctx, task, step)
}

// executeStepOnce executes a step once
func (tf *TaskFramework) executeStepOnce(ctx context.Context, task *Task, step *spec.Step) error {
	// Evaluate when condition
	if step.When != "" {
		stepContext := map[string]any{
			"job": map[string]any{
				"name": task.Job.Name,
			},
			"step": map[string]any{
				"name": step.Name,
			},
		}
		ok, err := tf.execCtx.EvalConditionWithContext(step.When, stepContext)
		if err != nil {
			return fmt.Errorf("evaluate when condition: %w", err)
		}
		if !ok {
			return nil // Step skipped
		}
	}

	tf.emitStepEvent(plugin.EventTypeStepStarted, task, step.Name, map[string]any{
		"status": "started",
	})

	// Create step runner and execute
	stepRunner := NewStepRunner(tf.execCtx, task.Job, step)
	if err := stepRunner.Run(ctx); err != nil {
		if step.ContinueOnError {
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

	tf.emitStepEvent(plugin.EventTypeStepCompleted, task, step.Name, map[string]any{
		"status": "completed",
	})

	return nil
}

// handleSource handles source configuration by invoking the appropriate
// source plugin (e.g. "git") to populate the job workspace.
func (tf *TaskFramework) handleSource(_ context.Context, task *Task) error {
	src := task.Job.Source
	if src == nil {
		return nil
	}

	if tf.pluginManager == nil {
		tf.logger.Warnw("plugin manager not available, skipping source", "job", task.Name)
		return nil
	}

	workspace := tf.execCtx.JobWorkspace(task.Job.Name)
	sourceType := src.Type
	if sourceType == "" {
		sourceType = "git"
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
	paramsJSON, err := json.Marshal(params)
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
	for key, uri := range uris {
		tf.execCtx.ArtifactURIs[task.Job.Name+"/"+key] = uri
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
func (tf *TaskFramework) handleTarget(_ context.Context, _ *Task) error {
	// Implementation similar to JobRunner.handleTarget
	// This would use target plugins
	return nil
}
