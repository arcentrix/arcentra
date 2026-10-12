// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/retry"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func stepAttemptIDs(step taskqueue.StepPayload) []string {
	if len(step.AttemptRunIDs) > 0 {
		return step.AttemptRunIDs
	}
	return []string{step.StepRunID}
}

func allJobAttemptIDs(payload *taskqueue.JobRunTaskPayload) []string {
	ids := []string{}
	for _, step := range payload.Steps {
		ids = append(ids, stepAttemptIDs(step)...)
	}
	return ids
}

func validateJobAttempts(payload *taskqueue.JobRunTaskPayload) error {
	if err := retry.Validate(payload.Retry); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, step := range payload.Steps {
		ids := stepAttemptIDs(step)
		if len(ids) != retry.Attempts(payload.Retry) || ids[0] != step.StepRunID {
			return fmt.Errorf("step %q has an invalid preallocated attempt plan", step.Name)
		}
		for _, id := range ids {
			if id == "" || seen[id] {
				return fmt.Errorf("attempt IDs must be nonempty and distinct")
			}
			seen[id] = true
		}
	}
	return nil
}

func executeJobAttempts(ctx, jobCtx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager, payload *taskqueue.JobRunTaskPayload, artifacts *artifactExecutor, workspace string, preparationErr error, start time.Time) error {
	visited := make(map[string]bool)
	steps := make([]retry.Step, len(payload.Steps))
	for i, step := range payload.Steps {
		steps[i] = retry.Step{Uses: step.Uses, ContinueOnError: step.ContinueOnError}
	}
	skip := func(stepID, reason string, status steprunv1.StepRunStatus) error {
		if visited[stepID] {
			return nil
		}
		visited[stepID] = true
		end := time.Now().Unix()
		return retainStepReport(ctx, stepID, nil, func(reportCtx context.Context) error {
			return reportStepRunStatus(reportCtx, client, conf, stepID, status, 0, reason, start, end, nil)
		})
	}
	normalize := func(stepID string, err error) error {
		if terminalPersistenceFailed(err) || terminalQuarantined(err) {
			return retry.Block(err)
		}
		if pending, ok := pendingStepReports.Load(receiptKey(ctx, "step", stepID)); ok {
			return retry.Block(pending.(*executionReceipt).executionErr)
		}
		if executor.ExecutionContextCause(err) != nil || errors.Is(err, executor.ErrBuildLogPersistence) {
			return retry.Block(err)
		}
		return err
	}
	jobErr := preparationErr
	if jobErr == nil {
		jobErr = retry.Run(jobCtx, payload.Retry, steps, func(index, attempt int) error {
			step := payload.Steps[index]
			ids := stepAttemptIDs(step)
			stepID := ids[attempt-1]
			if attempt > 1 {
				if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
					return retry.Block(fmt.Errorf("retry workspace unavailable; a fresh run is required"))
				}
				if payload.Retry.Mode == "whole_job" {
					// Close every unvisited slot from the preceding round before claiming a later attempt.
					for _, previous := range payload.Steps {
						for _, previousID := range stepAttemptIDs(previous)[:attempt-1] {
							if err := skip(previousID, "attempt not used", steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED); err != nil {
								return retry.Block(err)
							}
						}
					}
				}
			}
			visited[stepID] = true
			env := mergeEnv(payload.Env, step.Env)
			allowed, conditionErr := evaluateStepCondition(step.When, payload.JobName, step.Name, env, payload.Variables)
			if conditionErr != nil || !allowed {
				status, reason := steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED, "condition evaluated to false"
				if conditionErr != nil {
					conditionErr = redactJobError(payload, conditionErr)
					status, reason = steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, conditionErr.Error()
				}
				stepStart := time.Now()
				err := retainStepReport(ctx, stepID, conditionErr, func(reportCtx context.Context) error {
					return reportStepRunStatus(reportCtx, client, conf, stepID, status, 0, reason, stepStart, stepStart.Unix(), nil)
				})
				return normalize(stepID, err)
			}
			request := &taskqueue.StepRunTaskPayload{PipelineID: payload.PipelineID, PipelineRunID: payload.PipelineRunID, JobID: payload.JobRunID, JobName: payload.JobName, StepName: step.Name, StepIndex: step.StepIndex, StepRunID: stepID, Uses: step.Uses, Action: step.Action, Args: step.Args, Env: env, Workspace: workspace, Timeout: step.Timeout, AgentID: payload.AgentID}
			var err error
			if strings.EqualFold(strings.TrimSpace(step.Uses), "builtin:artifacts") {
				err = executeStepRunViaExecutor(jobCtx, conf, client, manager, request, artifacts)
			} else if manager != nil {
				err = executeStepRunViaExecutor(jobCtx, conf, client, manager, request)
			} else {
				err = executeStepRun(jobCtx, conf, client, request)
			}
			return normalize(stepID, err)
		})
	}
	// A persistence outage after the last actual command does not change its outcome.
	// The final job receipt still cannot commit until all step records are durable and acknowledged.
	if terminalPersistenceFailed(jobErr) {
		allVisited, executionFailed := true, false
		for _, step := range payload.Steps {
			attempted := false
			for _, id := range stepAttemptIDs(step) {
				attempted = attempted || visited[id]
				if pending, ok := pendingStepReports.Load(receiptKey(ctx, "step", id)); ok && pending.(*executionReceipt).executionErr != nil {
					executionFailed = true
				}
			}
			allVisited = allVisited && attempted
		}
		if allVisited && !executionFailed {
			jobErr = nil
		}
	}

	for _, step := range payload.Steps {
		for _, stepID := range stepAttemptIDs(step) {
			reason := "attempt not used"
			status := steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED
			// 日志持久化内部取消仍属于失败，不能冒充控制面或用户取消
			if jobCtx.Err() != nil || errors.Is(jobErr, context.Canceled) && !errors.Is(jobErr, executor.ErrBuildLogPersistence) {
				status = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
				reason = context.Canceled.Error()
				if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
					status = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
					reason = context.DeadlineExceeded.Error()
				}
			}
			_ = skip(stepID, reason, status) // Final receipt stages and delivers every pending attempt before the job.

		}
	}
	return jobErr
}
