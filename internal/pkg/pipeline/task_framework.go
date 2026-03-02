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
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
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
		task.MarkCompleted(TaskStateFailed, err)
		tf.emitJobEvent(plugin.EventTypeJobFailed, task, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return fmt.Errorf("wait task %s: %w", task.Name, err)
	}

	task.MarkCompleted(TaskStateSucceeded, nil)
	tf.emitJobEvent(plugin.EventTypeJobCompleted, task, map[string]any{
		"status": "succeeded",
	})
	return nil
}

// prepare validates and prepares task execution
func (tf *TaskFramework) prepare(_ context.Context, task *Task) error {
	task.State = TaskStatePrepared

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
	now := time.Now()
	task.StartedAt = &now
	task.State = TaskStateStarted

	tf.logger.Infow("task started", "task", task.Name)
	tf.emitJobEvent(plugin.EventTypeJobStarted, task, map[string]any{
		"status": "started",
	})
	return nil
}

// queue queues task for execution
func (tf *TaskFramework) queue(_ context.Context, task *Task) error {
	task.State = TaskStateQueued

	tf.logger.Infow("task queued", "task", task.Name)
	if tf.execCtx.TaskQueue != nil {
		for i := range task.Job.Steps {
			step := task.Job.Steps[i]
			if step == nil {
				continue
			}
			if !step.RunOnAgent {
				continue
			}
			stepRunId := fmt.Sprintf("%s-%s-%s", tf.execCtx.Pipeline.Namespace, task.Job.Name, step.Name)
			payload := &taskqueue.StepRunTaskPayload{
				PipelineId: tf.execCtx.Pipeline.Namespace,
				JobId:      fmt.Sprintf("%s-%s", tf.execCtx.Pipeline.Namespace, task.Job.Name),
				JobName:    task.Job.Name,
				StepName:   step.Name,
				StepIndex:  int32(i),
				StepRunId:  stepRunId,
				Uses:       step.Uses,
				Action:     step.Action,
				Args:       spec.StructAsMap(step.Args),
				Env:        tf.execCtx.ResolveStepEnv(task.Job, step),
				Workspace:  tf.execCtx.StepWorkspace(task.Job.Name, step.Name),
				Timeout:    step.Timeout,
			}
			payloadBytes, err := sonic.Marshal(payload)
			if err != nil {
				return fmt.Errorf("marshal step run payload: %w", err)
			}
			_, err = tf.execCtx.TaskQueue.Enqueue(&nova.Task{
				Type:    taskqueue.TaskTypeStepRun,
				Payload: payloadBytes,
			}, nova.Queue("DEFAULT"))
			if err != nil {
				return fmt.Errorf("enqueue step run task: %w", err)
			}
		}
	}
	return nil
}

// wait waits for task completion by executing steps
func (tf *TaskFramework) wait(ctx context.Context, task *Task) error {
	task.State = TaskStateRunning

	// Execute steps sequentially
	for i := range task.Job.Steps {
		step := task.Job.Steps[i]
		if step == nil {
			continue
		}
		if err := tf.executeStep(ctx, task, step); err != nil {
			// Handle failure notification
			if task.Job.Notify != nil && task.Job.Notify.OnFailure != nil {
				_ = tf.execCtx.SendNotification(ctx, task.Job.Notify.OnFailure, false)
			}
			return fmt.Errorf("step %s failed: %w", step.Name, err)
		}
	}

	// Handle target deployment if specified
	if task.Job.Target != nil {
		if err := tf.handleTarget(ctx, task); err != nil {
			return fmt.Errorf("handle target: %w", err)
		}
	}

	// Send success notification
	if task.Job.Notify != nil && task.Job.Notify.OnSuccess != nil {
		_ = tf.execCtx.SendNotification(ctx, task.Job.Notify.OnSuccess, true)
	}

	return nil
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

// handleSource handles source configuration
func (tf *TaskFramework) handleSource(_ context.Context, _ *Task) error {
	// Implementation similar to JobRunner.handleSource
	// This would use workspace manager and source plugins
	return nil
}

// handleApproval handles approval configuration
func (tf *TaskFramework) handleApproval(_ context.Context, _ *Task) error {
	// Implementation similar to JobRunner.handleApproval
	// This would use approval manager
	return nil
}

// handleTarget handles target deployment
func (tf *TaskFramework) handleTarget(_ context.Context, _ *Task) error {
	// Implementation similar to JobRunner.handleTarget
	// This would use target plugins
	return nil
}
