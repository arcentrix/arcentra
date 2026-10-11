package process

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

func (e *Process) cancelJobRunOnAgent(ctx context.Context, agentID, jobRunID, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.taskQueue == nil {
		return fmt.Errorf("task queue is not configured")
	}
	data, err := sonic.Marshal(taskqueue.JobCancelTaskPayload{AgentID: agentID, JobRunID: jobRunID, Reason: reason})
	if err != nil {
		return err
	}
	_, err = e.taskQueue.Enqueue(&nova.Task{Type: taskqueue.TaskTypeJobCancel, Payload: data}, nova.Queue(taskqueue.AgentControlQueue(agentID)))
	return err
}
