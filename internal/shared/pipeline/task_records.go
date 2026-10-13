package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/retry"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/bytedance/sonic"
)

func (tf *TaskFramework) createRecords(ctx context.Context, task *Task) error {
	if err := retry.Validate(task.Job.Retry); err != nil {
		return err
	}

	tf.execCtx.JobRunID = id.GetUild()
	task.Set("jobRunID", tf.execCtx.JobRunID)
	task.Set("stepStatuses", map[string]int{})
	store := tf.getJobRunStore()
	if store != nil {
		if err := store.CreateJobRun(ctx, &model.JobRun{JobRunID: tf.execCtx.JobRunID, PipelineID: tf.execCtx.PipelineIDRef,
			PipelineRunID: tf.execCtx.PipelineRunID, JobName: task.Job.Name, Status: model.JobRunStatusPending,
			Priority: 5, Timeout: task.Job.Timeout, TotalSteps: len(task.Job.Steps), Env: "{}", ArtifactURIs: "{}", ArtifactReferences: "{}"}); err != nil {
			return fmt.Errorf("create job run: %w", err)
		}
	}
	if tf.execCtx.StepAttemptIDs == nil {
		tf.execCtx.StepAttemptIDs = make(map[string][]string)
	}
	if tf.execCtx.StepRunIDs == nil {
		tf.execCtx.StepRunIDs = make(map[string]string)
	}
	task.Set("attemptStatuses", map[string]int{})
	for i, step := range task.Job.Steps {
		if step == nil {
			continue
		}
		previous := ""
		for attempt := 1; attempt <= retry.Attempts(task.Job.Retry); attempt++ {
			stepID := id.GetUild()
			tf.execCtx.StepAttemptIDs[step.Name] = append(tf.execCtx.StepAttemptIDs[step.Name], stepID)
			if attempt == 1 {
				tf.execCtx.StepRunIDs[step.Name] = stepID
			}
			if store != nil {
				args, err := sonic.MarshalString(spec.StructAsMap(step.Args))
				if err != nil {
					return err
				}
				policyJSON, err := sonic.MarshalString(task.Job.Retry)
				if err != nil {
					return err
				}
				mode := ""
				if task.Job.Retry != nil {
					mode = task.Job.Retry.Mode
				}
				sr := &model.StepRun{StepRunID: stepID, PipelineID: tf.execCtx.PipelineIDRef, PipelineRunID: tf.execCtx.PipelineRunID,
					JobID: tf.execCtx.PipelineIDRef + "-" + task.Job.Name, JobRunID: tf.execCtx.JobRunID, Name: step.Name,
					CreatedBy: tf.execCtx.TriggeredBy, StepIndex: i, Attempt: attempt, RetryMode: mode, RetryPolicy: policyJSON, PreviousStepRunID: previous, Status: 1, Priority: 5,
					Uses: step.Uses, Action: step.Action, Args: args, Env: "{}", Secrets: "{}", LabelSelector: "{}", Timeout: step.Timeout, When: step.When}
				if step.ContinueOnError {
					sr.ContinueOnError = 1
				}
				if err := store.CreateStepRun(ctx, sr); err != nil {
					return fmt.Errorf("create step run %s attempt %d: %w", step.Name, attempt, err)
				}
			}
			previous = stepID
		}
	}
	return nil
}

func (tf *TaskFramework) finishRecords(ctx context.Context, task *Task, runErr error) {
	store := tf.getJobRunStore()
	if store == nil {
		return
	}
	if _, failed := task.Get("recordError"); failed {
		return
	}
	// 取消执行不能取消最终状态的持久化
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	status := model.JobRunStatusSuccess
	switch {
	case task.State == TaskStateSkipped:
		status = model.JobRunStatusSkipped
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		status = model.JobRunStatusTimeout
	case errors.Is(ctx.Err(), context.Canceled):
		status = model.JobRunStatusCancelled
	case errors.Is(executor.ExecutionContextCause(runErr), context.DeadlineExceeded):
		status = model.JobRunStatusTimeout
	case errors.Is(executor.ExecutionContextCause(runErr), context.Canceled):
		status = model.JobRunStatusCancelled
	case errors.Is(runErr, executor.ErrBuildLogPersistence):
		status = model.JobRunStatusFailed
	case errors.Is(runErr, context.DeadlineExceeded):
		status = model.JobRunStatusTimeout
	case errors.Is(runErr, context.Canceled):
		status = model.JobRunStatusCancelled
	case runErr != nil:
		status = model.JobRunStatusFailed
	}
	if isAgentJob(task.Job) && (status == model.JobRunStatusTimeout || status == model.JobRunStatusCancelled) && tf.execCtx.CancelJob != nil {
		agent, _ := task.Get("agentID")
		if agentID, ok := agent.(string); ok && agentID != "" {
			if err := tf.execCtx.CancelJob(persistCtx, agentID, tf.execCtx.JobRunID, "job execution cancelled or timed out"); err != nil {
				tf.logger.Errorw("cancel remote job failed", "jobRunId", tf.execCtx.JobRunID, "error", err)
			}
		}
	}
	remoteStatus := 0
	// Agent 终态由其报告决定，控制面轮询错误不能覆盖成功、失败、取消或超时
	if isAgentJob(task.Job) {
		remote, err := store.GetJobRun(persistCtx, tf.execCtx.JobRunID)
		if err != nil {
			tf.logger.Errorw("read remote terminal state failed", "error", err)
			return
		}
		if remote == nil || model.IsJobRunTerminal(remote.Status) {
			return
		}
		remoteStatus = remote.Status
		if runErr == nil && status == model.JobRunStatusSuccess {
			return
		}
	}
	now := time.Now()
	updates := map[string]any{"status": status, "end_time": now}
	if task.StartedAt != nil {
		updates["duration"] = now.Sub(*task.StartedAt).Milliseconds()
	}
	if runErr != nil {
		updates["error_message"] = runErr.Error()
	}
	if !isAgentJob(task.Job) || task.State == TaskStateSkipped {
		states, _ := task.Get("stepStatuses")
		stepStates, _ := states.(map[string]int)
		attemptsValue, _ := task.Get("attemptStatuses")
		attemptStates, _ := attemptsValue.(map[string]int)
		for _, ids := range tf.execCtx.StepAttemptIDs {
			for index, stepID := range ids {
				if _, done := attemptStates[stepID]; done {
					continue
				}
				attemptStatus, reason := 8, "attempt not used"
				if index == 0 && (status == model.JobRunStatusCancelled || status == model.JobRunStatusTimeout) {
					attemptStatus, reason = status, "job cancelled or timed out before step execution"
				}
				if err := store.UpdateStepRun(persistCtx, stepID, map[string]any{"status": attemptStatus, "end_time": now, "error_message": reason}); err != nil {
					tf.logger.Errorw("finalize unused attempt failed", "error", err)
					return
				}
			}
		}
		completed, failed := 0, 0
		for _, step := range task.Job.Steps {
			if step == nil {
				continue
			}
			st, done := stepStates[step.Name]
			if !done {
				st = 8
				if status == model.JobRunStatusCancelled || status == model.JobRunStatusTimeout {
					st = status
				}
				if len(tf.execCtx.StepAttemptIDs[step.Name]) == 0 {
					if err := store.UpdateStepRun(persistCtx, tf.execCtx.StepRunIDs[step.Name], map[string]any{"status": st, "end_time": now}); err != nil {
						tf.logger.Errorw("finalize pending step failed", "error", err)
						return
					}
				}
			}
			if st >= 4 {
				completed++
			}
			if st == 5 || st == 7 {
				failed++
			}
		}
		updates["completed_steps"], updates["failed_steps"] = completed, failed
	}
	if isAgentJob(task.Job) {
		updater, ok := store.(interface {
			UpdateJobRunIfStatus(context.Context, string, int, map[string]any) error
		})
		if !ok {
			tf.logger.Errorw("conditional remote status updater unavailable")
			return
		}
		if err := updater.UpdateJobRunIfStatus(persistCtx, tf.execCtx.JobRunID, remoteStatus, updates); err != nil {
			tf.logger.Warnw("remote job state changed during finalization", "error", err)
			return
		}
		if finisher, ok := store.(interface {
			FinishOpenStepRuns(context.Context, []string, int) error
		}); ok {
			ids := make([]string, 0, len(tf.execCtx.StepRunIDs))
			for _, attempts := range tf.execCtx.StepAttemptIDs {
				ids = append(ids, attempts...)
			}
			if len(ids) == 0 {
				for _, stepID := range tf.execCtx.StepRunIDs {
					ids = append(ids, stepID)
				}
			}
			stepStatus := 8
			if status == model.JobRunStatusCancelled || status == model.JobRunStatusTimeout {
				stepStatus = status
			}
			if err := finisher.FinishOpenStepRuns(persistCtx, ids, stepStatus); err != nil {
				tf.logger.Errorw("finish pending remote steps failed", "error", err)
			}
		}
		return
	}
	if err := store.UpdateJobRun(persistCtx, tf.execCtx.JobRunID, updates); err != nil {
		tf.logger.Errorw("finalize job failed", "jobRunId", tf.execCtx.JobRunID, "error", err)
	}
}

func (tf *TaskFramework) recordStep(ctx context.Context, task *Task, step *spec.Step, status int, start time.Time, err error) error {
	states, _ := task.Get("stepStatuses")
	if values, ok := states.(map[string]int); ok {
		values[step.Name] = status
	}
	store := tf.getJobRunStore()
	if store == nil {
		return nil
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := time.Now()
	updates := map[string]any{"status": status, "end_time": now, "duration": now.Sub(start).Milliseconds()}
	if err != nil {
		updates["error_message"] = err.Error()
	}
	if status == 4 {
		updates["exit_code"] = 0
	}
	if e := store.UpdateStepRun(persistCtx, tf.execCtx.StepRunIDs[step.Name], updates); e != nil {
		task.Set("recordError", e)
		return retry.Block(fmt.Errorf("finalize step attempt: %w", e))
	}
	if states, ok := task.Get("attemptStatuses"); ok {
		if values, ok := states.(map[string]int); ok {
			values[tf.execCtx.StepRunIDs[step.Name]] = status
		}
	}
	return nil
}

func (tf *TaskFramework) skipPreviousAttempts(ctx context.Context, task *Task, beforeAttempt int) error {
	value, _ := task.Get("attemptStatuses")
	states, _ := value.(map[string]int)
	store := tf.getJobRunStore()
	if store == nil {
		return nil
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, ids := range tf.execCtx.StepAttemptIDs {
		for index, stepID := range ids {
			if index+1 >= beforeAttempt {
				break
			}
			if _, exists := states[stepID]; exists {
				continue
			}
			if err := store.UpdateStepRun(persistCtx, stepID, map[string]any{"status": 8, "end_time": time.Now(), "error_message": "attempt not used"}); err != nil {
				task.Set("recordError", err)
				return retry.Block(err)
			}
			states[stepID] = 8
		}
	}
	return nil
}
