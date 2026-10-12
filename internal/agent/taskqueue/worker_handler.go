// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

func newWorkerHandler(workerCtx context.Context, agentConf *config.AgentConfig, grpcClient *grpc.ClientWrapper, execManager *executor.Manager, reports *terminalOutbox) nova.HandlerFunc {
	return nova.HandlerFunc(func(ctx context.Context, task *nova.Task) error {
		ctx = withTerminalOutbox(ctx, reports)
		if task == nil {
			return nil
		}
		if workerCtx.Err() != nil {
			return workerCtx.Err()
		}
		if err := reports.replayBound(ctx, grpcClient, agentConf); err != nil {
			return &terminalDeliveryError{err}
		}
		switch task.Type {
		case taskqueue.TaskTypeJobRun:
			var payload taskqueue.JobRunTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				log.Warnw("discarding malformed job task", "error", err)
				return nil
			}
			if payload.AgentID != agentConf.Agent.ID {
				log.Warnw("discarding job addressed to a different agent", "jobRunID", payload.JobRunID)
				return nil
			}
			log.Infow("received job run task",
				"jobRunId", payload.JobRunID,
				"jobName", payload.JobName,
				"steps", len(payload.Steps),
			)
			return acknowledgeCompleted(executeJobRun(ctx, agentConf, grpcClient, execManager, &payload))
		case taskqueue.TaskTypeStepRun:
			var payload taskqueue.StepRunTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				log.Warnw("discarding malformed step task", "error", err)
				return nil
			}
			if payload.AgentID != agentConf.Agent.ID {
				log.Warnw("discarding step addressed to a different agent", "stepRunID", payload.StepRunID)
				return nil
			}
			log.Infow("received step run task",
				"stepRunId", payload.StepRunID,
				"jobName", payload.JobName,
				"stepName", payload.StepName,
			)
			if execManager != nil {
				return acknowledgeCompleted(executeStepRunViaExecutor(ctx, agentConf, grpcClient, execManager, &payload))
			}
			return acknowledgeCompleted(executeStepRun(ctx, agentConf, grpcClient, &payload))
		default:
			log.Debugw("unknown task type", "type", task.Type)
			return nil
		}
	})
}
