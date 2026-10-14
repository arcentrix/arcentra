// Copyright 2026 Arcentra Authors.
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

package process

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// IJobRunStore abstracts DB operations needed by the execution process
// (TaskFramework) to manage JobRun and StepRun records without importing
// the full repo layer directly.
type IJobRunStore interface {
	CreateJobRun(ctx context.Context, jr *model.JobRun) error
	GetJobRun(ctx context.Context, jobRunID string) (*model.JobRun, error)
	GetJobRunStatus(ctx context.Context, jobRunID string) (int, error)
	UpdateJobRun(ctx context.Context, jobRunID string, updates map[string]any) error
	CreateStepRun(ctx context.Context, sr *model.StepRun) error
	UpdateStepRun(ctx context.Context, stepRunID string, updates map[string]any) error
}

// JobRunStore adapts repo interfaces into IJobRunStore.
type JobRunStore struct {
	jobRunRepo  repo.IJobRunRepository
	stepRunRepo repo.IStepRunRepository
}

// NewJobRunStore creates a new store backed by the given repositories.
func NewJobRunStore(jr repo.IJobRunRepository, sr repo.IStepRunRepository) *JobRunStore {
	return &JobRunStore{jobRunRepo: jr, stepRunRepo: sr}
}

// CreateJobRun persists a new job run record.
func (s *JobRunStore) CreateJobRun(ctx context.Context, jr *model.JobRun) error {
	return s.jobRunRepo.Create(ctx, jr)
}

// GetJobRun returns the full job run record by business ID.
func (s *JobRunStore) GetJobRun(ctx context.Context, jobRunID string) (*model.JobRun, error) {
	return s.jobRunRepo.GetByJobRunID(ctx, jobRunID)
}

// GetJobRunStatus returns the current status of a job run (lightweight).
func (s *JobRunStore) GetJobRunStatus(ctx context.Context, jobRunID string) (int, error) {
	return s.jobRunRepo.GetStatus(ctx, jobRunID)
}

// UpdateJobRun patches a job run record by its business ID.
func (s *JobRunStore) UpdateJobRun(ctx context.Context, jobRunID string, updates map[string]any) error {
	return s.jobRunRepo.UpdateByJobRunID(ctx, jobRunID, updates)
}

// CreateStepRun persists a new step run record.
func (s *JobRunStore) CreateStepRun(ctx context.Context, sr *model.StepRun) error {
	return s.stepRunRepo.Create(ctx, sr)
}

// UpdateStepRun patches a step run record by its business ID.
func (s *JobRunStore) UpdateStepRun(ctx context.Context, stepRunID string, updates map[string]any) error {
	return s.stepRunRepo.PatchByStepRunID(ctx, stepRunID, updates)
}

// UpdateJobRunIfStatus 只更新仍处于观察状态的作业，保护 Agent 并发终态报告
func (s *JobRunStore) UpdateJobRunIfStatus(ctx context.Context, jobRunID string, status int, updates map[string]any) error {
	current, err := s.jobRunRepo.GetByJobRunID(ctx, jobRunID)
	if err != nil {
		return err
	}
	if current != nil && (current.RetryPlan != "" || current.RetryPlanDigest != "") {
		if current.JobRunID != jobRunID || current.Status != status {
			return fmt.Errorf("retry job changed before controller finalization")
		}
		finalizer, ok := s.jobRunRepo.(interface {
			FinalizeRetryJobIfCurrent(context.Context, *model.JobRun, map[string]any) error
		})
		if !ok {
			return fmt.Errorf("fenced retry job finalization unavailable")
		}
		return finalizer.FinalizeRetryJobIfCurrent(ctx, current, updates)
	}
	if next, _ := updates["status"].(int); next == model.JobRunStatusCancelled {
		canceller, ok := s.jobRunRepo.(interface {
			CancelAgentJobIfCurrent(context.Context, string, int, map[string]any) error
		})
		if !ok {
			return fmt.Errorf("atomic agent cancellation unavailable")
		}
		return canceller.CancelAgentJobIfCurrent(ctx, jobRunID, status, updates)
	}
	updater, ok := s.jobRunRepo.(interface {
		PatchStatusIfCurrent(context.Context, string, int, map[string]any) error
	})
	if !ok {
		return fmt.Errorf("conditional job status updates unavailable")
	}
	return updater.PatchStatusIfCurrent(ctx, jobRunID, status, updates)
}

// FinishOpenStepRuns 仅结束尚未达到终态的步骤
func (s *JobRunStore) FinishOpenStepRuns(ctx context.Context, stepIDs []string, status int) error {
	updater, ok := s.stepRunRepo.(interface {
		PatchStatusIfCurrent(context.Context, string, int, map[string]any) error
	})
	if !ok {
		return fmt.Errorf("conditional step status updates unavailable")
	}
	for _, stepID := range stepIDs {
		step, err := s.stepRunRepo.GetByStepRunID(ctx, stepID)
		if err != nil {
			return err
		}
		if step == nil || step.Status >= 4 {
			continue
		}
		if err := updater.PatchStatusIfCurrent(ctx, stepID, step.Status, map[string]any{"status": status, "end_time": time.Now()}); err != nil {
			// 并发状态变化时保留 Agent 的结果，下一次读取可反映终态
			current, readErr := s.stepRunRepo.GetByStepRunID(ctx, stepID)
			if readErr != nil || current == nil || current.Status < 4 {
				return err
			}
		}
	}
	return nil
}

// CaptureSourceExpectation 在排队前原子绑定源请求和目标 Agent
func (s *JobRunStore) CaptureSourceExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding) error {
	writer, ok := s.jobRunRepo.(interface {
		CaptureSourceExpectation(context.Context, string, string, string, string, *sourceprovenance.Binding) error
	})
	if !ok {
		return fmt.Errorf("atomic job source capture unavailable")
	}
	return writer.CaptureSourceExpectation(ctx, pipelineID, runID, jobID, agentID, binding)
}

// CaptureRuntimeExpectation 原子保存作业运行时与源请求，缺少专用持久化能力时关闭派发
func (s *JobRunStore) CaptureRuntimeExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest) error {
	writer, ok := s.jobRunRepo.(interface {
		CaptureRuntimeExpectation(context.Context, string, string, string, string, *sourceprovenance.Binding, *taskqueue.RuntimeRequest) error
	})
	if !ok {
		return fmt.Errorf("atomic runtime capture unavailable")
	}
	return writer.CaptureRuntimeExpectation(ctx, pipelineID, runID, jobID, agentID, binding, runtime)
}

// CaptureRuntimeInputExpectation 原子保存控制面选定的输入与源及运行时身份
func (s *JobRunStore) CaptureRuntimeInputExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, uris map[string]string, refs map[string]taskqueue.ArtifactReference) error {
	writer, ok := s.jobRunRepo.(interface {
		CaptureRuntimeInputExpectation(context.Context, string, string, string, string, *sourceprovenance.Binding, *taskqueue.RuntimeRequest, map[string]string, map[string]taskqueue.ArtifactReference) error
	})
	if !ok {
		return fmt.Errorf("atomic job input capture unavailable")
	}
	return writer.CaptureRuntimeInputExpectation(ctx, pipelineID, runID, jobID, agentID, binding, runtime, uris, refs)
}

// CaptureRuntimeRetryExpectation 保存不可扩大重试计划及原始输入授权
func (s *JobRunStore) CaptureRuntimeRetryExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, uris map[string]string, refs map[string]taskqueue.ArtifactReference, plan *taskqueue.JobRetryPlan) error {
	writer, ok := s.jobRunRepo.(interface {
		CaptureRuntimeRetryExpectation(context.Context, string, string, string, string, *sourceprovenance.Binding, *taskqueue.RuntimeRequest, map[string]string, map[string]taskqueue.ArtifactReference, *taskqueue.JobRetryPlan) error
	})
	if !ok {
		return fmt.Errorf("atomic retry plan capture unavailable")
	}
	return writer.CaptureRuntimeRetryExpectation(ctx, pipelineID, runID, jobID, agentID, binding, runtime, uris, refs, plan)
}

// CaptureRuntimeRetryApprovalExpectation 保存需人工审批的不可扩大重试计划及可信运行上下文
func (s *JobRunStore) CaptureRuntimeRetryApprovalExpectation(ctx context.Context, pipelineID, runID, jobID, agentID string, binding *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, uris map[string]string, refs map[string]taskqueue.ArtifactReference, plan *taskqueue.JobRetryPlan) error {
	writer, ok := s.jobRunRepo.(interface {
		CaptureRuntimeRetryApprovalExpectation(context.Context, string, string, string, string, *sourceprovenance.Binding, *taskqueue.RuntimeRequest, map[string]string, map[string]taskqueue.ArtifactReference, *taskqueue.JobRetryPlan) error
	})
	if !ok {
		return fmt.Errorf("atomic retry plan capture unavailable")
	}
	return writer.CaptureRuntimeRetryApprovalExpectation(ctx, pipelineID, runID, jobID, agentID, binding, runtime, uris, refs, plan)
}
