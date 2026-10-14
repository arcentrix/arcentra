// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

var jobRuntimeColumns = []string{"runtime_type", "runtime_image", "runtime_cpu", "runtime_memory", "runtime_binding_digest"}

// CaptureRuntimeExpectation 原子冻结运行时、源请求及 Agent 归属，禁止重新派发或降级
func (r *JobRunRepo) CaptureRuntimeExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, source *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest) error {
	if runtime == nil {
		return fmt.Errorf("runtime expectation is required")
	}
	copy := *runtime
	digest, err := taskqueue.RuntimeBindingDigest(pipelineID, runID, jobID, agentID, &copy)
	if err != nil || digest != copy.BindingDigest {
		return fmt.Errorf("runtime binding does not match execution identity")
	}
	return r.captureDispatchExpectation(ctx, pipelineID, runID, jobID, agentID, source, &copy, nil)
}

func runtimeField(key string) bool {
	return strings.HasPrefix(strings.ReplaceAll(strings.ToLower(key), "_", ""), "runtime")
}
func emptyRuntime(job *model.JobRun) bool {
	return job.RuntimeType == "" && job.RuntimeImage == "" && job.RuntimeCPU == "" && job.RuntimeMemory == "" && job.RuntimeBindingDigest == ""
}
func jobRuntimeEqual(a, b *model.JobRun) bool {
	return a.RuntimeType == b.RuntimeType && a.RuntimeImage == b.RuntimeImage && a.RuntimeCPU == b.RuntimeCPU && a.RuntimeMemory == b.RuntimeMemory && a.RuntimeBindingDigest == b.RuntimeBindingDigest
}

// InspectJobExecution 从主库读取原始运行时认领状态，不变更认领、结果或权限
func (r *JobRunRepo) InspectJobExecution(ctx context.Context, agentID, jobID, claim, binding string) (int, error) {
	if agentID == "" || jobID == "" || claim == "" || binding == "" {
		return 0, fmt.Errorf("exact original runtime execution identity is required")
	}
	var current model.JobRun
	if err := r.Database().WithContext(ctx).Clauses(database.Write()).Select(jobSnapshotColumns).Where("job_run_id = ?", jobID).First(&current).Error; err != nil {
		return 0, err
	}
	if current.JobRunID != jobID || current.AgentID != agentID || current.ClaimID != claim || current.RuntimeBindingDigest != binding || !current.SourceExpectationCaptured {
		return 0, fmt.Errorf("original runtime execution ownership does not match")
	}
	request := &taskqueue.RuntimeRequest{Type: current.RuntimeType, Image: current.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: current.RuntimeCPU, Memory: current.RuntimeMemory}}
	digest, err := taskqueue.RuntimeBindingDigest(current.PipelineID, current.PipelineRunID, current.JobRunID, current.AgentID, request)
	if err != nil || digest != binding {
		return 0, fmt.Errorf("stored runtime binding is invalid")
	}
	return current.Status, nil
}
